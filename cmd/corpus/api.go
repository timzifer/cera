package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"math/big"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/timzifer/cera"
	"github.com/timzifer/cera/internal/cms"
	"github.com/timzifer/cera/internal/corpus"
)

// run -api runs what a viewer and an editor ask of a document, beyond
// drawing it, over the files: the text with round trips of search and
// selection, the links and where they lead, and the editor — a file saved
// anew, an update with a highlight, the form and the annotations
// flattened, a field added, a signature, a redaction — each opened again
// and its first page compared with what it was, or for a redaction read
// for what should be gone. A file saved smaller has every page looked at
// compared; a linearised one says of itself how long it is; a page at
// half its size is drawn twice as large and compared; the outline and
// the page labels, set as they read, read the same; the first page drawn
// in grey is the grey of the page drawn in colour.
//
// The gate is that of rendering: no panic, no hang, no deadline.
// Everything else it finds is a difference, reported with its file.

// apiResult is what the API run found in one file.
type apiResult struct {
	file   string
	err    error // the file did not open
	pages  int   // pages looked at
	chars  int
	words  int // words searched for and found again
	links  int
	leads  int // links that resolve to a view of a page
	saved  int // files written and opened again: Save, Update, Flatten
	hang   bool
	failed []string // panics and deadlines
	// refused is what the document does not allow, differences what is
	// not as it should be.
	refused, differences []string
	// plain and small are the bytes of the file saved as it is and with
	// everything that makes it smaller, where both were written.
	plain, small int64
}

func (r *apiResult) fatal() bool { return r.hang || len(r.failed) > 0 }

// step runs f; a panic in it fails the file.
func (r *apiResult) step(name string, f func()) {
	defer func() {
		if v := recover(); v != nil {
			r.failed = append(r.failed, fmt.Sprintf("%s: panic: %v", name, v))
		}
	}()
	f()
}

// check takes the error of a step: a panic cera recovered or a deadline
// fails the file, a refusal is noted, anything else is a difference. It
// reports whether the step went through.
func (r *apiResult) check(name string, err error) bool {
	var pe *cera.PanicError
	switch {
	case err == nil:
		return true
	case errors.As(err, &pe), errors.Is(err, cera.ErrDeadline), errors.Is(err, context.DeadlineExceeded):
		r.failed = append(r.failed, fmt.Sprintf("%s: %v", name, err))
	case errors.Is(err, cera.ErrSigned), errors.Is(err, cera.ErrCertified), errors.Is(err, cera.ErrNoAssembly),
		errors.Is(err, cera.ErrNotPermitted), errors.Is(err, cera.ErrNoUpdate), errors.Is(err, cera.ErrPassword),
		errors.Is(err, cera.ErrLocked), errors.Is(err, cera.ErrRedaction):
		r.refused = append(r.refused, fmt.Sprintf("%s: %v", name, err))
	default:
		r.differ("%s: %v", name, err)
	}
	return false
}

func (r *apiResult) differ(format string, args ...any) {
	if len(r.differences) < maxDifferences {
		r.differences = append(r.differences, fmt.Sprintf(format, args...))
	}
}

// outlineText writes an outline for comparing: an entry a line, with
// where it leads.
func outlineText(items []cera.OutlineItem, depth int) string {
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%d %q %v %v %v", depth, it.Title, it.Open, it.Bold, it.Italic)
		if t := it.Target; t != nil {
			fmt.Fprintf(&b, " %q %d %s %q %q %q %v", t.URI, t.Page, t.Fit, t.Named, t.Action, t.NamedAction, t.External)
		}
		b.WriteString("\n" + outlineText(it.Children, depth+1))
	}
	return b.String()
}

// linearised matches the first object of a linearised file.
var linearised = regexp.MustCompile(`^%PDF-[^\n]*\n%[^\n]*\n\d+ 0 obj\n<</Linearized 1 /L (\d+) `)

// Limits of the API run: differences listed per file, places of a page
// whose word is searched for, links of a page resolved.
const (
	maxDifferences = 12
	apiSamples     = 8
	apiLinks       = 64
)

// apiFile runs the API over one document, under a watchdog as file does.
func (c runConfig) apiFile(path, rel string) (apiResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return apiResult{}, err
	}
	done := make(chan apiResult, 1)
	go func() {
		pw, _ := corpus.Password(path)
		done <- c.api(data, pw, rel)
	}()
	budget := c.timeout * time.Duration(max(c.pages, 4)+6)
	select {
	case r := <-done:
		return r, nil
	case <-time.After(budget):
		return apiResult{file: rel, hang: true, failed: []string{fmt.Sprintf("hang: no result after %v", budget)}}, nil
	}
}

