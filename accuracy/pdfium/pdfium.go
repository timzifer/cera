// Package pdfium renders with PDFium compiled to WebAssembly (go-pdfium on
// wazero), no cgo, as the stilus harness does.
package pdfium

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

// MaxPixels bounds the bitmap of one page.
const MaxPixels = 64 << 20

// Engine is one WebAssembly PDFium instance; it is not safe for concurrent
// use.
type Engine struct {
	pool pdfium.Pool
	inst pdfium.Pdfium
}

// New starts an instance.
func New() (*Engine, error) {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		return nil, err
	}
	inst, err := pool.GetInstance(time.Minute)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Engine{pool: pool, inst: inst}, nil
}

// Close stops the instance.
func (p *Engine) Close() {
	p.inst.Close()
	p.pool.Close()
}

// Doc is an open document.
type Doc struct {
	p   *Engine
	doc references.FPDF_DOCUMENT
	n   int
}

// Open opens a document.
func (p *Engine) Open(data []byte) (*Doc, error) { return p.OpenPassword(data, "") }

// OpenPassword opens a document with a password ("" for none).
func (p *Engine) OpenPassword(data []byte, password string) (*Doc, error) {
	req := &requests.OpenDocument{File: &data}
	if password != "" {
		req.Password = &password
	}
	r, err := p.inst.OpenDocument(req)
	if err != nil {
		return nil, err
	}
	n, err := p.inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: r.Document})
	if err != nil {
		p.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: r.Document})
		return nil, err
	}
	return &Doc{p: p, doc: r.Document, n: n.PageCount}, nil
}

// Pages is the number of pages.
func (d *Doc) Pages() int { return d.n }

// Close closes the document.
func (d *Doc) Close() {
	d.p.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: d.doc})
}

// Render draws page (0-based) at scale on white, into a bitmap sized as
// cera sizes pages (rounded up), with an exact matrix: PDFium's own DPI
// rendering stretches the page to the rounded size, which shifts content by
// up to a pixel across a page. With annots, annotations with an appearance
// are drawn too (form widgets are not: there is no form handle).
func (d *Doc) Render(page int, scale float64, annots bool) (*image.RGBA, error) {
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
	if w <= 0 || h <= 0 || w*h > MaxPixels {
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
	var flags enums.FPDF_RENDER_FLAG
	if annots {
		flags = enums.FPDF_RENDER_FLAG_ANNOT
	}
	_, err = in.FPDF_RenderPageBitmapWithMatrix(&requests.FPDF_RenderPageBitmapWithMatrix{
		Bitmap:   bm.Bitmap,
		Page:     ref,
		Matrix:   structs.FPDF_FS_MATRIX{A: float32(scale), D: float32(scale)},
		Clipping: structs.FPDF_FS_RECTF{Right: float32(w), Bottom: float32(h)},
		Flags:    flags,
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
