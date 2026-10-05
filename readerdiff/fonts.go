package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-pdfkit/pdffont"
	"github.com/go-pdfkit/reader"
	"github.com/timzifer/cera/internal/corpus"
	"github.com/timzifer/cera/internal/pdf"
	newfont "github.com/timzifer/cera/internal/pdffont"
)

// runFonts compares go-pdfkit/pdffont v0.3.1 with internal/pdffont on every
// font the pages of every file use, directly or through form XObjects.
func runFonts(dirs, allowPath string, max int) error {
	allow, err := readAllowlist(allowPath)
	if err != nil {
		return err
	}
	files, err := pdfFiles(dirs)
	if err != nil {
		return err
	}
	var failed, allowed, fonts, entries int
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		pw, _ := corpus.Password(f)
		start := time.Now()
		s, n := safeFontSnapshot(data, pw)
		if el := time.Since(start); el > 5*time.Second {
			fmt.Fprintln(os.Stderr, "slow:", filepath.ToSlash(f), el)
		}
		fonts += n
		entries += len(s)
		var bad []string
		for _, d := range s {
			if allow.reason(f, d.key) != "" {
				allowed++
				continue
			}
			bad = append(bad, fmt.Sprintf("  %s\n    v031: %s\n    new:  %s", d.key, clip(d.a), clip(d.b)))
		}
		if len(bad) == 0 {
			continue
		}
		failed++
		fmt.Printf("%s: %d differences\n", filepath.ToSlash(f), len(bad))
		for i, b := range bad {
			if i == max {
				fmt.Println("  …")
				break
			}
			fmt.Println(b)
		}
	}
	fmt.Printf("%d files, %d fonts, %d files differ, %d differences allowed\n", len(files), fonts, failed, allowed)
	if failed > 0 {
		return fmt.Errorf("%d files differ", failed)
	}
	return nil
}

func safeFontSnapshot(data []byte, pw string) (ds []diff, n int) {
	defer func() {
		if r := recover(); r != nil {
			ds = []diff{{"panic", "", fmt.Sprint(r)}}
		}
	}()
	return fontDiffs(data, pw)
}

// fontDiffs reads every font of a file with both packages and lists where
// they disagree.
func fontDiffs(data []byte, pw string) ([]diff, int) {
	vd, err1 := reader.OpenWithPassword(data, pw)
	nd, err2 := pdf.OpenWithPassword(data, pw)
	if err1 != nil || err2 != nil {
		return nil, 0
	}
	refs := fontRefs(vd)
	var ds []diff
	for _, r := range refs {
		vo, _ := vd.Get(r)
		vdict, ok := reader.ToDict(vo)
		if !ok {
			continue
		}
		no, _ := nd.Get(pdf.Ref{Num: int32(r.Num), Gen: int32(r.Gen)})
		ndict, _ := no.Dict()
		a := pdffont.Read(vd, vdict)
		b := newfont.Read(nd, ndict)
		key := fmt.Sprintf("font/%d", r.Num)
		// Names and text of a font that names a Mac encoding differ by
		// design: v0.3.1 read them as StandardEncoding.
		mac := macEncoding(vd, vdict)
		adler := adlerOnly(vd, vdict, nd, ndict)
		cmp := func(what, x, y string) {
			if x == y {
				return
			}
			k := key + "/" + what
			if mac != "" && (strings.HasSuffix(what, "/text") || strings.HasSuffix(what, "/name")) {
				k = mac + "/" + k
			}
			if adler {
				k = "adler/" + k
			}
			ds = append(ds, diff{k, x, y})
		}
		cmp("kind", fmt.Sprint(int(a.Kind())), fmt.Sprint(int(b.Kind())))
		cmp("symbolic", fmt.Sprint(a.Symbolic()), fmt.Sprint(b.Symbolic()))
		cmp("matrix", fmt.Sprint(a.FontMatrix()), fmt.Sprint(b.FontMatrix()))
		ka, da, oka := a.Program()
		kb, db, okb := b.Program()
		cmp("program", fmt.Sprint(ka, oka, len(da), hash(da)), fmt.Sprint(kb, okb, len(db), hash(db)))
		for _, c := range codesOf(vd, vdict, a) {
			ta, oa := a.Text(c)
			tb, ob := b.Text(c)
			cmp(fmt.Sprintf("code/%d/text", c), fmt.Sprintf("%q %v", ta, oa), fmt.Sprintf("%q %v", tb, ob))
			cmp(fmt.Sprintf("code/%d/width", c), fmt.Sprint(a.Width(c), a.HasWidth(c)), fmt.Sprint(b.Width(c), b.HasWidth(c)))
			na, oa := a.GlyphName(c)
			nb, ob := b.GlyphName(c)
			cmp(fmt.Sprintf("code/%d/name", c), fmt.Sprintf("%s %v %v", na, oa, a.Chosen(c)), fmt.Sprintf("%s %v %v", nb, ob, b.Chosen(c)))
			if a.Kind() == pdffont.Composite {
				ga, oa := a.CIDToGID(c)
				gb, ob := b.CIDToGID(c)
				cmp(fmt.Sprintf("code/%d/gid", c), fmt.Sprint(ga, oa), fmt.Sprint(gb, ob))
			}
		}
	}
	return ds, len(refs)
}

