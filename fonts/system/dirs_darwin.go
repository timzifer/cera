package system

import (
	"os"
	"path/filepath"
)

// platformDirs returns the font directories of macOS: the system's, the
// machine's and the user's.
func platformDirs() []string {
	dirs := []string{"/System/Library/Fonts", "/Library/Fonts"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Library", "Fonts"))
	}
	return dirs
}
