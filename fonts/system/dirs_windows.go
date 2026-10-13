package system

import (
	"os"
	"path/filepath"
)

// platformDirs returns the font directories of Windows: the system's,
// and the user's, where fonts installed for one account are.
func platformDirs() []string {
	var dirs []string
	if root := os.Getenv("WINDIR"); root != "" {
		dirs = append(dirs, filepath.Join(root, "Fonts"))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		dirs = append(dirs, filepath.Join(local, "Microsoft", "Windows", "Fonts"))
	}
	return dirs
}