// adlerOnly reports a font one of whose streams (program, ToUnicode map,
// CIDToGIDMap) v0.6 refuses to decode and internal/pdf decodes: a Flate
// stream whose only fault is its Adler-32 checksum.
func adlerOnly(vd *reader.Document, vdict reader.Dict, nd *pdf.Document, ndict pdf.Dict) bool {
	type pair struct {
		v reader.Object
		n pdf.Object
	}
	var ps []pair
	ps = append(ps, pair{vdict.Get("ToUnicode"), ndict.Get("ToUnicode")})
	vdesc, ndesc := vdict, ndict
	if arr, ok := reader.ToArray(mustResolve(vd, vdict.Get("DescendantFonts"))); ok && len(arr) > 0 {
		if kid, ok := reader.ToDict(mustResolve(vd, arr[0])); ok {
			vdesc = kid
			ps = append(ps, pair{kid.Get("CIDToGIDMap"), pdf.Null})
		}
	}
	if arr, ok := nd.Resolve(ndict.Get("DescendantFonts")).Array(); ok && len(arr) > 0 {
		if kid, ok := nd.Resolve(arr[0]).Dict(); ok {
			ndesc = kid
			if len(ps) == 2 {
				ps[1].n = kid.Get("CIDToGIDMap")
			}
		}
	}
	vfd, _ := reader.ToDict(mustResolve(vd, vdesc.Get("FontDescriptor")))
	nfd, _ := nd.Resolve(ndesc.Get("FontDescriptor")).Dict()
	for _, k := range []string{"FontFile", "FontFile2", "FontFile3"} {
		ps = append(ps, pair{vfd.Get(reader.Name(k)), nfd.Get(pdf.Name(k))})
	}
	for _, p := range ps {
		vs, ok1 := reader.ToStream(mustResolve(vd, p.v))
		ns, ok2 := nd.Resolve(p.n).Stream()
		if !ok1 || !ok2 {
			continue
		}
		_, _, verr := vd.DecodeStream(vs)
		if verr != nil && !nd.DecodeUncached(ns).Recovered {
			return true
		}
	}
	return false
}

// macEncoding names the Mac encoding a font's /Encoding names, directly or
// as its /BaseEncoding: "macroman" or "macexpert", or "".
func macEncoding(d *reader.Document, dict reader.Dict) string {
	enc := mustResolve(d, dict.Get("Encoding"))
	if ed, ok := reader.ToDict(enc); ok {
		enc = mustResolve(d, ed.Get("BaseEncoding"))
	}
	switch n, _ := reader.ToName(enc); n {
	case "MacRomanEncoding":
		return "macroman"
	case "MacExpertEncoding":
		return "macexpert"
	}
	return ""
}

// fontRefs finds the fonts the pages use, through their resources and
// those of the form XObjects they draw.
func fontRefs(d *reader.Document) []reader.Ref {
	seen := map[reader.Ref]bool{}
	res := map[reader.Ref]bool{}
	var out []reader.Ref
	var walkRes func(o reader.Object, depth int)
	walkRes = func(o reader.Object, depth int) {
		if depth > 8 {
			return
		}
		if r, ok := o.(reader.Ref); ok {
			if res[r] {
				return
			}
			res[r] = true
		}
		rv, _ := d.Resolve(o)
		rd, ok := reader.ToDict(rv)
		if !ok {
			return
		}
		fv, _ := d.Resolve(rd.Get("Font"))
		if fonts, ok := reader.ToDict(fv); ok {
			for _, f := range fonts {
				if r, ok := f.(reader.Ref); ok && !seen[r] {
					seen[r] = true
					out = append(out, r)
				}
			}
		}
		xv, _ := d.Resolve(rd.Get("XObject"))
		if xs, ok := reader.ToDict(xv); ok {
			for _, x := range xs {
				xo, _ := d.Resolve(x)
				if s, ok := reader.ToStream(xo); ok {
					if st, _ := reader.ToName(s.Dict.Get("Subtype")); st == "Form" {
						walkRes(s.Dict.Get("Resources"), depth+1)
					}
				}
			}
		}
	}
	for i := range d.PageCount() {
		if p, err := d.Page(i + 1); err == nil {
			walkRes(p.Get("Resources"), 0)
		}
	}
	slices.SortFunc(out, func(a, b reader.Ref) int { return a.Num - b.Num })
	return out
}

// codesOf lists the codes worth comparing: every byte for a simple font;
// for a composite one the identifiers its widths and ToUnicode map name,
// up to a few thousand, and the first bytes' worth.
func codesOf(d *reader.Document, dict reader.Dict, f *pdffont.Font) []int {
	var out []int
	for c := range 256 {
		out = append(out, c)
	}
	if f.Kind() != pdffont.Composite {
		return out
	}
	set := map[int]bool{}
	if tu, _ := d.Resolve(dict.Get("ToUnicode")); tu != nil {
		if s, ok := reader.ToStream(tu); ok {
			if data, _, err := d.DecodeStream(s); err == nil {
				for c := range pdffont.ReadToUnicode(data) {
					set[c] = true
				}
			}
		}
	}
	if arr, ok := reader.ToArray(mustResolve(d, dict.Get("DescendantFonts"))); ok && len(arr) > 0 {
		if kid, ok := reader.ToDict(mustResolve(d, arr[0])); ok {
			if w, ok := reader.ToArray(mustResolve(d, kid.Get("W"))); ok {
				for _, e := range w {
					if n, ok := reader.ToInt(mustResolve(d, e)); ok && n >= 0 && n < 1<<20 {
						set[int(n)] = true
						set[int(n)+1] = true
					}
				}
			}
		}
	}
	for c := range set {
		if c >= 256 {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	if len(out) > 4096 {
		out = out[:4096]
	}
	return out
}

func mustResolve(d *reader.Document, o reader.Object) reader.Object {
	v, _ := d.Resolve(o)
	return v
}
