//go:build !cgo

package main

// Without cgo, MuPDF runs as its command-line tool, if installed. mutool
// draw draws annotations whatever annots says.
func newMuPDF(annots bool) (engine, error) { return newExecMuPDF(annots) }
