package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/timzifer/cera"
	"github.com/timzifer/cera/accuracy/exact"
	"github.com/timzifer/cera/accuracy/pdfium"
	"github.com/timzifer/cera/internal/corpus"
)

// file is a corpus file with the pixel size of each page (cera's sizes,
// which every rendering is cropped or padded to).
type file struct {
	path, rel, category string
	password            string // from a pdf.js manifest (corpus.Password)
	data                []byte
	key                 string // SHA-256 prefix
	sizes               []image.Point
	pts                 []point          // page size in points, as cera sees it
	unsupported         map[int][]string // cera's Stats.Unsupported keys per page
}

type point struct{ w, h float64 }

// openOptions are the options cera opens f with.
func (f *file) openOptions() cera.OpenOptions {
	opt := openOptions
	opt.Password = f.password
	return opt
}

// engine renders the pages of a file. Engines are not used concurrently.
type engine interface {
	name() string
	// version identifies the build, for the cache; empty disables caching.
	version() string
	// render draws pages (0-based) of f at scale on white.
	render(f *file, pages []int, scale float64) ([]*image.RGBA, []error)
	close()
}

// engineOrder is the fixed order (and colour slot) of the engines.
var engineOrder = []string{"cera", "pdfium", "mupdf", "poppler", "ghostscript"}

// newEngine starts the engine called name, or says why it is not available.
// With annots, the engines draw annotations as far as they can; without,
// the page content only. MuPDF through cgo draws the page content only
// either way, and PDFium draws no form widgets, so only the page content
// is compared like with like.
func newEngine(name string, timeout time.Duration, annots bool) (engine, error) {
	switch name {
	case "cera":
		return ceraEngine{timeout, annots}, nil
	case "pdfium":
		p, err := pdfium.New()
		if err != nil {
			return nil, err
		}
		return &pdfiumEngine{p, annots}, nil
	case "mupdf":
		return newMuPDF(annots)
	case "poppler":
		return newExec("poppler", "pdftoppm", "-v", annots)
	case "ghostscript":
		return newExec("ghostscript", "gs", "--version", annots)
	}
	return nil, fmt.Errorf("unknown engine %q", name)
}

// ceraEngine renders with the cera of this checkout; never cached.
type ceraEngine struct {
	timeout time.Duration
	annots  bool
}

func (ceraEngine) name() string    { return "cera" }
func (ceraEngine) version() string { return "" }
func (ceraEngine) close()          {}

func (c ceraEngine) render(f *file, pages []int, scale float64) ([]*image.RGBA, []error) {
	imgs, errs := make([]*image.RGBA, len(pages)), make([]error, len(pages))
	doc, err := cera.OpenWith(f.data, f.openOptions())
	if err != nil {
		for i := range errs {
			errs[i] = err
		}
		return imgs, errs
	}
	f.unsupported = map[int][]string{}
	for k, i := range pages {
		p, err := doc.Page(i)
		if err != nil {
			errs[k] = err
			continue
		}
		img := image.NewRGBA(p.Bounds(scale))
		mode := cera.AnnotsNone
		if c.annots {
			mode = cera.AnnotsView
		}
		var st cera.Stats
		err = p.Render(context.Background(), img, cera.RenderOptions{
			Scale: scale, Background: color.RGBA{255, 255, 255, 255}, Deadline: time.Now().Add(c.timeout), Stats: &st,
			Annotations: mode,
		})
		f.unsupported[i] = st.UnsupportedKeys()
		var pe *cera.PanicError
		if errors.As(err, &pe) || errors.Is(err, cera.ErrDeadline) {
			errs[k] = err
			continue
		}
		imgs[k] = img
		p.Release()
	}
	return imgs, errs
}

type pdfiumEngine struct {
	p      *pdfium.Engine
	annots bool
}

func (*pdfiumEngine) name() string { return "pdfium" }
func (e *pdfiumEngine) version() string {
	return "pdfium-wasm-go-pdfium-1.21.1" + contentSuffix(e.annots)
}
func (e *pdfiumEngine) close() { e.p.Close() }

// contentSuffix tells apart in the cache the renderings of the page content
// only from those with annotations, which keep the old cache names.
func contentSuffix(annots bool) string {
	if annots {
		return ""
	}
	return "-content"
}

func (e *pdfiumEngine) render(f *file, pages []int, scale float64) ([]*image.RGBA, []error) {
	imgs, errs := make([]*image.RGBA, len(pages)), make([]error, len(pages))
	doc, err := e.p.OpenPassword(f.data, f.password)
	if err != nil {
		for i := range errs {
			errs[i] = err
		}
		return imgs, errs
	}
	defer doc.Close()
	for k, i := range pages {
		imgs[k], errs[k] = doc.Render(i, scale, e.annots)
	}
	return imgs, errs
}

// execEngine runs a renderer's command-line tool per page.
type execEngine struct {
	id, bin, ver string
	annots       bool
}

func newExec(id, bin, versionFlag string, annots bool) (engine, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("%s not installed (%s)", bin, id)
	}
	out, _ := exec.Command(path, versionFlag).CombinedOutput()
	ver := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return &execEngine{id: id, bin: path, ver: id + "-" + ver, annots: annots}, nil
}

