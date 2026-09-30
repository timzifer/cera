package corpus

import (
	"archive/zip"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Source is a large external corpus used for robustness runs (scheduled CI,
// not every pull request). Git sources are pinned to a commit; zip sources
// are sharded archives of which one shard is sampled per run.
type Source struct {
	Name string
	Note string
	// Unsafe marks corpora built to crash parsers (fuzzer output,
	// deliberately malformed files). They are only run on demand.
	Unsafe bool

	// Git source: repository, pinned commit and the paths to check out.
	Repo   string
	Commit string
	Paths  []string

	// Zip source: the URL of each shard and the number of shards.
	ShardURL func(n int) string
	Shards   int
}

const digitalCorpora = "https://digitalcorpora.s3.amazonaws.com/corpora/files"

// Sources returns the external corpora.
func Sources() []Source {
	return []Source{
		{
			Name: "pdfjs", Note: "pdf.js test suite: every PDF committed to test/pdfs (regression files from bug reports)",
			Repo: "https://github.com/mozilla/pdf.js", Commit: "18e8a26a3813a319b38c806076f0b0ef9baf1bf4", Paths: []string{"test/pdfs"},
		},
		{
			Name: "borb", Note: "borb-pdf-corpus: ~630 real-world documents, 1999–2025 (MIT)",
			Repo: "https://github.com/borb-pdf/borb-pdf-corpus", Commit: "934b44ce50c5367c1ad1880f3be5c36164f4e7c6", Paths: []string{"pdf"},
		},
		{
			Name: "verapdf", Note: "veraPDF corpus: PDF/A and PDF/UA conformance test files, one feature each",
			Repo: "https://github.com/veraPDF/veraPDF-corpus", Commit: "bb75f4f0073d9350dfd058c0162a367e6fadf25e",
		},
		{
			Name: "horrors", Note: "Open Preservation Foundation format corpus: pdfCabinetOfHorrors (broken and odd files)",
			Repo: "https://github.com/openpreserve/format-corpus", Commit: "366f068cec399d0cdfd61fa473de3ab6dc858098", Paths: []string{"pdfCabinetOfHorrors"},
		},
		{
			Name: "pdf20", Note: "PDF Association PDF 2.0 examples",
			Repo: "https://github.com/pdf-association/pdf20examples", Commit: "c20f2c17bfcc4baab7cfe62e70fae64caf14d5fa",
		},
		{
			Name: "qpdf", Note: "qpdf test suite: damaged xref tables, object streams, encryption",
			Repo: "https://github.com/qpdf/qpdf", Commit: "4eba95899886e851cc41d76886483b347612f2a8", Paths: []string{"qpdf/qtest/qpdf"},
		},
		{
			Name: "ccmain", Note: "digitalcorpora CC-MAIN-2021-31-PDF-UNTRUNCATED: 7.9 M web PDFs in 7 933 zips of ~1 000 (1–2.8 GB each)",
			Shards: 7933, ShardURL: func(n int) string {
				// Zips are grouped in directories of a thousand ("3000-3999").
				g := n / 1000 * 1000
				return fmt.Sprintf("%s/CC-MAIN-2021-31-PDF-UNTRUNCATED/zipfiles/%04d-%04d/%04d.zip", digitalCorpora, g, g+999, n)
			},
		},
		{
			Name: "unsafe", Note: "digitalcorpora CC-MAIN-2021-31-UNSAFE corpora-pdf: fuzzer output and deliberately malformed PDFs (SafeDocs)",
			Shards: 10, Unsafe: true, ShardURL: func(n int) string {
				return fmt.Sprintf("%s/CC-MAIN-2021-31-UNSAFE/corpora-pdf/corpora-pdf-x%03d.zip", digitalCorpora, n)
			},
		},
	}
}

// LookupSource returns the source with the given name.
func LookupSource(name string) (Source, bool) {
	for _, s := range Sources() {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

// Get fetches source s into dir. For zip sources, shard selects the archive
// (-1 picks one from seed) and sample limits the number of PDFs extracted
// (0 = all), chosen from seed as well, so a run can be reproduced.
func (s Source) Get(dir string, shard, sample int, seed uint64, log io.Writer) error {
	if s.Repo != "" {
		return s.getGit(dir, log)
	}
	r := rand.New(rand.NewPCG(seed, 0x6365726120))
	if shard < 0 {
		shard = r.IntN(s.Shards)
	}
	if shard >= s.Shards {
		return fmt.Errorf("%s has %d shards", s.Name, s.Shards)
	}
	return s.getZip(dir, s.ShardURL(shard), sample, r, log)
}

func (s Source) getGit(dir string, log io.Writer) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = log, log
		return cmd.Run()
	}
	steps := [][]string{{"init", "-q"}}
	if _, err := os.Stat(filepath.Join(dir, ".git", "config")); err == nil {
		steps = nil
	}
	steps = append(steps,
		[]string{"remote", "add", "origin", s.Repo},
	)
	if len(s.Paths) > 0 {
		steps = append(steps, append([]string{"sparse-checkout", "set", "--no-cone"}, s.Paths...))
	}
	steps = append(steps,
		[]string{"fetch", "-q", "--depth", "1", "--filter=blob:none", "origin", s.Commit},
		[]string{"checkout", "-q", "--detach", s.Commit},
	)
	for _, a := range steps {
		if err := run(a...); err != nil && a[0] != "remote" {
			return fmt.Errorf("git %s: %w", strings.Join(a, " "), err)
		}
	}
	fmt.Fprintf(log, "%s at %s\n", s.Repo, s.Commit[:12])
	return nil
}

// maxEntry bounds one extracted file, against decompression bombs.
const maxEntry = 512 << 20

func (s Source) getZip(dir, url string, sample int, r *rand.Rand, log io.Writer) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "cera-corpus-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	fmt.Fprintf(log, "downloading %s\n", url)
	t0 := time.Now()
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %s", url, resp.Status)
	}
	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "%d MB in %v\n", n>>20, time.Since(t0).Round(time.Second))

	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return err
	}
	var pdfs []*zip.File
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && f.UncompressedSize64 <= maxEntry {
			pdfs = append(pdfs, f)
		}
	}
	if sample > 0 && sample < len(pdfs) {
		r.Shuffle(len(pdfs), func(i, j int) { pdfs[i], pdfs[j] = pdfs[j], pdfs[i] })
		pdfs = pdfs[:sample]
	}
	for _, f := range pdfs {
		name := path.Base(f.Name)
		if !strings.EqualFold(path.Ext(name), ".pdf") {
			name += ".pdf" // fuzzer corpora often have no extension
		}
		if err := extract(f, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	fmt.Fprintf(log, "extracted %d of %d files\n", len(pdfs), len(zr.File))
	return nil
}

func extract(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(rc, maxEntry)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
