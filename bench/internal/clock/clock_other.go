//go:build !windows

package clock

func now() int64 { return portable() }
