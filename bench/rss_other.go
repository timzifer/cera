//go:build !windows && !linux

package main

// peakRSS is not measured here.
func peakRSS(int) uint64 { return 0 }