func (c runConfig) api(data []byte, password, rel string) apiResult {
	r := apiResult{file: rel}
	opt := cera.OpenOptions{Password: password}
	doc, err := cera.OpenWith(data, opt)
	if err != nil {
		r.err = err
		return r
	}
	n := doc.NumPages()
	if c.pages > 0 {
		n = min(n, c.pages)
	}
	r.pages = n
	r.step("view settings", func() {
		if o := doc.ViewSettings().Open; o != nil && o.Page >= doc.NumPages() {
			r.differ("the document opens at page %d of %d", o.Page+1, doc.NumPages())
		}
	})

	r.step("structure", func() {
		tree := doc.Structure()
		if tree == nil {
			return
		}
		var walk func(e *cera.StructElem)
		walk = func(e *cera.StructElem) {
			for _, k := range e.Kids {
				switch {
				case k.Page >= doc.NumPages(), k.Elem == nil && k.Page < 0, k.Elem == nil && (k.MCID < 0) == (k.Annot < 0):
					r.differ("the structure element %s has the kid %+v", e.Type, k)
				case k.Elem != nil:
					walk(k.Elem)
				}
			}
		}
		for _, e := range tree.Elems {
			walk(e)
		}
	})

	// The text and the links of the pages.
	var first *cera.PageText
	var firstWord cera.TextRange // a word of the first page, to highlight
	index := cera.NewTextIndex(doc, cera.TextOptions{}, 4)
	for i := range n {
		name := fmt.Sprintf("page %d", i+1)
		r.step(name+" text", func() {
			p, err := doc.Page(i)
			if !r.check(name, err) {
				return
			}
			boxes := p.Boxes()
			for b := cera.BoxCrop; b <= cera.BoxArt; b++ {
				in, m := boxes.Box(b), boxes.Media
				if !(in.Dx() > 0 && in.Dy() > 0) || in.X0 < m.X0 || in.Y0 < m.Y0 || in.X1 > m.X1 || in.Y1 > m.Y1 {
					r.differ("%s: its box %d is %v on a medium of %v", name, b, in, m)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
			defer cancel()
			pt, err := p.Text(ctx)
			if !r.check(name+" text", err) {
				return
			}
			r.chars += len(pt.Chars)
			word := c.apiText(&r, name, pt)
			if i == 0 {
				first, firstWord = pt, word
			}
			r.step(name+" links", func() { c.apiLinks(&r, name, doc, p, pt, index, i) })
			p.Release()
		})
	}
	if n == 0 {
		return r
	}

	// The editor. Each file it writes is opened again; the first page is
	// compared with what the document shows.
	var before *image.RGBA
	r.step("render", func() {
		img, err := c.apiRender(doc, 0)
		if r.check("render", err) {
			before = img
		}
	})
	reopen := func(name string, out []byte, compare bool) *cera.Document {
		od, err := cera.OpenWith(out, opt)
		if err != nil {
			r.differ("%s: the file does not open again: %v", name, err)
			return nil
		}
		r.saved++
		if od.NumPages() != doc.NumPages() {
			r.differ("%s: %d pages, the document has %d", name, od.NumPages(), doc.NumPages())
			return od
		}
		if img, err := c.apiRender(od, 0); r.check(name+": render", err) && compare && before != nil && img != nil {
			if share := pixelShare(before, img); share > apiPixelShare {
				r.differ("%s: page 1 differs in %.2f %% of its pixels", name, 100*share)
			}
		}
		return od
	}
	plain := 0
	r.step("save", func() {
		var out bytes.Buffer
		if r.check("save", doc.Edit().Save(&out)) {
			reopen("save", out.Bytes(), true)
			plain = out.Len()
		}
		// And in the other form: packed into object streams, or not.
		other := cera.SaveOptions{ObjectStreams: cera.ObjectStreamsAlways}
		if bytes.Contains(out.Bytes(), []byte("/ObjStm")) {
			other.ObjectStreams = cera.ObjectStreamsNever
		}
		out.Reset()
		if r.check("save, the other form", doc.Edit().SaveWith(&out, other)) {
			reopen("save, the other form", out.Bytes(), true)
		}
	})
	r.step("grey", func() {
		// The first page in grey is the page converted.
		if before == nil {
			return
		}
		p, err := doc.Page(0)
		if err != nil {
			return
		}
		defer p.Release()
		grey := image.NewGray(before.Rect)
		err = p.RenderGray(context.Background(), grey, cera.RenderOptions{
			Scale: 1, Background: color.RGBA{255, 255, 255, 255}, Deadline: time.Now().Add(c.timeout), Workers: c.workers,
		})
		if !r.check("grey", err) {
			return
		}
		// A large page is drawn in several bands; a band of a page has
		// the pixels of the page drawn at once.
		differ := 0
		for y := before.Rect.Min.Y; y < before.Rect.Max.Y; y++ {
			for x := before.Rect.Min.X; x < before.Rect.Max.X; x++ {
				d := int(color.GrayModel.Convert(before.RGBAAt(x, y)).(color.Gray).Y) - int(grey.GrayAt(x, y).Y)
				if d != 0 {
					differ++
				}
			}
		}
		if differ > 0 {
			r.differ("grey: %d pixels of page 1 are not the grey of their colour", differ)
		}
	})
	r.step("outline", func() {
		// The outline and the page labels set as they read: the result
		// reads the same. Then without its last page, pruned.
		want := outlineText(doc.Outline(), 0)
		ranges := doc.PageLabels()
		if want == "" && ranges == nil {
			return
		}
		e := doc.Edit()
		if !r.check("outline", e.SetOutline(e.Outline())) || ranges != nil && !r.check("page labels", e.SetPageLabels(ranges)) {
			return
		}
		var out bytes.Buffer
		if !r.check("outline: save", e.Save(&out)) {
			return
		}
		od := reopen("outline", out.Bytes(), true)
		if od == nil {
			return
		}
		if got := outlineText(od.Outline(), 0); got != want {
			r.differ("outline: set as it reads, it reads differently: %d bytes for %d", len(got), len(want))
		}
		for i := range n {
			if a, b := doc.PageLabel(i), od.PageLabel(i); a != b && ranges != nil {
				r.differ("page labels: page %d is labelled %q, was %q", i+1, b, a)
				break
			}
		}
		if doc.NumPages() < 2 || want == "" {
			return
		}
		e = doc.Edit()
		e.PruneOutline()
		if !r.check("outline: delete", e.DeletePages(doc.NumPages()-1)) {
			return
		}
		out.Reset()
		if !r.check("outline: pruned: save", e.Save(&out)) {
			return
		}
		pd, err := cera.OpenWith(out.Bytes(), opt)
		if err != nil {
			r.differ("outline: pruned: the file does not open again: %v", err)
			return
		}
		r.saved++
		// No entry leads to the page that is gone, and none is new.
		var count func(items []cera.OutlineItem) (entries, gone int)
		count = func(items []cera.OutlineItem) (entries, gone int) {
			for _, it := range items {
				if t := it.Target; t != nil && t.URI == "" && !t.External && t.Page >= pd.NumPages() {
					gone++
				}
				e, g := count(it.Children)
				entries, gone = entries+1+e, gone+g
			}
			return entries, gone
		}
		had, _ := count(doc.Outline())
		if entries, gone := count(pd.Outline()); entries > had || gone > 0 {
			r.differ("outline: pruned, it has %d entries of %d, %d to the page that is gone", entries, had, gone)
		}
	})
	r.step("resize", func() {
		// The first page at half its size: drawn twice as large it is
		// what it was. Annotations keep the width of their borders and
		// the size of their text, so a page with any is only written.
		p, err := doc.Page(0)
		if err != nil {
			return
		}
		w, h := p.Size()
		bare := len(p.Annotations()) == 0
		e := doc.Edit()
		if w < 4 || h < 4 || !r.check("resize", e.ResizePage(0, w/2, h/2, cera.FitStretch)) {
			return
		}
		var out bytes.Buffer
		if !r.check("resize: save", e.Save(&out)) {
			return
		}
		od := reopen("resize", out.Bytes(), false)
		if od == nil || !bare || before == nil {
			return
		}
		img, err := c.apiRenderAt(od, 0, 2)
		if !r.check("resize: render", err) || img == nil || img.Rect != before.Rect {
			return
		}
		if share := pixelShare(before, img); share > apiPixelShare {
			r.differ("resize: page 1 at half its size differs in %.2f %% of its pixels", 100*share)
		}
	})
	r.step("sheets", func() {
		// The pages looked at, two by two on sheets after the others.
		e := doc.Edit()
		var pages []int
		for i := range n {
			pages = append(pages, i)
		}
		sheet := cera.Sheet{Width: 842, Height: 595, Columns: 2, Rows: 2, Margin: 20, Gap: 10}
		if !r.check("sheets", e.AddSheets(e.NumPages(), doc, sheet, pages...)) {
			return
		}
		var out bytes.Buffer
		if !r.check("sheets: save", e.Save(&out)) {
			return
		}
		od, err := cera.OpenWith(out.Bytes(), opt)
		if err != nil {
			r.differ("sheets: the file does not open again: %v", err)
			return
		}
		r.saved++
		if want := doc.NumPages() + (n+3)/4; od.NumPages() != want {
			r.differ("sheets: %d pages, want %d", od.NumPages(), want)
			return
		}
		_, err = c.apiRender(od, doc.NumPages())
		r.check("sheets: render", err)
	})
	r.step("import structure", func() {
		// The pages looked at, imported with their structure into the
		// document itself, opened a second time: the tree gains a part
		// with as much content on the copies as the pages have.
		if doc.Structure() == nil {
			return
		}
		again, err := cera.OpenWith(data, opt)
		if err != nil {
			return
		}
		e := doc.Edit()
		var pages []cera.EditPage
		for i := range n {
			pages = append(pages, cera.EditPage{Index: i})
		}
		if !r.check("import structure", e.ImportPagesWith(e.NumPages(), again, cera.ImportOptions{Structure: true}, pages...)) {
			return
		}
		var out bytes.Buffer
		if !r.check("import structure: save", e.Save(&out)) {
			return
		}
		od, err := cera.OpenWith(out.Bytes(), opt)
		if err != nil {
			r.differ("import structure: the file does not open again: %v", err)
			return
		}
		r.saved++
		if od.Structure() == nil {
			r.differ("import structure: the result is untagged")
			return
		}
		// Marked content by page, of a tree; truncated trees are not
		// compared.
		count := func(t *cera.StructTree) map[int]int {
			got := map[int]int{}
			var walk func(e *cera.StructElem)
			walk = func(e *cera.StructElem) {
				for _, k := range e.Kids {
					if k.Elem != nil {
						walk(k.Elem)
					} else if k.MCID >= 0 {
						got[k.Page]++
					}
				}
			}
			for _, e := range t.Elems {
				walk(e)
			}
			return got
		}
		if doc.Structure().Truncated || od.Structure().Truncated {
			return
		}
		was, is := count(doc.Structure()), count(od.Structure())
		for i := range n {
			if was[i] != is[doc.NumPages()+i] || was[i] != is[i] {
				r.differ("import structure: page %d has %d marked contents in the tree, its copy %d, the page itself %d", i+1, was[i], is[doc.NumPages()+i], is[i])
				return
			}
		}
	})
	r.step("linearise", func() {
		// Laid out for a reader that gets the file piece by piece: the
		// first object says so and how long the file is, and the document
		// is what it was.
		for _, mode := range []cera.ObjectStreamMode{cera.ObjectStreamsNever, cera.ObjectStreamsAlways} {
			var out bytes.Buffer
			name := "linearise"
			if mode == cera.ObjectStreamsAlways {
				name = "linearise, packed"
			}
			if !r.check(name, doc.Edit().SaveWith(&out, cera.SaveOptions{Linearize: true, ObjectStreams: mode})) {
				return
			}
			if m := linearised.FindSubmatch(out.Bytes()[:min(out.Len(), 1024)]); m == nil {
				r.differ("%s: the file does not start with a linearisation dictionary", name)
			} else if string(m[1]) != fmt.Sprint(out.Len()) {
				r.differ("%s: the file says it has %s bytes and has %d", name, m[1], out.Len())
			}
			reopen(name, out.Bytes(), true)
		}
	})
	r.step("images", func() {
		// The images made smaller, at a loss: the pages looked at are as
		// bright as they were, the file opens and is no larger.
		var out bytes.Buffer
		opt := cera.SaveOptions{Images: &cera.ImageOptions{MaxPPI: 150, TargetPPI: 110, JPEGQuality: 75, Bilevel: true}}
		if !r.check("images", doc.Edit().SaveWith(&out, opt)) {
			return
		}
		od := reopen("images", out.Bytes(), false)
		if od == nil || od.NumPages() != doc.NumPages() {
			return
		}
		var plain bytes.Buffer
		// Strings of an encrypted file get a random vector each time: a
		// few bytes either way.
		if doc.Edit().Save(&plain) == nil && out.Len() > plain.Len()+64 {
			r.differ("images: the file is %d bytes, saved as it is %d", out.Len(), plain.Len())
		}
		for i := range n {
			a, err := c.apiRender(doc, i)
			if err != nil || a == nil {
				continue
			}
			b, err := c.apiRender(od, i)
			if !r.check(fmt.Sprintf("images: render page %d", i+1), err) || b == nil || a.Rect != b.Rect {
				continue
			}
			var sa, sb float64
			for k := 0; k < len(a.Pix); k += 4 {
				sa += float64(a.Pix[k]) + float64(a.Pix[k+1]) + float64(a.Pix[k+2])
				sb += float64(b.Pix[k]) + float64(b.Pix[k+1]) + float64(b.Pix[k+2])
			}
			if px := float64(len(a.Pix) / 4 * 3); math.Abs(sa-sb)/px > 2 {
				r.differ("images: page %d is %.1f levels brighter on average", i+1, (sb-sa)/px)
			}
		}
	})
	r.step("optimise", func() {
		// Everything that makes the file smaller and loses nothing: the
		// pages are what they were.
		var out bytes.Buffer
		small := cera.SaveOptions{
			ObjectStreams: cera.ObjectStreamsAlways, PruneResources: true, JoinDuplicates: true, Compress: true,
			Drop: cera.DropThumbnails | cera.DropPieceInfo | cera.DropObjectMetadata,
		}
		if !r.check("optimise", doc.Edit().SaveWith(&out, small)) {
			return
		}
		od := reopen("optimise", out.Bytes(), true)
		if od == nil || od.NumPages() != doc.NumPages() {
			return
		}
		if plain > 0 {
			r.plain, r.small = int64(plain), int64(out.Len())
		}
		for i := 1; i < n; i++ {
			a, err := c.apiRender(doc, i)
			if err != nil || a == nil {
				continue
			}
			b, err := c.apiRender(od, i)
			if !r.check(fmt.Sprintf("optimise: render page %d", i+1), err) || b == nil {
				continue
			}
			if share := pixelShare(a, b); share > apiPixelShare {
				r.differ("optimise: page %d differs in %.2f %% of its pixels", i+1, 100*share)
			}
		}
	})
	r.step("update", func() {
		// With a highlight on a word of the first page, so that there is
		// something to append.
		e := doc.Edit()
		marked := false
		if first != nil && !firstWord.Empty() {
			marked = r.check("highlight", e.AddHighlight(0, cera.Highlight{Quads: first.Quads(firstWord)}))
		}
		p, err := doc.Page(0)
		if err != nil {
			return
		}
		had := len(p.Annotations())
		var out bytes.Buffer
		if !r.check("update", e.Update(&out)) {
			return
		}
		if !bytes.HasPrefix(out.Bytes(), data) {
			r.differ("update: the file does not start with the document's")
		}
		od := reopen("update", out.Bytes(), !marked)
		if od == nil || !marked {
			return
		}
		if op, err := od.Page(0); err == nil && len(op.Annotations()) <= had {
			r.differ("update: %d annotations on page 1 after a highlight was added to %d", len(op.Annotations()), had)
		}
	})
	r.step("redact", func() {
		// The first word of the first page and a box across its middle
		// are taken out: no character of the page that is written then
		// has its middle in them.
		p, err := doc.Page(0)
		if err != nil {
			return
		}
		w, h := p.Size()
		areas := []cera.Quad{cera.QuadOf(cera.Rect{X0: 0.3 * w, Y0: 0.3 * h, X1: 0.7 * w, Y1: 0.5 * h})}
		if first != nil && !firstWord.Empty() {
			areas = append(areas, first.Quads(firstWord)...)
		}
		e := doc.Edit()
		if !r.check("redact", e.Redact(0, areas, color.Black)) {
			return
		}
		var out bytes.Buffer
		if !r.check("redact: save", e.Save(&out)) {
			return
		}
		od := reopen("redact", out.Bytes(), false)
		if od == nil {
			return
		}
		op, err := od.Page(0)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		pt, err := op.Text(ctx)
		if !r.check("redact: text", err) {
			return
		}
		left := 0
		for k := range pt.Chars {
			q := pt.Chars[k].Quad
			x := (q.P[0][0] + q.P[1][0] + q.P[2][0] + q.P[3][0]) / 4
			y := (q.P[0][1] + q.P[1][1] + q.P[2][1] + q.P[3][1]) / 4
			for _, a := range areas {
				if a.Contains(x, y) {
					left++
					break
				}
			}
		}
		if left > 0 {
			r.differ("redact: %d characters are left in the areas", left)
		}
	})
	r.step("actions", func() {
		// What the buttons of the form do: carried out or put together,
		// which changes nothing of the document.
		form := doc.Form()
		if form == nil {
			return
		}
		state := form.NewState()
		for _, f := range form.Fields {
			for _, w := range f.Widgets {
				for _, a := range w.Actions {
					switch {
					case state.Do(a):
					case a.Kind == "SubmitForm":
						if _, _, err := state.Submission(a); err != nil && !errors.Is(err, cera.ErrSubmitDocument) {
							r.differ("actions: what %s submits: %v", f.Name, err)
						}
					case a.Kind == "":
						r.differ("actions: %s has an action of no kind", f.Name)
					}
				}
			}
		}
	})
	r.step("field", func() {
		const name = "cera.corpus.field"
		e := doc.Edit()
		if !r.check("field", e.AddField(0, cera.Rect{X0: 10, Y0: 10, X1: 110, Y1: 30}, cera.NewField{Name: name, Value: cera.TextValue("cera")})) {
			return
		}
		var out bytes.Buffer
		if !r.check("field: update", e.Update(&out)) {
			return
		}
		od := reopen("field", out.Bytes(), false)
		if od == nil {
			return
		}
		had := 0
		if form := doc.Form(); form != nil {
			had = len(form.Fields)
		}
		form := od.Form()
		if f := form.Field(name); f == nil || len(f.Widgets) != 1 || f.Saved.Text() != "cera" || len(form.Fields) != had+1 {
			r.differ("field: the form has %d fields after one was added to %d, the new one is %+v", len(form.Fields), had, f)
			return
		}
		// And taken out again: the form as it was.
		e = od.Edit()
		if !r.check("field: remove", e.RemoveField(name)) {
			return
		}
		out.Reset()
		if !r.check("field: update", e.Update(&out)) {
			return
		}
		if od := reopen("field removed", out.Bytes(), true); od != nil {
			n := 0
			if form := od.Form(); form != nil {
				n = len(form.Fields)
			}
			if n != had {
				r.differ("field: the form has %d fields after the added one was removed, it had %d", n, had)
			}
		}
	})
	r.step("sign", func() {
		signer, err := apiSigner()
		if err != nil {
			return
		}
		// With a timestamp of a test authority, the signer's own (B-T).
		stamp := func(_ context.Context, digest []byte) ([]byte, error) {
			info, err := cms.TimestampInfo(digest, time.Now())
			if err != nil {
				return nil, err
			}
			return cms.SignContent(info, true, signer.cert, signer.key, time.Now())
		}
		var out bytes.Buffer
		if !r.check("sign", doc.Edit().Sign(&out, cera.SignOptions{Key: signer.key, Certificate: signer.cert, Timestamp: stamp})) {
			return
		}
		if !bytes.HasPrefix(out.Bytes(), data) {
			r.differ("sign: the file does not start with the document's")
		}
		// An invisible signature: the page is as it was.
		od := reopen("sign", out.Bytes(), true)
		if od == nil {
			return
		}
		sigs := od.Signatures()
		if len(sigs) == 0 {
			r.differ("sign: the signed file has no signature")
			return
		}
		last := &sigs[len(sigs)-1]
		st, err := last.Verify(cera.VerifyOptions{})
		if err != nil || !st.Intact || !last.CoversWholeFile || st.Changes.Any() || !st.Timestamped {
			r.differ("sign: the signature does not hold: %+v, %v", st, err)
			return
		}
		// Its validation data, and a document timestamp over it (B-LTA):
		// the signature still holds, and sees no change.
		e := od.Edit()
		if !r.check("sign: validation", e.AddValidation(last, cera.Validation{Certificates: st.Certificates})) {
			return
		}
		out.Reset()
		if !r.check("sign: document timestamp", e.Timestamp(context.Background(), &out, stamp)) {
			return
		}
		ld := reopen("sign: document timestamp", out.Bytes(), true)
		if ld == nil {
			return
		}
		sigs = ld.Signatures()
		if len(sigs) < 2 {
			r.differ("sign: the stamped file has %d signatures", len(sigs))
			return
		}
		first, stamped := &sigs[len(sigs)-2], &sigs[len(sigs)-1]
		if st, err := first.Verify(cera.VerifyOptions{}); err != nil || !st.Intact || st.Changes.Any() || !st.ChangesAllowed {
			r.differ("sign: the signature after the document timestamp: %+v, %v", st, err)
		}
		if st, err := stamped.Verify(cera.VerifyOptions{}); err != nil || !st.Intact || !stamped.CoversWholeFile || !st.Timestamped {
			r.differ("sign: the document timestamp does not hold: %+v, %v", st, err)
		}
	})
	r.step("flatten", func() {
		e := doc.Edit()
		if doc.Form() != nil && !r.check("flatten", e.Flatten()) {
			return
		}
		if !r.check("flatten annotations", e.FlattenAnnotations()) {
			return
		}
		var out bytes.Buffer
		if r.check("flatten: save", e.Save(&out)) {
			reopen("flatten", out.Bytes(), true)
		}
	})
	return r
}

// signer is a key with its certificate.
type signer struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
}

// apiSigner returns a key and a certificate of its own making, to sign the
// files with.
var apiSigner = sync.OnceValues(func() (signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return signer{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cera corpus run"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return signer{}, err
	}
	cert, err := x509.ParseCertificate(der)
	return signer{key, cert}, err
})

// apiText checks the text of a page against itself: offsets into String
// ascend with the positions, a selection holds what it was asked for and
// grows with its unit, and a word of the page is found where it stands and
// found again by its quote. It returns the first such word.
func (c runConfig) apiText(r *apiResult, name string, pt *cera.PageText) (first cera.TextRange) {
	s := pt.String()
	n := len(pt.Chars)
	prev := 0
	for k := 0; k <= n; k++ {
		if o := pt.Offset(cera.TextPos(k)); o < prev || o > len(s) {
			r.differ("%s: offset %d of position %d after offset %d, in a text of %d bytes", name, o, k, prev, len(s))
			break
		} else {
			prev = o
		}
	}
	if all := pt.TextOf(pt.All(), cera.TextFormat{}); all != s {
		r.differ("%s: the text of all characters (%d bytes) is not String (%d bytes)", name, len(all), len(s))
	}
	if n == 0 {
		return first
	}
	for k := range apiSamples {
		at := cera.TextPos(k * n / apiSamples)
		word := pt.WordAt(at)

		// A selection from at to the end of its word, by every unit.
		inner := cera.TextRange{Start: at, End: max(word.End, at)}
		for g := cera.SelectChars; g <= cera.SelectBlocks; g++ {
			sel := pt.Select(inner.Start, inner.End, g)
			if sel.Start < 0 || int(sel.End) > n || sel.Start > inner.Start || sel.End < inner.End {
				r.differ("%s: selecting %v by unit %d gives %v of %d characters", name, inner, g, sel, n)
				break
			}
			inner = sel
		}
		for _, q := range pt.Quads(word) {
			for _, p := range q.P {
				if math.IsNaN(p[0]+p[1]) || math.IsInf(p[0]+p[1], 0) {
					r.differ("%s: a quad of %v is not finite", name, word)
				}
			}
		}

		// The word, searched for: letters and digits only, so that what
		// search folds — spaces, hyphens at line ends — is not in it.
		text := pt.TextOf(word, cera.TextFormat{})
		if utf8.RuneCountInString(text) < 3 || strings.ContainsFunc(text, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) }) {
			continue
		}
		opt := cera.SearchOptions{MatchCase: true}
		matches := pt.Find(text, opt)
		var found *cera.TextMatch
		for i := range matches {
			m := &matches[i]
			if i > 0 && m.Range.Start < matches[i-1].Range.End {
				r.differ("%s: matches of %q overlap or go back: %v after %v", name, text, m.Range, matches[i-1].Range)
				break
			}
			if m.Range.Start < word.End && m.Range.End > word.Start {
				found = m
			}
		}
		if found == nil {
			r.differ("%s: %q is not found where it stands, at %v (%d matches)", name, text, word, len(matches))
			continue
		}
		r.words++
		if first.Empty() {
			first = found.Range
		}
		// Its quote finds it again, or an earlier place that reads the
		// same as far as the quote goes: where the text repeats.
		if again, ok := pt.FindQuote(found.Quote, opt); !ok {
			r.differ("%s: the quote of %q at %v finds nothing", name, text, found.Range)
		} else if again.Range != found.Range && (again.Range.Start >= found.Range.Start ||
			!strings.HasSuffix(again.Quote.Prefix, found.Quote.Prefix) || !strings.HasPrefix(again.Quote.Suffix, found.Quote.Suffix)) {
			r.differ("%s: the quote of %q at %v finds %v, which reads otherwise", name, text, found.Range, again.Range)
		}
	}
	return first
}