func (e *execEngine) name() string { return e.id }

// version keys the cache; mutool draw has no switch for annotations, so
// its renderings are the same in both modes. Poppler and Ghostscript
// renderings name the crop box, which older cached ones did not draw.
func (e *execEngine) version() string {
	if e.id == "mupdf" {
		return e.ver
	}
	return e.ver + "-cropbox" + contentSuffix(e.annots)
}
func (e *execEngine) close() {}

func (e *execEngine) render(f *file, pages []int, scale float64) ([]*image.RGBA, []error) {
	imgs, errs := make([]*image.RGBA, len(pages)), make([]error, len(pages))
	tmp, err := os.MkdirTemp("", "cera-ref-*")
	if err != nil {
		for i := range errs {
			errs[i] = err
		}
		return imgs, errs
	}
	defer os.RemoveAll(tmp)
	dpi := strconvG(scale * 72)
	for k, i := range pages {
		out := filepath.Join(tmp, fmt.Sprintf("p%d.png", i+1))
		var args []string
		switch e.id {
		case "poppler":
			args = []string{"-r", dpi, "-f", fmt.Sprint(i + 1), "-l", fmt.Sprint(i + 1), "-png", "-singlefile", "-aa", "yes", "-aaVector", "yes", "-cropbox"}
			if !e.annots {
				args = append(args, "-hide-annotations")
			}
			if f.password != "" {
				args = append(args, "-opw", f.password, "-upw", f.password)
			}
			args = append(args, f.path, strings.TrimSuffix(out, ".png"))
		case "mupdf":
			args = []string{"draw", "-q", "-r", dpi, "-o", out}
			if f.password != "" {
				args = append(args, "-p", f.password)
			}
			args = append(args, f.path, fmt.Sprint(i+1))
		case "ghostscript":
			// Ghostscript anchors pages at the bottom left of a bitmap of
			// rounded size; the page is drawn into cera's size, moved up by
			// the fraction cera's rounding adds at the top.
			sz, pt := f.sizes[i], f.pts[i]
			shift := (float64(sz.Y) - pt.h*scale) / scale
			args = []string{"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=png16m", "-r" + dpi,
				"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", fmt.Sprintf("-g%dx%d", sz.X, sz.Y), "-dFIXEDMEDIA", "-dUseCropBox",
				fmt.Sprintf("-dFirstPage=%d", i+1), fmt.Sprintf("-dLastPage=%d", i+1), "-o", out}
			if !e.annots {
				args = append(args, "-dShowAnnots=false")
			}
			if f.password != "" {
				args = append(args, "-sPDFPassword="+f.password)
			}
			args = append(args, "-c", fmt.Sprintf("<< /Install { 0 %.6f translate } >> setpagedevice", shift), "-f", f.path)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		msg, err := exec.CommandContext(ctx, e.bin, args...).CombinedOutput()
		cancel()
		if err != nil {
			errs[k] = fmt.Errorf("%s: %v: %s", e.id, err, firstLine(msg))
			continue
		}
		imgs[k], errs[k] = readPNG(out)
	}
	return imgs, errs
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

func strconvG(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
}

func newExecMuPDF(annots bool) (engine, error) {
	e, err := newExec("mupdf", "mutool", "-v", annots)
	if err != nil {
		return nil, err
	}
	e.(*execEngine).ver = "mupdf-" + strings.TrimPrefix(e.version(), "mupdf-")
	return e, nil
}

// exactEngine renders the synthetic drawings exactly (package exact);
// other files have no exact rendering.
type exactEngine struct{}

func (exactEngine) name() string    { return "exact" }
func (exactEngine) version() string { return "exact-1-rows" + fmt.Sprint(exact.Rows) }
func (exactEngine) close()          {}

func (exactEngine) render(f *file, pages []int, scale float64) ([]*image.RGBA, []error) {
	imgs, errs := make([]*image.RGBA, len(pages)), make([]error, len(pages))
	name, ok := strings.CutPrefix(f.rel, "synthetic/")
	var content string
	if ok {
		for _, s := range corpus.Scenes() {
			if s.Name+".pdf" == name {
				var b strings.Builder
				s.Content(&b)
				content = b.String()
			}
		}
	}
	for k, i := range pages {
		if content == "" || i != 0 {
			errs[k] = errNoExact
			continue
		}
		pg, err := exact.Interpret(content, f.pts[0].w, f.pts[0].h, scale)
		if err != nil {
			errs[k] = err
			continue
		}
		imgs[k] = pg.Render()
	}
	return imgs, errs
}

var errNoExact = errors.New("no exact rendering")

// fit returns img on white, cropped or padded to size.
func fit(img image.Image, size image.Point) *image.RGBA {
	out := image.NewRGBA(image.Rectangle{Max: size})
	draw.Draw(out, out.Rect, image.White, image.Point{}, draw.Src)
	draw.Draw(out, out.Rect, img, img.Bounds().Min, draw.Over)
	return out
}
