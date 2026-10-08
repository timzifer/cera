// Command cera renders PDF pages to PNG.
//
//	cera -dpi 150 -page 1 -o page.png input.pdf
//	cera -dpi 150 -o 'out-%d.png' input.pdf   # all pages
//	cera -field name=Ada -field agree=Yes -page 1 form.pdf
//	cera -cmyk-profile CoatedFOGRA39.icc -page 1 print.pdf
//
// PNGs are written at the fastest compression level. With -png stream
// (the default) each band of a page is compressed by calamus as soon as
// it is drawn, while the others are still drawn; -png encode compresses
// the page with calamus after drawing it, in bands on all cores; -png
// stdlib with image/png, on one core. Compressing bands on their own
// makes the files somewhat larger than one stream would.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	stdpng "image/png"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/timzifer/calamus/png"
	"github.com/timzifer/cera"
)

func main() {
	dpi := flag.Float64("dpi", 150, "resolution")
	page := flag.Int("page", 0, "page to render, 1-based (0 = all)")
	out := flag.String("o", "page-%d.png", "output file; %d is replaced by the page number")
	password := flag.String("password", "", "password of an encrypted file")
	transparent := flag.Bool("transparent", false, "leave the background transparent")
	timeout := flag.Duration("timeout", time.Minute, "deadline per page")
	verbose := flag.Bool("v", false, "print timing and statistics per page")
	workers := flag.Int("workers", 0, "goroutines drawing one page (0 = all cores)")
	annots := flag.String("annots", "view", "annotations to draw: view, print or none")
	imageFilter := flag.String("image-filter", "nearest", "how magnified images without /Interpolate are sampled: nearest or smooth (bilinearly below 2× magnification)")
	cmykProfile := flag.String("cmyk-profile", "", "ICC profile `file` DeviceCMYK is converted through instead of the bundled SWOP profile")
	naiveCMYK := flag.Bool("naive-cmyk", false, "convert CMYK naively, as device values, without a profile")
	pngMode := flag.String("png", "stream", "how PNGs are compressed: stream (calamus, bands while they are drawn), encode (calamus, after drawing) or stdlib (image/png)")
	var fields []string
	flag.Func("field", "set a form field, `name=value` (repeatable); a check box or radio button takes the name of its state", func(s string) error {
		if !strings.Contains(s, "=") {
			return fmt.Errorf("%q: want name=value", s)
		}
		fields = append(fields, s)
		return nil
	})
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cera [flags] input.pdf")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	modes := map[string]cera.AnnotMode{"view": cera.AnnotsView, "print": cera.AnnotsPrint, "none": cera.AnnotsNone}
	mode, ok := modes[*annots]
	if !ok {
		fmt.Fprintf(os.Stderr, "cera: -annots %q: want view, print or none\n", *annots)
		os.Exit(2)
	}
	filters := map[string]cera.ImageFilter{"nearest": cera.ImageNearest, "smooth": cera.ImageSmooth}
	filter, ok := filters[*imageFilter]
	if !ok {
		fmt.Fprintf(os.Stderr, "cera: -image-filter %q: want nearest or smooth\n", *imageFilter)
		os.Exit(2)
	}
	if !slices.Contains([]string{"stream", "encode", "stdlib"}, *pngMode) {
		fmt.Fprintf(os.Stderr, "cera: -png %q: want stream, encode or stdlib\n", *pngMode)
		os.Exit(2)
	}
	opt := cera.OpenOptions{Password: *password, NaiveCMYK: *naiveCMYK}
	if *cmykProfile != "" {
		b, err := os.ReadFile(*cmykProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cera:", err)
			os.Exit(2)
		}
		opt.CMYKProfile = b
	}
	if err := run(flag.Arg(0), opt, *dpi, *page, *out, *transparent, *timeout, *verbose, *workers, mode, filter, fields, *pngMode); err != nil {
		fmt.Fprintln(os.Stderr, "cera:", err)
		os.Exit(1)
	}
}

