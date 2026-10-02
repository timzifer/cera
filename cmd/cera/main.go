// Command cera renders PDF pages to PNG.
//
//	cera -dpi 150 -page 1 -o page.png input.pdf
//	cera -dpi 150 -o 'out-%d.png' input.pdf   # all pages
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"time"

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
	if err := run(flag.Arg(0), *dpi, *page, *out, *password, *transparent, *timeout, *verbose, *workers, mode); err != nil {
		fmt.Fprintln(os.Stderr, "cera:", err)
		os.Exit(1)
	}
}

func run(in string, dpi float64, page int, out, password string, transparent bool, timeout time.Duration, verbose bool, workers int, annots cera.AnnotMode) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	doc, err := cera.OpenWithPassword(data, password)
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
		dst := image.NewRGBA(p.Bounds(dpi / 72))
		var st cera.Stats
		t0 := time.Now()
		err = p.Render(context.Background(), dst, cera.RenderOptions{
			Scale: dpi / 72, Background: bg, Deadline: time.Now().Add(timeout), Stats: &st,
			Workers: workers, Annotations: annots,
		})
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
		name := out
		if strings.Contains(out, "%d") {
			name = fmt.Sprintf(out, n)
		}
		if err := writePNG(name, dst); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d pages rendered partially", failed)
	}
	return nil
}

func writePNG(name string, img image.Image) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
