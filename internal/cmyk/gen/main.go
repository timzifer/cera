// Command gen writes internal/cmyk/swop.bin, the profile cera converts
// DeviceCMYK with: the colorimetric table (A2B1) of a CMYK press profile
// and its black point.
//
// The profile is the one colord ships for CGATS TR 005 (SWOP, coated #5
// paper), SWOP_TR005_coated_5.icc, from the colord-data package or built
// from https://github.com/hughsie/colord (data/profiles). Its
// characterization data come from CGATS TR 005 (NPES): "Profiles, or
// other derivative work, based on these data may be distributed with no
// further permissions from CGATS. However, this Technical Report must be
// identified as the source of the characterization data." colord's
// profile itself is CC0.
//
//	go run ./internal/cmyk/gen -profile /usr/share/color/icc/colord/SWOP_TR005_coated_5.icc
//
// The black point is found as Little CMS finds it for the relative
// colorimetric intent of a CMYK output profile: Lab black through the
// perceptual table into ink (B2A0) and back colorimetrically (A2B1), L*
// at most 50, made neutral.
//
// With -eval, gen converts CMYK percentages read from stdin, one colour
// per line, to sRGB components (0..255) through the profile as written,
// to compare with transicc -t 1 -b of the same profile to sRGB.
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/timzifer/cera/internal/cmyk"
)

func main() {
	profile := flag.String("profile", "/usr/share/color/icc/colord/SWOP_TR005_coated_5.icc", "CMYK press profile (ICC v2, lut16 tables, Lab connection space)")
	out := flag.String("out", filepath.Join("internal", "cmyk", "swop.bin"), "table written")
	eval := flag.Bool("eval", false, "convert CMYK percentages from stdin to sRGB instead")
	flag.Parse()
	data, err := os.ReadFile(*profile)
	if err != nil {
		log.Fatal(err)
	}
	p, L, err := parse(data)
	if err != nil {
		log.Fatal(err)
	}
	if *eval {
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			var c [4]float64
			if _, err := fmt.Sscan(in.Text(), &c[0], &c[1], &c[2], &c[3]); err != nil {
				continue
			}
			for i := range c {
				c[i] /= 100
			}
			r, g, b := p.RGB(c)
			fmt.Printf("%.4f %.4f %.4f\n", 255*r, 255*g, 255*b)
		}
		return
	}
	b, err := p.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s (%d bytes, %d nodes per ink, black point L* %.2f)\n", *out, len(b), p.A2B.Grid, L)
}

// parse reads the tables of a CMYK profile and works out its black point
// (returned as L* too).
func parse(b []byte) (*cmyk.Profile, float64, error) {
	if len(b) < 132 || string(b[16:20]) != "CMYK" || string(b[20:24]) != "Lab " {
		return nil, 0, errors.New("not a CMYK profile with a Lab connection space")
	}
	tags := map[string][]byte{}
	n := int(binary.BigEndian.Uint32(b[128:]))
	for i := range n {
		e := 132 + 12*i
		if e+12 > len(b) {
			return nil, 0, errors.New("truncated tag table")
		}
		off := int(binary.BigEndian.Uint32(b[e+4:]))
		size := int(binary.BigEndian.Uint32(b[e+8:]))
		if off < 0 || size < 0 || off+size > len(b) {
			return nil, 0, errors.New("tag out of range")
		}
		tags[string(b[e:e+4])] = b[off : off+size]
	}
	a2b1, err := readLut16(tags["A2B1"])
	if err != nil {
		return nil, 0, fmt.Errorf("A2B1: %w", err)
	}
	b2a0, err := readLut16(tags["B2A0"])
	if err != nil {
		return nil, 0, fmt.Errorf("B2A0: %w", err)
	}
	if a2b1.In != 4 || a2b1.Out != 3 || b2a0.In != 3 || b2a0.Out != 4 {
		return nil, 0, errors.New("tables of the wrong shape")
	}
	black := cmyk.EncodeLab(0, 0, 0)
	var ink [4]float64
	b2a0.Eval(black[:], ink[:])
	var lab [3]float64
	a2b1.Eval(ink[:], lab[:])
	L, _, _ := cmyk.DecodeLab(lab[:])
	L = min(L, 50)
	return &cmyk.Profile{A2B: a2b1, Black: cmyk.LabToXYZ(L, 0, 0)}, L, nil
}

// readLut16 reads an ICC lut16Type (mft2).
func readLut16(b []byte) (*cmyk.Lut, error) {
	if len(b) < 52 || string(b[:4]) != "mft2" {
		return nil, errors.New("not a lut16Type")
	}
	t := &cmyk.Lut{In: int(b[8]), Out: int(b[9]), Grid: int(b[10])}
	nIn := int(binary.BigEndian.Uint16(b[48:]))
	nOut := int(binary.BigEndian.Uint16(b[50:]))
	if t.In < 3 || t.In > 4 || t.Out < 3 || t.Out > 4 || t.Grid < 2 || nIn < 2 || nOut < 2 {
		return nil, errors.New("unsupported lut16Type")
	}
	p := 52
	get := func(k int) ([]float64, error) {
		if p+2*k > len(b) {
			return nil, errors.New("truncated lut16Type")
		}
		v := make([]float64, k)
		for i := range v {
			v[i] = float64(binary.BigEndian.Uint16(b[p+2*i:])) / 65535
		}
		p += 2 * k
		return v, nil
	}
	for range t.In {
		c, err := get(nIn)
		if err != nil {
			return nil, err
		}
		t.InCurves = append(t.InCurves, c)
	}
	size := t.Out
	for range t.In {
		size *= t.Grid
	}
	var err error
	if t.Table, err = get(size); err != nil {
		return nil, err
	}
	for range t.Out {
		c, err := get(nOut)
		if err != nil {
			return nil, err
		}
		t.OutCurves = append(t.OutCurves, c)
	}
	return t, nil
}
