package cera

import (
	"bytes"
	"strconv"
)

// A font program's FontMatrix may do more than scale: oblique faces made
// from upright ones (Helvetica-BlackOblique, HelveticaNeue-MediumCondObl
// in borb 0374.pdf) carry their slant in it, [0.001 0 0.000176 0.001 0 0].
// go-opentype takes the units per em from it and drops the rest, so such
// glyphs came out upright. fontShear reads the matrix of a bare CFF or a
// Type 1 program, and the outlines are transformed by it.

// fontShear returns the linear part of the FontMatrix of a font program
// (FontFile, or a bare non-CID CFF in FontFile3) when it is more than a
// scale, with ok false otherwise. The matrix maps font units to em.
func fontShear(key string, data []byte) (m [4]float64, ok bool) {
	var fm [6]float64
	switch key {
	case "FontFile":
		fm, ok = type1Matrix(data)
	case "FontFile3":
		fm, ok = cffTopMatrix(data)
	}
	if !ok || (fm[1] == 0 && fm[2] == 0) {
		return m, false
	}
	if !plausibleScale(fm[0]) || !plausibleScale(fm[3]) ||
		!plausibleShear(fm[1]/fm[3]) || !plausibleShear(fm[2]/fm[0]) {
		return m, false
	}
	return [4]float64{fm[0], fm[1], fm[2], fm[3]}, true
}

// plausibleShear bounds a slant to what a face may have (under 60°).
func plausibleShear(v float64) bool { return v > -2 && v < 2 }

// cffTopMatrix reads the Top DICT FontMatrix of a bare CFF program that is
// not CID-keyed (those are cffFDScales's).
func cffTopMatrix(b []byte) ([6]float64, bool) {
	if len(b) < 4 || b[0] != 1 {
		return [6]float64{}, false
	}
	_, p, ok := cffIndex(b, int(b[2])) // Name INDEX
	if !ok {
		return [6]float64{}, false
	}
	tops, _, ok := cffIndex(b, p)
	if !ok || len(tops) == 0 {
		return [6]float64{}, false
	}
	top := cffDict(tops[0])
	if _, cid := top[1230]; cid { // ROS
		return [6]float64{}, false
	}
	return cffMatrix(top)
}

// type1Matrix reads /FontMatrix from the clear-text part of a Type 1
// program (PFA, or PFB with its segment headers): six numbers in brackets
// or braces.
func type1Matrix(b []byte) ([6]float64, bool) {
	if len(b) > 6 && b[0] == 0x80 && b[1] == 1 {
		n := int(b[2]) | int(b[3])<<8 | int(b[4])<<16 | int(b[5])<<24
		b = b[6:]
		if n >= 0 && n < len(b) {
			b = b[:n]
		}
	}
	if i := bytes.Index(b, []byte("eexec")); i >= 0 {
		b = b[:i]
	}
	i := bytes.Index(b, []byte("/FontMatrix"))
	if i < 0 {
		return [6]float64{}, false
	}
	b = b[i+len("/FontMatrix"):]
	open := bytes.IndexAny(b, "[{")
	if open < 0 || open > 16 {
		return [6]float64{}, false
	}
	b = b[open+1:]
	end := bytes.IndexAny(b, "]}")
	if end < 0 {
		return [6]float64{}, false
	}
	f := bytes.Fields(b[:end])
	if len(f) != 6 {
		return [6]float64{}, false
	}
	var m [6]float64
	for k, s := range f {
		v, err := strconv.ParseFloat(string(s), 64)
		if err != nil {
			return [6]float64{}, false
		}
		m[k] = v
	}
	if m[0] == 0 || m[3] == 0 {
		return [6]float64{}, false
	}
	return m, true
}
