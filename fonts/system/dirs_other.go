//go:build !windows && !darwin && !js

package system

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// fontconfigDir matches a directory in a fontconfig configuration.
var fontconfigDir = regexp.MustCompile(`<dir(?:\s+prefix="(\w+)")?\s*>([^<]+)</dir>`)

// platformDirs returns the font directories fontconfig reads: those its
// configuration names, and the ones it has by default.
func platformDirs() []string {
	home, _ := os.UserHomeDir()
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" && home != "" {
		data = filepath.Join(home, ".local", "share")
	}
	dirs := []string{"/usr/share/fonts", "/usr/local/share/fonts"}
	if data != "" {
		dirs = append(dirs, filepath.Join(data, "fonts"))
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".fonts"))
	}
	if conf, err := os.ReadFile("/etc/fonts/fonts.conf"); err == nil {
		for _, m := range fontconfigDir.FindAllSubmatch(conf, 64) {
			dir := strings.TrimSpace(string(m[2]))
			switch {
			case string(m[1]) == "xdg" && data != "":
				dir = filepath.Join(data, dir)
			case strings.HasPrefix(dir, "~") && home != "":
				dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
			case !filepath.IsAbs(dir):
				continue
			}
			dirs = append(dirs, dir)
		}
	}
	// A directory named twice is read once.
	seen := map[string]bool{}
	out := dirs[:0]
	for _, d := range dirs {
		if d = filepath.Clean(d); !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
