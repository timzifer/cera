package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/timzifer/cera"
)

// The subcommands: what the library does besides drawing pages, without
// writing Go. Each has its own flags; `cera <command> -h` lists them.

// command runs a subcommand with its arguments and returns the exit code.
type command struct {
	usage, about string
	run          func(c *cli, args []string) error
}

// cli is what a subcommand writes to and the flags all of them have.
type cli struct {
	out, err io.Writer
	fs       *flag.FlagSet
	password *string
}

var commands = map[string]command{
	"text":   {"cera text [-pages 1-3] [-json] input.pdf", "the text of pages, plain or with the place of every line", cmdText},
	"search": {"cera search [-match-case] [-whole-word] [-pages 1-3] input.pdf query", "where a text is found, with page and context", cmdSearch},
	"info":   {"cera info [-json] [-unsupported=false] input.pdf", "what cera makes of a file: metadata, pages, layers, form, signatures, what it does not draw exactly", cmdInfo},
	"merge":  {"cera merge -o out.pdf a.pdf b.pdf ...", "the pages of several files in one, with their outlines, labels and fields", cmdMerge},
	"split":  {"cera split [-every 1] [-pages 1-3] -o 'part-%d.pdf' input.pdf", "pages as files of their own", cmdSplit},
	"rotate": {"cera rotate -by 90 [-pages 1-3] -o out.pdf input.pdf", "pages turned", cmdRotate},
	"delete": {"cera delete -pages 2,5-7 -o out.pdf input.pdf", "pages taken out", cmdDelete},
	"fill":   {"cera fill -field name=value ... [-flatten] [-update] -o out.pdf input.pdf", "a form filled in and written", cmdFill},
}

// errUsage is returned for arguments a command cannot do anything with.
var errUsage = errors.New("usage")

// runCommand runs the subcommand name; false when there is none of that
// name.
func runCommand(name string, args []string, stdout, stderr io.Writer) (code int, ok bool) {
	cmd, ok := commands[name]
	if !ok {
		return 0, false
	}
	c := &cli{out: stdout, err: stderr, fs: flag.NewFlagSet(name, flag.ContinueOnError)}
	c.fs.SetOutput(stderr)
	c.password = c.fs.String("password", "", "password of an encrypted file")
	c.fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s\n  %s\n", cmd.usage, cmd.about)
		c.fs.PrintDefaults()
	}
	switch err := cmd.run(c, args); {
	case err == nil:
		return 0, true
	case errors.Is(err, flag.ErrHelp):
		return 0, true
	case errors.Is(err, errUsage):
		c.fs.Usage()
		return 2, true
	default:
		fmt.Fprintln(stderr, "cera:", err)
		return 1, true
	}
}

// parse parses the flags and wants n arguments after them (-1: one or
// more).
func (c *cli) parse(args []string, n int) error {
	if err := c.fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errUsage
	}
	if got := c.fs.NArg(); got != n && (n >= 0 || got < 1) {
		return errUsage
	}
	return nil
}

func (c *cli) open(name string) (*cera.Document, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	doc, err := cera.OpenWith(data, cera.OpenOptions{Password: *c.password})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return doc, nil
}

// parsePages reads a selection of pages, "1-3,5,8-", of a document of n:
// their indices in the order given; all pages for none.
func parsePages(spec string, n int) ([]int, error) {
	var out []int
	if strings.TrimSpace(spec) == "" {
		for i := range n {
			out = append(out, i)
		}
		return out, nil
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		b := a
		if isRange && hi == "" {
			b = n
		} else if isRange {
			var err2 error
			if b, err2 = strconv.Atoi(hi); err2 != nil {
				err = err2
			}
		}
		if err != nil || a < 1 || b < a || b > n {
			return nil, fmt.Errorf("pages %q: want numbers and ranges from 1 to %d, as in 1-3,5,8-", part, n)
		}
		for i := a; i <= b; i++ {
			out = append(out, i-1)
		}
	}
	return out, nil
}

// writeFlags are the flags of the commands that write a PDF.
type writeFlags struct {
	out       *string
	small     *bool
	linearize *bool
}

