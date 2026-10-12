package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/timzifer/cera"
	"github.com/timzifer/cera/internal/corpus"
)

// run -api runs what a viewer and an editor ask of a document, beyond
// drawing it, over the files: the text with round trips of search and
// selection, the links and where they lead, and the editor — a file saved
// anew, an update with a highlight, the form and the annotations
// flattened — each opened again and its first page compared with what it
// was.
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
		errors.Is(err, cera.ErrNotPermitted), errors.Is(err, cera.ErrNoUpdate), errors.Is(err, cera.ErrPassword):
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
	r.step("save", func() {
		var out bytes.Buffer
		if r.check("save", doc.Edit().Save(&out)) {
			reopen("save", out.Bytes(), true)
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
	p, err := doc.Page(i)
	if err != nil {
		return nil, err
	}
	defer p.Release()
	b := p.Bounds(1)
	if b.Dx()*b.Dy() > maxPixels/4 {
		return nil, nil
	}
	dst := image.NewRGBA(b)
	err = p.Render(context.Background(), dst, cera.RenderOptions{
		Scale: 1, Background: color.RGBA{255, 255, 255, 255}, Deadline: time.Now().Add(c.timeout), Workers: c.workers,
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
