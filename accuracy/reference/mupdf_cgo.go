//go:build cgo

package main

import (
	"image"

	"github.com/gen2brain/go-fitz"
)

// muPDF renders with MuPDF linked through cgo (go-fitz, which ships its
// static libraries, so nothing needs installing). MuPDF is AGPL: it is
// linked into this tool only, never into cera.
type muPDF struct{}

func newMuPDF() (engine, error) { return &muPDF{}, nil }

func (*muPDF) name() string    { return "mupdf" }
func (*muPDF) version() string { return "mupdf-" + fitz.FzVersion }
func (*muPDF) close()          {}

func (m *muPDF) render(f *file, pages []int, scale float64) ([]*image.RGBA, []error) {
	imgs, errs := make([]*image.RGBA, len(pages)), make([]error, len(pages))
	doc, err := fitz.NewFromMemory(f.data)
	if err != nil {
		for i := range errs {
			errs[i] = err
		}
		return imgs, errs
	}
	defer doc.Close()
	for k, i := range pages {
		img, err := doc.ImageDPI(i, scale*72)
		if err != nil {
			errs[k] = err
			continue
		}
		imgs[k] = fit(img, img.Bounds().Size())
	}
	return imgs, errs
}
