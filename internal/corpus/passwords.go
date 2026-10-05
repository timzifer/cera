package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// The pdf.js test suite gives the password of each encrypted file it
// opens in test/test_manifest.json ({"file": "pdfs/x.pdf", "password":
// "…"}, the file relative to the manifest). The pdfjs source checks it
// out next to test/pdfs, and Password finds it from a file's path.

var (
	pwMu    sync.Mutex
	pwCache = map[string]map[string]string{} // manifest path → file → password
)

// Password returns the password a pdf.js manifest gives for the PDF at
// path: the manifest is looked for in the file's directory and the two
// above it. ok is false when none names the file.
func Password(path string) (password string, ok bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	dir := filepath.Dir(abs)
	for range 3 {
		m := filepath.Join(dir, "test_manifest.json")
		if pw := manifestPasswords(m); pw != nil {
			if rel, err := filepath.Rel(dir, abs); err == nil {
				if p, ok := pw[filepath.ToSlash(rel)]; ok {
					return p, true
				}
			}
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return "", false
}

// manifestPasswords reads the passwords of a pdf.js manifest, nil if there
// is none at path or it names no password.
func manifestPasswords(path string) map[string]string {
	pwMu.Lock()
	defer pwMu.Unlock()
	if pw, ok := pwCache[path]; ok {
		return pw
	}
	var pw map[string]string
	if data, err := os.ReadFile(path); err == nil {
		var entries []struct {
			File     string  `json:"file"`
			Password *string `json:"password"`
		}
		if json.Unmarshal(data, &entries) == nil {
			for _, e := range entries {
				if e.Password != nil {
					if pw == nil {
						pw = map[string]string{}
					}
					pw[filepath.ToSlash(filepath.Clean(e.File))] = *e.Password
				}
			}
		}
	}
	pwCache[path] = pw
	return pw
}
