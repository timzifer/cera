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
// The profile is read by cmyk.Parse, as cera reads profiles at run time;
// the black point is found as Little CMS finds it for the relative
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
	"flag"
	"fmt"
	"log"
	"math"
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
	p, err := cmyk.Parse(data)
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
	L := 116*math.Cbrt(p.Black[1]) - 16
	fmt.Printf("wrote %s (%d bytes, %d nodes per ink, black point L* %.2f)\n", *out, len(b), p.A2B.(*cmyk.Lut).Grid, L)
}