func (c *cli) writeFlags() writeFlags {
	return writeFlags{
		out:       c.fs.String("o", "", "output `file`"),
		small:     c.fs.Bool("small", false, "make the file smaller: unused resources left out, duplicates joined, streams compressed, object streams"),
		linearize: c.fs.Bool("linearize", false, "write the file linearised, for fast web view"),
	}
}

func (w writeFlags) options() cera.SaveOptions {
	opt := cera.SaveOptions{Linearize: *w.linearize}
	if *w.small {
		opt.PruneResources, opt.JoinDuplicates, opt.Compress, opt.ObjectStreams = true, true, true, cera.ObjectStreamsAlways
	}
	return opt
}

// save writes what e has to name.
func save(name string, write func(w io.Writer) error) error {
	if name == "" {
		return fmt.Errorf("-o: no output file: %w", errUsage)
	}
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	return f.Close()
}

func cmdText(c *cli, args []string) error {
	pages := c.fs.String("pages", "", "pages, as in 1-3,5,8- (default all)")
	asJSON := c.fs.Bool("json", false, "write JSON: every line of every page with its text and the four corners of its box, in points from the top left of the page")
	if err := c.parse(args, 1); err != nil {
		return err
	}
	doc, err := c.open(c.fs.Arg(0))
	if err != nil {
		return err
	}
	sel, err := parsePages(*pages, doc.NumPages())
	if err != nil {
		return err
	}
	type line struct {
		Text string     `json:"text"`
		Quad [8]float64 `json:"quad"`
	}
	type page struct {
		Page   int     `json:"page"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Lines  []line  `json:"lines"`
	}
	var all []page
	for k, i := range sel {
		p, err := doc.Page(i)
		if err != nil {
			return err
		}
		t, err := p.Text(context.Background())
		p.Release()
		if err != nil {
			return fmt.Errorf("page %d: %w", i+1, err)
		}
		if !*asJSON {
			if k > 0 {
				fmt.Fprint(c.out, "\f")
			}
			fmt.Fprintln(c.out, t.String())
			continue
		}
		w, h := p.Size()
		pg := page{Page: i + 1, Width: w, Height: h, Lines: []line{}}
		for _, l := range t.Lines {
			var s strings.Builder
			for _, ch := range t.Chars[l.Start:l.End] {
				s.WriteString(ch.Text)
			}
			var q [8]float64
			for j, pt := range l.Quad.P {
				q[2*j], q[2*j+1] = pt[0], pt[1]
			}
			pg.Lines = append(pg.Lines, line{s.String(), q})
		}
		all = append(all, pg)
	}
	if *asJSON {
		enc := json.NewEncoder(c.out)
		enc.SetIndent("", " ")
		return enc.Encode(struct {
			Pages []page `json:"pages"`
		}{all})
	}
	return nil
}

func cmdSearch(c *cli, args []string) error {
	pages := c.fs.String("pages", "", "pages, as in 1-3,5,8- (default all)")
	matchCase := c.fs.Bool("match-case", false, "tell upper and lower case apart")
	wholeWord := c.fs.Bool("whole-word", false, "find the text only as a word of its own")
	if err := c.parse(args, 2); err != nil {
		return err
	}
	doc, err := c.open(c.fs.Arg(0))
	if err != nil {
		return err
	}
	sel, err := parsePages(*pages, doc.NumPages())
	if err != nil {
		return err
	}
	found := 0
	for _, i := range sel {
		p, err := doc.Page(i)
		if err != nil {
			return err
		}
		t, err := p.Text(context.Background())
		p.Release()
		if err != nil {
			return fmt.Errorf("page %d: %w", i+1, err)
		}
		for _, m := range t.Find(c.fs.Arg(1), cera.SearchOptions{MatchCase: *matchCase, WholeWord: *wholeWord}) {
			found++
			fmt.Fprintf(c.out, "%d: %s[%s]%s\n", i+1, m.Quote.Prefix, m.Quote.Exact, m.Quote.Suffix)
		}
	}
	if found == 0 {
		return fmt.Errorf("%q is not in the text", c.fs.Arg(1))
	}
	return nil
}

// info is what `cera info` says of a file.
type info struct {
	File       string            `json:"file"`
	Bytes      int64             `json:"bytes"`
	Encrypted  bool              `json:"encrypted"`
	Pages      int               `json:"pages"`
	Sizes      []pageSize        `json:"sizes"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Labels     []string          `json:"labels,omitempty"`
	Outline    int               `json:"outlineEntries"`
	Layers     []string          `json:"layers,omitempty"`
	Fields     []fieldInfo       `json:"fields,omitempty"`
	Signatures []string          `json:"signatures,omitempty"`
	Files      []string          `json:"attachments,omitempty"`
	Fonts      []fontInfo        `json:"fonts,omitempty"`
	// Unsupported lists, by page, what was not drawn exactly.
	Unsupported map[int]map[string]int `json:"unsupported,omitempty"`
}

// fontInfo is a font of the document: what it is, whether the file holds
// its program, what cera draws it with in place of one, and its pages
// (1-based).
type fontInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Subset     bool   `json:"subset,omitempty"`
	Embedded   bool   `json:"embedded"`
	Unreadable bool   `json:"unreadable,omitempty"`
	DrawnWith  string `json:"drawnWith,omitempty"`
	Pages      []int  `json:"pages"`
}

