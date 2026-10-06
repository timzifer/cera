package main

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/timzifer/cera/bench/internal/clock"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// workerEnv switches the bench binary into one of its worker modes.
const workerEnv = "BENCH_WORKER"

func init() {
	switch os.Getenv(workerEnv) {
	case "":
		return
	case "wasm-host":
		os.Exit(wasmHost())
	case "pdfium-wasm":
		os.Exit(pdfiumWorker())
	}
	fmt.Fprintln(os.Stderr, "unknown worker", os.Getenv(workerEnv))
	os.Exit(2)
}

// wasmHost runs ceraworker built for wasip1 on wazero (the runtime
// go-pdfium runs PDFium on), with the directory of the file to open
// mounted as the root.
func wasmHost() int {
	ctx := context.Background()
	bin, err := os.ReadFile(os.Getenv("BENCH_WASM"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cfg := wazero.NewRuntimeConfig()
	if dir := os.Getenv("BENCH_WASM_CACHE"); dir != "" {
		if c, err := wazero.NewCompilationCacheWithDir(dir); err == nil {
			cfg = cfg.WithCompilationCache(c)
		}
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	defer rt.Close(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	mod, err := rt.CompileModule(ctx, bin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	mc := wazero.NewModuleConfig().
		WithStdin(os.Stdin).WithStdout(os.Stdout).WithStderr(os.Stderr).
		WithNanotime(clock.Now, 1).WithSysWalltime().WithSysNanosleep().
		WithFSConfig(wazero.NewFSConfig().WithDirMount(os.Getenv("BENCH_WASM_ROOT"), "/")).
		WithArgs("ceraworker")
	if _, err := rt.InstantiateModule(ctx, mod, mc); err != nil && !strings.Contains(err.Error(), "exit_code(0)") {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// pdfiumWorker answers the protocol with PDFium compiled to WebAssembly
// (go-pdfium on wazero), drawing into a PDFium bitmap that stays inside
// the WebAssembly memory: nothing is copied out but for ink.
func pdfiumWorker() int {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer pool.Close()
	inst, err := pool.GetInstance(time.Minute)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer inst.Close()
	p := &pdfiumWasm{in: inst}
	out := bufio.NewWriter(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if f[0] == "quit" {
			break
		}
		var r []string
		if fn, ok := map[string]func([]string) ([]string, error){
			"version": func([]string) ([]string, error) { return []string{"pdfium (wasm, go-pdfium 1.21.1)"}, nil },
			"open":    p.open,
			"render":  p.render,
		}[f[0]]; ok {
			v, err := fn(f)
			if err != nil {
				r = []string{"err", strings.NewReplacer("\t", " ", "\n", " ").Replace(err.Error())}
			} else {
				r = append([]string{"ok"}, v...)
			}
		} else {
			r = []string{"unsupported"}
		}
		out.WriteString(strings.Join(r, "\t") + "\n")
		out.Flush()
	}
	p.closeDoc()
	return 0
}

type pdfiumWasm struct {
	in  pdfium.Pdfium
	doc *references.FPDF_DOCUMENT
}

func (p *pdfiumWasm) closeDoc() {
	if p.doc != nil {
		p.in.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: *p.doc})
		p.doc = nil
	}
}

func (p *pdfiumWasm) open(f []string) ([]string, error) {
	data, err := os.ReadFile(f[1])
	if err != nil {
		return nil, err
	}
	req := &requests.OpenDocument{File: &data}
	if len(f) > 2 && f[2] != "" {
		req.Password = &f[2]
	}
	p.closeDoc()
	t0 := clock.Now()
	r, err := p.in.OpenDocument(req)
	if err != nil {
		return nil, err
	}
	n, err := p.in.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: r.Document})
	ns := clock.Since(t0)
	if err != nil {
		return nil, err
	}
	p.doc = &r.Document
	return []string{strconv.FormatInt(int64(ns), 10), strconv.Itoa(n.PageCount)}, nil
}

func (p *pdfiumWasm) render(f []string) ([]string, error) {
	i, _ := strconv.Atoi(f[1])
	scale, _ := strconv.ParseFloat(f[2], 64)
	in := p.in
	t0 := clock.Now()
	pg, err := in.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: *p.doc, Index: i})
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
	if w <= 0 || h <= 0 || w*h > 64<<20 {
		return nil, fmt.Errorf("page of %d×%d px", w, h)
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
	ns := clock.Since(t0)
	if err != nil {
		return nil, err
	}
	r := []string{strconv.FormatInt(int64(ns), 10), strconv.Itoa(w), strconv.Itoa(h)}
	if len(f) > 3 && f[3] == "1" {
		buf, err := in.FPDFBitmap_GetBuffer(&requests.FPDFBitmap_GetBuffer{Bitmap: bm.Bitmap})
		if err != nil {
			return nil, err
		}
		st, err := in.FPDFBitmap_GetStride(&requests.FPDFBitmap_GetStride{Bitmap: bm.Bitmap})
		if err != nil {
			return nil, err
		}
		img := &image.RGBA{Pix: buf.Buffer, Stride: st.Stride, Rect: image.Rect(0, 0, w, h)}
		r = append(r, strconv.FormatFloat(inkBGRx(img), 'f', 5, 64))
	}
	return r, nil
}

// inkBGRx is the share of colour values below white in a 4-byte-per-pixel
// bitmap.
func inkBGRx(img *image.RGBA) float64 {
	n, all := 0, 0
	for y := range img.Rect.Dy() {
		row := img.Pix[y*img.Stride : y*img.Stride+img.Rect.Dx()*4]
		for x := 0; x < len(row); x += 4 {
			for _, v := range row[x : x+3] {
				if v != 255 {
					n++
				}
			}
			all += 3
		}
	}
	if all == 0 {
		return 0
	}
	return float64(n) / float64(all)
}
