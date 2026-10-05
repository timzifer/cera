package corpus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPassword(t *testing.T) {
	dir := t.TempDir()
	pdfs := filepath.Join(dir, "test", "pdfs")
	if err := os.MkdirAll(pdfs, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `[{"id": "a", "file": "pdfs/a.pdf", "password": "pässwört"},
		{"id": "b", "file": "pdfs/b.pdf"}, {"id": "e", "file": "pdfs/e.pdf", "password": ""}]`
	if err := os.WriteFile(filepath.Join(dir, "test", "test_manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		file, want string
		ok         bool
	}{
		{"a.pdf", "pässwört", true},
		{"b.pdf", "", false},
		{"e.pdf", "", true},
		{"c.pdf", "", false},
	} {
		got, ok := Password(filepath.Join(pdfs, c.file))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: %q %v, want %q %v", c.file, got, ok, c.want, c.ok)
		}
	}
	if _, ok := Password(filepath.Join(t.TempDir(), "x.pdf")); ok {
		t.Error("a password without a manifest")
	}
}
