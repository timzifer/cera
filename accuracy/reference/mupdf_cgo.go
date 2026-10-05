//go:build cgo

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gen2brain/go-fitz"
)

// muPDF renders with MuPDF linked through cgo (go-fitz, which ships its
// static libraries, so nothing needs installing). MuPDF is AGPL: it is
// linked into this tool only, never into cera.
//
// MuPDF aborts the process on some errors it does not catch (pdf.js'
// issue19517.pdf), so each file is rendered by a child process: this
// binary again, in worker mode, given the file on stdin. An abort or a
// hang then costs that file's pages, not the run.
type muPDF struct{}

// muPDFWorkerEnv switches a process into worker mode; its value is the
// directory the pages are written to.
const muPDFWorkerEnv = "CERA_REFERENCE_MUPDF_WORKER"

// muPDFTimeout bounds one file's rendering by the worker.
const muPDFTimeout = 2 * time.Minute

func init() {
	if dir := os.Getenv(muPDFWorkerEnv); dir != "" {
		os.Exit(muPDFWorker(dir, os.Args[1:]))
	}
}

func newMuPDF() (engine, error) { return &muPDF{}, nil }

func (*muPDF) name() string    { return "mupdf" }
func (*muPDF) version() string { return "mupdf-" + fitz.FzVersion }
func (*muPDF) close()          {}

func (m *muPDF) render(f *file, pages []int, scale float64) ([]*image.RGBA, []error) {
	imgs, errs := make([]*image.RGBA, len(pages)), make([]error, len(pages))
	fail := func(err error) ([]*image.RGBA, []error) {
		for k := range errs {
			if imgs[k] == nil && errs[k] == nil {
				errs[k] = err
			}
		}
		return imgs, errs
	}
	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	dir, err := os.MkdirTemp("", "mupdf-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(dir)

	args := []string{strconv.FormatFloat(scale*72, 'g', -1, 64)}
	for _, i := range pages {
		args = append(args, strconv.Itoa(i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), muPDFTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Env = append(os.Environ(), muPDFWorkerEnv+"="+dir)
	cmd.Stdin = bytes.NewReader(f.data)
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()

	for k, i := range pages {
		base := filepath.Join(dir, fmt.Sprintf("p%d", i))
		if msg, err := os.ReadFile(base + ".err"); err == nil {
			errs[k] = errors.New(string(msg))
			continue
		}
		img, err := readPNG(base + ".png")
		if err != nil {
			continue // not reached by the worker
		}
		imgs[k] = fit(img, img.Bounds().Size())
	}
	if runErr != nil {
		if ctx.Err() != nil {
			runErr = fmt.Errorf("no result after %v", muPDFTimeout)
		} else {
			runErr = fmt.Errorf("aborted: %w", runErr)
		}
		return fail(runErr)
	}
	return fail(errors.New("page not rendered"))
}

// muPDFWorker renders the file on stdin at args[0] dpi, the pages
// args[1:], into dir as p<i>.png, or p<i>.err with the error.
func muPDFWorker(dir string, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "mupdf worker: no resolution")
		return 2
	}
	dpi, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mupdf worker:", err)
		return 2
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mupdf worker:", err)
		return 2
	}
	pageErr := func(i int, err error) {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("p%d.err", i)), []byte(strings.TrimSpace(err.Error())), 0o644)
	}
	var pages []int
	for _, a := range args[1:] {
		i, err := strconv.Atoi(a)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mupdf worker:", err)
			return 2
		}
		pages = append(pages, i)
	}
	doc, err := fitz.NewFromMemory(data)
	if err != nil {
		for _, i := range pages {
			pageErr(i, err)
		}
		return 0
	}
	defer doc.Close()
	for _, i := range pages {
		img, err := doc.ImageDPI(i, dpi)
		if err != nil {
			pageErr(i, err)
			continue
		}
		if err := writePNG(filepath.Join(dir, fmt.Sprintf("p%d.png", i)), img); err != nil {
			pageErr(i, err)
		}
	}
	return 0
}
