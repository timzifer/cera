package main

import (
	"fmt"
	"image"
	"math"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// pdfiumEngine renders with PDFium compiled to WebAssembly (go-pdfium on
// wazero), no cgo, as the stilus harness does.
type pdfiumEngine struct {
	pool pdfium.Pool
	inst pdfium.Pdfium
}

func newPDFium() (*pdfiumEngine, error) {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		return nil, err
	}
	inst, err := pool.GetInstance(time.Minute)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &pdfiumEngine{pool: pool, inst: inst}, nil
}

func (p *pdfiumEngine) Close() {
	p.inst.Close()
	p.pool.Close()
}

type pdfiumDoc struct {
	p   *pdfiumEngine
	doc references.FPDF_DOCUMENT
	n   int
}

func (p *pdfiumEngine) open(data []byte) (*pdfiumDoc, error) {
	r, err := p.inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, err
	}
	n, err := p.inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: r.Document})
	if err != nil {
		p.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: r.Document})
		return nil, err
	}
	return &pdfiumDoc{p: p, doc: r.Document, n: n.PageCount}, nil
}

func (d *pdfiumDoc) Close() {
	d.p.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: d.doc})
}

// render draws page (0-based) at scale on white, into a bitmap sized as
// cera sizes pages (rounded up), with an exact matrix: PDFium's own DPI
// rendering stretches the page to the rounded size, which shifts content by
// up to a pixel across a page.
func (d *pdfiumDoc) render(page int, scale float64) (*image.RGBA, error) {
	in := d.p.inst
	pg, err := in.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: d.doc, Index: page})
	if err != nil {
		return nil, err
	}
	defer in.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: pg.Page})
	ref := requests.Page{ByReference: &pg.Page}
	pw, err := in.FPDF_GetPageWidthF(&requests.FPDF_GetPageWidthF{Page: ref})
	if err != nil {
		return nil, err
	}
	ph, err := in.FPDF_GetPageHeightF(&requests.FPDF_GetPageHeightF{Page: ref})
	if err != nil {
		return nil, err
	}
	w, h := int(math.Ceil(float64(pw.PageWidth)*scale)), int(math.Ceil(float64(ph.PageHeight)*scale))
	if w <= 0 || h <= 0 || w*h > maxPixels {
		return nil, fmt.Errorf("pdfium: page of %d×%d px", w, h)
	}
	bm, err := in.FPDFBitmap_Create(&requests.FPDFBitmap_Create{Width: w, Height: h, Alpha: 0})
	if err != nil {
		return nil, err
	}
	defer in.FPDFBitmap_Destroy(&requests.FPDFBitmap_Destroy{Bitmap: bm.Bitmap})
	if _, err := in.FPDFBitmap_FillRect(&requests.FPDFBitmap_FillRect{Bitmap: bm.Bitmap, Width: w, Height: h, Color: 0xffffffff}); err != nil {
		return nil, err
	}
	_, err = in.FPDF_RenderPageBitmapWithMatrix(&requests.FPDF_RenderPageBitmapWithMatrix{
		Bitmap:   bm.Bitmap,
		Page:     ref,
		Matrix:   structs.FPDF_FS_MATRIX{A: float32(scale), D: float32(scale)},
		Clipping: structs.FPDF_FS_RECTF{Right: float32(w), Bottom: float32(h)},
		Flags:    enums.FPDF_RENDER_FLAG_ANNOT,
	})
	if err != nil {
		return nil, err
	}
	st, err := in.FPDFBitmap_GetStride(&requests.FPDFBitmap_GetStride{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	buf, err := in.FPDFBitmap_GetBuffer(&requests.FPDFBitmap_GetBuffer{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		src := buf.Buffer[y*st.Stride:]
		dst := out.Pix[y*out.Stride:]
		for x := range w { // BGRx to RGBA
			dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = src[4*x+2], src[4*x+1], src[4*x], 255
		}
	}
	return out, nil
}