func run(in string, opt cera.OpenOptions, dpi float64, page int, out string, transparent bool, timeout time.Duration, verbose bool, workers int, annots cera.AnnotMode, filter cera.ImageFilter, fields []string, pngMode string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	doc, err := cera.OpenWith(data, opt)
	if err != nil {
		return err
	}
	form, err := fill(doc, fields)
	if err != nil {
		return err
	}
	first, last := 1, doc.NumPages()
	if page > 0 {
		if page > last {
			return fmt.Errorf("page %d out of range (1–%d)", page, last)
		}
		first, last = page, page
	}
	if first != last && !strings.Contains(out, "%d") {
		return fmt.Errorf("-o %q needs %%d to render %d pages", out, last-first+1)
	}
	bg := color.RGBA{255, 255, 255, 255}
	if transparent {
		bg = color.RGBA{}
	}
	var failed int
	for n := first; n <= last; n++ {
		p, err := doc.Page(n - 1)
		if err != nil {
			return err
		}
		name := out
		if strings.Contains(out, "%d") {
			name = fmt.Sprintf(out, n)
		}
		dst := image.NewRGBA(p.Bounds(dpi / 72))
		var st cera.Stats
		ropt := cera.RenderOptions{
			Scale: dpi / 72, Background: bg, Deadline: time.Now().Add(timeout), Stats: &st,
			Workers: workers, Annotations: annots, Form: form, ImageFilter: filter,
		}
		var sw *streamPNG
		if pngMode == "stream" {
			if sw, err = newStreamPNG(name, dst, bg.A == 255); err != nil {
				return err
			}
			ropt.Band = sw.band
		}
		t0 := time.Now()
		err = p.Render(context.Background(), dst, ropt)
		p.Release()
		elapsed := time.Since(t0)
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "page %d: %v\n", n, err)
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "page %d: %v, %d ops, %d fills, %d strokes, %d clips, %d glyphs, %d images, %d groups, %d content errors",
				n, elapsed.Round(10*time.Microsecond), st.Ops, st.Fills, st.Strokes, st.Clips, st.Glyphs, st.Images, st.Groups, st.Errors)
			for _, k := range st.UnsupportedKeys() {
				fmt.Fprintf(os.Stderr, ", %s×%d", k, st.Unsupported[k])
			}
			fmt.Fprintln(os.Stderr)
		}
		if sw != nil {
			err = sw.close()
		} else {
			err = writePNG(name, dst, pngMode)
		}
		if err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d pages rendered partially", failed)
	}
	return nil
}

// fill returns the state of the document's form with the name=value
// pairs set, nil if there are none.
func fill(doc *cera.Document, fields []string) (*cera.FormState, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	form := doc.Form()
	if form == nil {
		return nil, fmt.Errorf("-field: the document has no form")
	}
	s := form.NewState()
	for _, kv := range fields {
		name, value, _ := strings.Cut(kv, "=")
		f := form.Field(name)
		if f == nil {
			return nil, fmt.Errorf("-field: no field %q", name)
		}
		if err := s.SetValue(f, cera.TextValue(value)); err != nil {
			return nil, fmt.Errorf("-field: %w", err)
		}
	}
	return s, nil
}

// writePNG writes img to the file name after drawing: with calamus in
// bands on all cores, or with image/png. Encoding, not rendering, was most
// of this command's time with image/png (#67).
func writePNG(name string, img image.Image, mode string) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	if mode == "stdlib" {
		err = (&stdpng.Encoder{CompressionLevel: stdpng.BestSpeed}).Encode(f, img)
	} else {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(f, img)
	}
	return cmp.Or(err, f.Close())
}

// streamRows is the fewest rows streamPNG compresses together: calamus
// loses at most 0.4 % in size on bands of 256 rows, up to 3 % on 64.
const streamRows = 256

// streamPNG writes a page to a PNG file while it is drawn: band is
// RenderOptions.Band. Finished parts are joined with the finished rows
// next to them and handed to calamus once streamRows are together, on
// the goroutine that finished them.
type streamPNG struct {
	f   *os.File
	w   *png.Writer
	dst *image.RGBA

	mu   sync.Mutex
	open []image.Rectangle // finished rows not handed over yet
	err  error
}

func newStreamPNG(name string, dst *image.RGBA, opaque bool) (*streamPNG, error) {
	f, err := os.Create(name)
	if err != nil {
		return nil, err
	}
	enc := &png.Encoder{CompressionLevel: png.BestSpeed}
	b := dst.Bounds()
	w, err := enc.NewWriter(f, png.Header{Width: b.Dx(), Height: b.Dy(), ColorModel: color.RGBAModel, Opaque: opaque})
	if err != nil {
		f.Close()
		return nil, err
	}
	return &streamPNG{f: f, w: w, dst: dst}, nil
}

func (s *streamPNG) band(r image.Rectangle) {
	s.mu.Lock()
	for i := 0; i < len(s.open); {
		if o := s.open[i]; o.Max.Y == r.Min.Y || o.Min.Y == r.Max.Y {
			r = r.Union(o)
			s.open = slices.Delete(s.open, i, i+1)
			continue
		}
		i++
	}
	if r.Dy() < streamRows && r != s.dst.Bounds() {
		s.open = append(s.open, r)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.write(r)
}

func (s *streamPNG) write(r image.Rectangle) {
	if err := s.w.WriteRows(s.dst.SubImage(r)); err != nil {
		s.mu.Lock()
		s.err = cmp.Or(s.err, err)
		s.mu.Unlock()
	}
}

// close hands over the rows left and finishes the file.
func (s *streamPNG) close() error {
	for _, r := range s.open {
		s.write(r)
	}
	s.open = nil
	return cmp.Or(s.err, s.w.Close(), s.f.Close())
}
