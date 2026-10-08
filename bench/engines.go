package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// engine is a renderer driven through a worker process.
type engine struct {
	name    string // key in reports
	label   string // shown in tables
	wasm    bool   // runs as WebAssembly: one core, compared apart
	wasmFS  bool   // sees only the file's directory, mounted as the root
	argv    []string
	env     []string
	version string
}

// engineOrder is the fixed order of engines in reports.
var engineOrder = []string{"mupdf", "cera", "pdfium", "hayro", "pdfjs", "cera-wasm", "cera-v8-wasm", "pdfium-wasm"}

var engineLabels = map[string]string{
	"mupdf":        "MuPDF",
	"cera":         "cera",
	"pdfium":       "PDFium",
	"hayro":        "hayro",
	"pdfjs":        "pdf.js",
	"cera-wasm":    "cera (wasm, wazero)",
	"cera-v8-wasm": "cera (wasm, V8)",
	"pdfium-wasm":  "PDFium (wasm, wazero)",
}

// setup makes engine name ready (building its worker if needed) and asks
// its version, or says why it is not available.
func setup(name, cache string) (*engine, error) {
	e := &engine{name: name, label: engineLabels[name]}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	switch name {
	case "cera":
		bin := filepath.Join(cache, "ceraworker"+exe)
		if err := goBuild(bin, nil); err != nil {
			return nil, err
		}
		e.argv = []string{bin}
	case "cera-wasm":
		wasm := filepath.Join(cache, "ceraworker.wasm")
		if err := goBuild(wasm, []string{"GOOS=wasip1", "GOARCH=wasm"}); err != nil {
			return nil, err
		}
		e.wasm, e.wasmFS = true, true
		e.argv = []string{self}
		e.env = []string{workerEnv + "=wasm-host", "BENCH_WASM=" + wasm, "BENCH_WASM_CACHE=" + filepath.Join(cache, "wazero")}
	case "cera-v8-wasm":
		// The same module under Node's WASI: V8, as in a browser. PDFium's
		// module needs Emscripten's imports, so it stays on wazero only.
		wasm := filepath.Join(cache, "ceraworker.wasm")
		if err := goBuild(wasm, []string{"GOOS=wasip1", "GOARCH=wasm"}); err != nil {
			return nil, err
		}
		e.wasm, e.wasmFS = true, true
		e.argv = []string{*node, "--single-threaded", "--no-warnings", filepath.Join("workers", "wasi", "host.mjs")}
		e.env = []string{"BENCH_WASM=" + wasm}
	case "pdfium-wasm":
		e.wasm = true
		e.argv = []string{self}
		e.env = []string{workerEnv + "=pdfium-wasm"}
	case "mupdf", "pdfium":
		e.argv = []string{*python, filepath.Join("workers", "pyworker.py"), name}
	case "hayro":
		bin := filepath.Join("workers", "hayro", "target", "release", "hayro-worker"+exe)
		if _, err := exec.LookPath("cargo"); err == nil {
			cmd := exec.Command("cargo", "build", "--release", "-q")
			cmd.Dir = filepath.Join("workers", "hayro")
			if out, err := cmd.CombinedOutput(); err != nil {
				return nil, fmt.Errorf("cargo build: %v: %s", err, firstLine(out))
			}
		}
		if _, err := os.Stat(bin); err != nil {
			return nil, fmt.Errorf("no hayro worker (cargo build --release in workers/hayro)")
		}
		e.argv = []string{bin}
	case "pdfjs":
		if _, err := os.Stat(filepath.Join("workers", "pdfjs", "node_modules", "pdfjs-dist")); err != nil {
			return nil, fmt.Errorf("pdf.js not installed (npm ci in workers/pdfjs)")
		}
		e.argv = []string{*node, "--single-threaded", filepath.Join("workers", "pdfjs", "worker.mjs")}
	default:
		return nil, fmt.Errorf("unknown engine %q", name)
	}
	w, err := e.start(".", *timeout)
	if err != nil {
		return nil, err
	}
	defer w.close()
	v, err := w.call("version")
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	e.version = v[0]
	if name == "cera" || name == "cera-wasm" || name == "cera-v8-wasm" {
		e.version = "cera " + ceraVersion()
	}
	return e, nil
}

// goBuild builds ./ceraworker to out.
func goBuild(out string, env []string) error {
	args := []string{"build", "-trimpath", "-o", out}
	if *ceraFresh {
		args = append(args, "-ldflags=-X main.fresh=1")
	}
	cmd := exec.Command("go", append(args, "./ceraworker")...)
	cmd.Env = append(os.Environ(), env...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s: %v: %s", filepath.Base(out), err, b)
	}
	return nil
}

// ceraVersion is the checkout's commit, marked when it has changes.
func ceraVersion() string {
	out, err := exec.Command("git", "describe", "--always", "--dirty", "--tags").Output()
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(out))
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}
