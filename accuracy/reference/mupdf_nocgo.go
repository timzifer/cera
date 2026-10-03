//go:build !cgo

package main

// Without cgo, MuPDF runs as its command-line tool, if installed.
func newMuPDF() (engine, error) { return newExecMuPDF() }
