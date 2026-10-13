package system

// platformDirs returns no directories: a page in a browser has no fonts
// of the machine to read.
func platformDirs() []string { return nil }