// apiLinks resolves the links of a page, and asks what lies at the middle
// of each and at the first character.
func (c runConfig) apiLinks(r *apiResult, name string, doc *cera.Document, p *cera.Page, pt *cera.PageText, index *cera.TextIndex, i int) {
	links := append(p.Links(), pt.URLs()...)
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	for k := range links[:min(len(links), apiLinks)] {
		l := &links[k]
		r.links++
		t := &l.Target
		if !t.External && t.Page >= doc.NumPages() {
			r.differ("%s: link %d leads to page %d of %d", name, l.Index, t.Page+1, doc.NumPages())
		}
		if d, ok := doc.Destination(t); ok {
			r.leads++
			if d.Page != t.Page {
				r.differ("%s: link %d to page %d resolves to page %d", name, l.Index, t.Page+1, d.Page+1)
			}
		}
		if len(l.Quads) == 0 {
			continue
		}
		q := l.Quads[0]
		x, y := (q.P[0][0]+q.P[1][0]+q.P[2][0]+q.P[3][0])/4, (q.P[0][1]+q.P[1][1]+q.P[2][1]+q.P[3][1])/4
		if math.IsNaN(x+y) || math.IsInf(x+y, 0) {
			r.differ("%s: a quad of link %d is not finite", name, l.Index)
			continue
		}
		if !l.Contains(x, y) {
			continue // a quad without area
		}
		h, err := index.Hit(ctx, i, x, y)
		if r.check(name+" hit", err) && h.Kind != cera.HitLink {
			r.differ("%s: at the middle of link %d lies no link (kind %d)", name, l.Index, h.Kind)
		}
	}
	if len(pt.Chars) > 0 {
		o := pt.Chars[0].Origin
		_, err := index.Hit(ctx, i, o[0], o[1])
		r.check(name+" hit", err)
	}
}