type pageSize struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Pages  int     `json:"pages"`
	First  int     `json:"first"`
}

type fieldInfo struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

func cmdInfo(c *cli, args []string) error {
	asJSON := c.fs.Bool("json", false, "write JSON")
	unsupported := c.fs.Bool("unsupported", true, "draw every page to say what of it cera does not draw exactly")
	if err := c.parse(args, 1); err != nil {
		return err
	}
	name := c.fs.Arg(0)
	doc, err := c.open(name)
	if err != nil {
		return err
	}
	in := info{File: filepath.Base(name), Encrypted: doc.Encrypted(), Pages: doc.NumPages(), Metadata: map[string]string{}}
	if st, err := os.Stat(name); err == nil {
		in.Bytes = st.Size()
	}
	m := doc.Metadata()
	for k, v := range map[string]string{"title": m.Title, "author": m.Author, "subject": m.Subject, "keywords": m.Keywords, "creator": m.Creator, "producer": m.Producer} {
		if v != "" {
			in.Metadata[k] = v
		}
	}
	if !m.Created.IsZero() {
		in.Metadata["created"] = m.Created.Format(time.RFC3339)
	}
	if !m.Modified.IsZero() {
		in.Metadata["modified"] = m.Modified.Format(time.RFC3339)
	}
	var count func(items []cera.OutlineItem) int
	count = func(items []cera.OutlineItem) (n int) {
		for _, it := range items {
			n += 1 + count(it.Children)
		}
		return n
	}
	in.Outline = count(doc.Outline())
	if l := doc.Layers(); l != nil {
		for _, layer := range l.Layers {
			s := layer.Name
			if !layer.Visible {
				s += " (hidden)"
			}
			in.Layers = append(in.Layers, s)
		}
	}
	if f := doc.Form(); f != nil {
		for _, fd := range f.Fields {
			in.Fields = append(in.Fields, fieldInfo{fd.Name, fd.Type.String(), fd.Saved.Text()})
		}
	}
	for _, s := range doc.Signatures() {
		who := s.Signer
		if s.Field != nil {
			who = s.Field.Name + ": " + who
		}
		in.Signatures = append(in.Signatures, who+" ("+s.SubFilter+")")
	}
	for _, a := range doc.Attachments() {
		in.Files = append(in.Files, a.Name)
	}
	for i := range doc.NumPages() {
		p, err := doc.Page(i)
		if err != nil {
			return err
		}
		w, h := p.Size()
		if k := len(in.Sizes) - 1; k >= 0 && in.Sizes[k].Width == w && in.Sizes[k].Height == h {
			in.Sizes[k].Pages++
		} else {
			in.Sizes = append(in.Sizes, pageSize{w, h, 1, i + 1})
		}
		if l := doc.PageLabel(i); l != "" && len(doc.PageLabels()) > 0 {
			in.Labels = append(in.Labels, l)
		}
		if *unsupported {
			// What is not drawn exactly is known once the page is
			// interpreted; the picture is not kept.
			var st cera.Stats
			dst := image.NewRGBA(p.Bounds(0.25))
			_ = p.Render(context.Background(), dst, cera.RenderOptions{Scale: 0.25, Background: color.RGBA{255, 255, 255, 255}, Stats: &st, Deadline: time.Now().Add(time.Minute)})
			if len(st.Unsupported) > 0 {
				if in.Unsupported == nil {
					in.Unsupported = map[int]map[string]int{}
				}
				in.Unsupported[i+1] = st.Unsupported
			}
		}
		p.Release()
	}
	if fonts, err := doc.Fonts(context.Background()); err == nil {
		for _, f := range fonts {
			fi := fontInfo{Name: f.Name(), Type: f.Type(), Subset: f.Subset(), Embedded: f.Embedded(), Unreadable: f.Unreadable(), DrawnWith: f.DrawnWith()}
			for _, p := range f.Pages {
				fi.Pages = append(fi.Pages, p+1)
			}
			in.Fonts = append(in.Fonts, fi)
		}
	}
	if *asJSON {
		enc := json.NewEncoder(c.out)
		enc.SetIndent("", " ")
		return enc.Encode(in)
	}
	fmt.Fprintf(c.out, "file: %s, %d bytes", in.File, in.Bytes)
	if in.Encrypted {
		fmt.Fprint(c.out, ", encrypted")
	}
	fmt.Fprintf(c.out, "\npages: %d\n", in.Pages)
	for _, s := range in.Sizes {
		fmt.Fprintf(c.out, "  %g x %g pt: %d from page %d\n", s.Width, s.Height, s.Pages, s.First)
	}
	for _, k := range []string{"title", "author", "subject", "keywords", "creator", "producer", "created", "modified"} {
		if v := in.Metadata[k]; v != "" {
			fmt.Fprintf(c.out, "%s: %s\n", k, v)
		}
	}
	list := func(title string, items []string) {
		if len(items) > 0 {
			fmt.Fprintf(c.out, "%s: %s\n", title, strings.Join(items, ", "))
		}
	}
	list("page labels", in.Labels)
	if in.Outline > 0 {
		fmt.Fprintf(c.out, "outline: %d entries\n", in.Outline)
	}
	list("layers", in.Layers)
	for _, f := range in.Fields {
		fmt.Fprintf(c.out, "field: %s (%s) = %q\n", f.Name, f.Type, f.Value)
	}
	list("signatures", in.Signatures)
	list("attachments", in.Files)
	for _, f := range in.Fonts {
		name := f.Name
		if name == "" {
			name = "(no name)"
		}
		how := "embedded"
		switch {
		case f.Unreadable:
			how = "embedded, unreadable"
		case !f.Embedded:
			how = "not embedded"
		}
		if f.Subset {
			how += " subset"
		}
		if f.DrawnWith != "" {
			how += ", drawn with " + f.DrawnWith
		}
		pages := make([]string, len(f.Pages))
		for i, p := range f.Pages {
			pages[i] = strconv.Itoa(p)
		}
		fmt.Fprintf(c.out, "font: %s (%s), %s, pages %s\n", name, f.Type, how, strings.Join(pages, " "))
	}
	for _, page := range slices.Sorted(func(yield func(int) bool) {
		for k := range in.Unsupported {
			if !yield(k) {
				return
			}
		}
	}) {
		var keys []string
		for k, n := range in.Unsupported[page] {
			keys = append(keys, fmt.Sprintf("%s x %d", k, n))
		}
		slices.Sort(keys)
		fmt.Fprintf(c.out, "page %d not drawn exactly: %s\n", page, strings.Join(keys, ", "))
	}
	return nil
}

