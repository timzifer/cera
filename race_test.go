//go:build race

package cera

// The race detector makes sync.Pool drop items, so pooled buffers are
// allocated again.
const raceEnabled = true