// apiRender draws page i at 72 dpi on white, nil for a page too large.
func (c runConfig) apiRender(doc *cera.Document, i int) (*image.RGBA, error) {
	return c.apiRenderAt(doc, i, 1)
}

// apiRenderAt is apiRender at a scale.
func (c runConfig) apiRenderAt(doc *cera.Document, i int, scale float64) (*image.RGBA, error) {
	p, err := doc.Page(i)
	if err != nil {
		return nil, err
	}
	defer p.Release()
	b := p.Bounds(scale)
	if b.Dx()*b.Dy() > maxPixels/4 {
		return nil, nil
	}
	dst := image.NewRGBA(b)
	err = p.Render(context.Background(), dst, cera.RenderOptions{
		Scale: scale, Background: color.RGBA{255, 255, 255, 255}, Deadline: time.Now().Add(c.timeout), Workers: c.workers,
	})
	return dst, err
}

// A page differs from what it was where a channel of a pixel is further
// than apiPixelLevels from it; a file is reported when more than
// apiPixelShare of the pixels of its first page do.
const (
	apiPixelLevels = 16
	apiPixelShare  = 0.001
)

// pixelShare returns the share of the pixels of a that differ in b; all of
// them for images of different sizes.
func pixelShare(a, b *image.RGBA) float64 {
	if a.Rect != b.Rect || len(a.Pix) == 0 {
		if a.Rect == b.Rect {
			return 0
		}
		return 1
	}
	n := 0
	for i := 0; i < len(a.Pix); i += 4 {
		for k := range 3 {
			if d := int(a.Pix[i+k]) - int(b.Pix[i+k]); d > apiPixelLevels || d < -apiPixelLevels {
				n++
				break
			}
		}
	}
	return float64(n) / float64(len(a.Pix)/4)
}