func cmdMerge(c *cli, args []string) error {
	w := c.writeFlags()
	if err := c.parse(args, -1); err != nil {
		return err
	}
	e := cera.NewEditor()
	for _, name := range c.fs.Args() {
		doc, err := c.open(name)
		if err != nil {
			return err
		}
		pages := make([]cera.EditPage, doc.NumPages())
		for i := range pages {
			pages[i] = cera.EditPage{Index: i, Fields: true}
		}
		title := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		opt := cera.ImportOptions{Outline: true, OutlineTitle: title, Destinations: true, Labels: true}
		if err := e.ImportPagesWith(e.NumPages(), doc, opt, pages...); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return save(*w.out, func(f io.Writer) error { return e.SaveWith(f, w.options()) })
}

func cmdSplit(c *cli, args []string) error {
	w := c.writeFlags()
	pages := c.fs.String("pages", "", "pages, as in 1-3,5,8- (default all)")
	every := c.fs.Int("every", 1, "pages in each file")
	if err := c.parse(args, 1); err != nil {
		return err
	}
	doc, err := c.open(c.fs.Arg(0))
	if err != nil {
		return err
	}
	sel, err := parsePages(*pages, doc.NumPages())
	if err != nil {
		return err
	}
	if *every < 1 || !strings.Contains(*w.out, "%d") {
		return fmt.Errorf("-o %q needs %%d for the number of the file, and -every is 1 or more: %w", *w.out, errUsage)
	}
	opt := w.options()
	// A part has what its pages draw and no more.
	opt.PruneResources = true
	for n := 0; n*(*every) < len(sel); n++ {
		part := sel[n*(*every) : min((n+1)*(*every), len(sel))]
		// The document with the other pages taken out keeps what is
		// around its pages: the outline of those left, the form, the
		// labels.
		e := doc.Edit()
		var gone []int
		for i := range doc.NumPages() {
			if !slices.Contains(part, i) {
				gone = append(gone, i)
			}
		}
		if err := e.DeletePages(gone...); err != nil {
			return err
		}
		e.PruneOutline()
		if err := save(fmt.Sprintf(*w.out, n+1), func(f io.Writer) error { return e.SaveWith(f, opt) }); err != nil {
			return err
		}
	}
	return nil
}

func cmdRotate(c *cli, args []string) error {
	w := c.writeFlags()
	pages := c.fs.String("pages", "", "pages, as in 1-3,5,8- (default all)")
	by := c.fs.Int("by", 90, "degrees clockwise, a multiple of 90")
	if err := c.parse(args, 1); err != nil {
		return err
	}
	doc, err := c.open(c.fs.Arg(0))
	if err != nil {
		return err
	}
	sel, err := parsePages(*pages, doc.NumPages())
	if err != nil {
		return err
	}
	e := doc.Edit()
	for _, i := range sel {
		if err := e.RotatePage(i, *by); err != nil {
			return err
		}
	}
	return save(*w.out, func(f io.Writer) error { return e.SaveWith(f, w.options()) })
}

func cmdDelete(c *cli, args []string) error {
	w := c.writeFlags()
	pages := c.fs.String("pages", "", "pages to take out, as in 2,5-7")
	if err := c.parse(args, 1); err != nil {
		return err
	}
	if *pages == "" {
		return fmt.Errorf("-pages: which pages: %w", errUsage)
	}
	doc, err := c.open(c.fs.Arg(0))
	if err != nil {
		return err
	}
	sel, err := parsePages(*pages, doc.NumPages())
	if err != nil {
		return err
	}
	e := doc.Edit()
	if err := e.DeletePages(sel...); err != nil {
		return err
	}
	e.PruneOutline()
	return save(*w.out, func(f io.Writer) error { return e.SaveWith(f, w.options()) })
}

func cmdFill(c *cli, args []string) error {
	w := c.writeFlags()
	flatten := c.fs.Bool("flatten", false, "make the form part of the pages: no fields are left")
	update := c.fs.Bool("update", false, "append the change to the file as it is, which keeps its signatures valid, in place of writing it anew")
	var fields []string
	c.fs.Func("field", "set a form field, `name=value` (repeatable); a check box or radio button takes the name of its state", func(s string) error {
		if !strings.Contains(s, "=") {
			return fmt.Errorf("%q: want name=value", s)
		}
		fields = append(fields, s)
		return nil
	})
	if err := c.parse(args, 1); err != nil {
		return err
	}
	doc, err := c.open(c.fs.Arg(0))
	if err != nil {
		return err
	}
	state, err := fill(doc, fields)
	if err != nil {
		return err
	}
	e := doc.Edit()
	if state != nil {
		if err := e.SetFields(state); err != nil {
			return err
		}
	}
	if *flatten {
		if err := e.Flatten(); err != nil {
			return err
		}
	}
	return save(*w.out, func(f io.Writer) error {
		if *update {
			return e.Update(f)
		}
		return e.SaveWith(f, w.options())
	})
}
