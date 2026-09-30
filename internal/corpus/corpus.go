// Package corpus fetches the public test corpus and writes synthetic
// technical-drawing pages.
//
// The corpus is not committed: manifest.json lists each file with its source
// (pdf.js test suite and arXiv papers at pinned revisions, shared with the
// stilus harness) and its SHA-256. Fetch downloads what is missing and
// refuses files whose content changed. Customer drawings stay local and are
// passed to the tools as a directory of their own.
package corpus

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed manifest.json
var manifest []byte

// Entry is one corpus file.
type Entry struct {
	Name     string `json:"name"`     // path below the corpus directory
	Category string `json:"category"` // text, transparency, shading, image, scan, vector, paper
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
}

// Manifest returns the corpus entries.
func Manifest() ([]Entry, error) {
	var e []Entry
	return e, json.Unmarshal(manifest, &e)
}

// Categories maps corpus-relative file names to their category.
func Categories() map[string]string {
	m := map[string]string{}
	entries, _ := Manifest()
	for _, e := range entries {
		m[e.Name] = e.Category
	}
	return m
}

// Fetch downloads missing entries into dir and verifies all of them. log
// receives one line per file.
func Fetch(dir string, log io.Writer) error {
	entries, err := Manifest()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	var failed int
	for _, e := range entries {
		path := filepath.Join(dir, filepath.FromSlash(e.Name))
		data, err := os.ReadFile(path)
		if err != nil {
			if data, err = download(client, e.URL); err != nil {
				fmt.Fprintf(log, "FAIL %s: %v\n", e.Name, err)
				failed++
				continue
			}
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != e.SHA256 {
			fmt.Fprintf(log, "FAIL %s: sha256 %s, manifest %s\n", e.Name, got, e.SHA256)
			failed++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(log, "ok   %s (%d KB)\n", e.Name, len(data)>>10)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files failed", failed, len(entries))
	}
	return nil
}

func download(c *http.Client, url string) ([]byte, error) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "cera-corpus/1 (test corpus)")
		resp, err := c.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("HTTP %s", resp.Status)
			continue
		}
		if err != nil {
			lastErr = err
			continue
		}
		return data, nil
	}
	return nil, lastErr
}