// writeAPIReport writes what the API run found.
func writeAPIReport(w io.Writer, rs []apiResult, pages int) {
	var opened, failed, differing, refused int
	var sum apiResult
	for i := range rs {
		r := &rs[i]
		if r.err == nil {
			opened++
		}
		if r.fatal() {
			failed++
		}
		if len(r.differences) > 0 {
			differing++
		}
		if len(r.refused) > 0 {
			refused++
		}
		sum.pages += r.pages
		sum.chars += r.chars
		sum.words += r.words
		sum.links += r.links
		sum.leads += r.leads
		sum.saved += r.saved
		sum.plain += r.plain
		sum.small += r.small
	}
	of := "all pages"
	if pages > 0 {
		of = fmt.Sprintf("the first %d pages", pages)
	}
	fmt.Fprintf(w, "# cera API run\n\nThe text, the links and the editor over %d files, %s of each.\n\n", len(rs), of)
	fmt.Fprintln(w, "| files | opened | pages | characters | words found again | links | lead to a page | files written and reopened | refused | with differences | failed |")
	fmt.Fprintln(w, "|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	fmt.Fprintf(w, "| %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
		len(rs), opened, sum.pages, sum.chars, sum.words, sum.links, sum.leads, sum.saved, refused, differing, failed)
	if sum.plain > 0 {
		fmt.Fprintf(w, "\nSaved with everything that makes a file smaller and loses nothing, the files are %d bytes; saved as they are, %d (%.1f %%).\n",
			sum.small, sum.plain, 100*float64(sum.small)/float64(sum.plain))
	}

	list := func(title, intro string, of func(*apiResult) []string) {
		first := true
		for i := range rs {
			for _, s := range of(&rs[i]) {
				if first {
					fmt.Fprintf(w, "\n## %s\n\n%s\n\n", title, intro)
					first = false
				}
				fmt.Fprintf(w, "- `%s`: %s\n", rs[i].file, strings.ReplaceAll(s, "\n", " "))
			}
		}
	}
	list("Failed", "A panic, a hang or a deadline: these fail the run.", func(r *apiResult) []string { return r.failed })
	list("Differences", "What is not as it should be; reported, not failed.", func(r *apiResult) []string { return r.differences })
	list("Refused", "What a document does not allow: it is signed, certified, protected, or needed a repair.", func(r *apiResult) []string { return r.refused })
}
