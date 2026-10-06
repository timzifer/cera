//go:build race

package pdf

// The race detector makes sync.Pool drop items, so pooled values are
// allocated again.
const raceEnabled = true
