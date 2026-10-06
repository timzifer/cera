package pdffont

import (
	"github.com/timzifer/cera/internal/pdf"
)

// readSimple reads a font addressed one byte at a time.
func (f *Font) readSimple() {
	f.descriptor, _ = f.doc.GetDict(f.dict, "FontDescriptor")
	f.symbolic = symbolicFlag(f.doc, f.descriptor)
	f.readEncoding()
	f.readSimpleWidths()
}

// readType3 reads a font whose glyphs are drawings.
func (f *Font) readType3() {
	f.descriptor, _ = f.doc.GetDict(f.dict, "FontDescriptor")
	f.readEncoding()
	f.charProcs, _ = f.doc.GetDict(f.dict, "CharProcs")
	f.t3Res, _ = f.doc.GetDict(f.dict, "Resources")
	if m, ok := floatArray(f.doc, f.dict.Get("FontMatrix")); ok && len(m) >= 6 {
		copy(f.fontMatrix[:], m[:6])
	}
	f.readSimpleWidths()
	// A Type 3 font's widths are in its own glyph space, which the font
	// matrix maps onto text space.
	for c := range f.widths {
		if f.hasWidth.has(c) {
			f.widths[c] = f.widths[c] * 1000 * f.fontMatrix[0]
		}
	}
	f.defaultW = 0
}

// symbolicFlag reads whether a font says it is addressed through its own
// character map: the symbolic flag set and the nonsymbolic one not.
func symbolicFlag(d *pdf.Document, descriptor pdf.Dict) bool {
	flags, ok := d.Resolve(descriptor.Get("Flags")).Int()
	if !ok {
		return false
	}
	const symbolic, nonSymbolic = 1 << 2, 1 << 5
	return flags&symbolic != 0 && flags&nonSymbolic == 0
}

// readEncoding works out what each code is called: a base table, and the
// differences the font names on top of it.
func (f *Font) readEncoding() {
	base := StandardEncoding
	enc := f.doc.Resolve(f.dict.Get("Encoding"))
	if name, ok := enc.Name(); ok {
		table := NamedEncoding(name, base)
		f.fill(table)
		// A named encoding is the document's own word on what its codes
		// are, symbolic font or not.
		for c, n := range table {
			if n != "" {
				f.chosen.set(c)
			}
		}
		return
	}
	encDict, ok := enc.Dict()
	if !ok {
		f.fill(base)
		return
	}
	if name, ok := f.doc.Resolve(encDict.Get("BaseEncoding")).Name(); ok {
		base = NamedEncoding(name, base)
	}
	f.fill(base)
	f.applyDifferences(encDict)
}

// NamedEncoding is the table a name stands for, or the fallback for a name
// that is not one of them.
func NamedEncoding(name pdf.Name, fallback [256]string) [256]string {
	switch name {
	case "WinAnsiEncoding":
		return WinAnsiEncoding
	case "StandardEncoding":
		return StandardEncoding
	case "MacRomanEncoding":
		return MacRomanEncoding
	case "MacExpertEncoding":
		return MacExpertEncoding
	}
	return fallback
}

// fill names every code the table names.
func (f *Font) fill(table [256]string) {
	for c, n := range table {
		if n != "" {
			f.names[c] = n
			f.named.set(c)
		}
	}
}

// applyDifferences reads the array that names glyphs a code at a time: a
// number, then the names of the codes from there on.
func (f *Font) applyDifferences(encDict pdf.Dict) {
	arr, ok := f.doc.Resolve(encDict.Get("Differences")).Array()
	if !ok {
		return
	}
	code := 0
	for _, e := range arr {
		v := f.doc.Resolve(e)
		if n, ok := v.Int(); ok {
			code = int(n)
			continue
		}
		if name, ok := v.Name(); ok {
			if code >= 0 && code < 256 {
				f.names[code] = string(name)
				f.named.set(code)
				f.chosen.set(code)
			}
			code++
		}
	}
}

// readSimpleWidths reads /Widths, one width per code from /FirstChar on.
func (f *Font) readSimpleWidths() {
	first, ok := f.doc.Resolve(f.dict.Get("FirstChar")).Int()
	if !ok {
		return
	}
	arr, ok := f.doc.Resolve(f.dict.Get("Widths")).Array()
	if !ok {
		return
	}
	for i, e := range arr {
		c := int(first) + i
		if c < 0 || c > 255 {
			continue
		}
		if v, ok := f.doc.Resolve(e).Float(); ok {
			f.widths[c] = v / 1000
			f.hasWidth.set(c)
		}
	}
	if mw, ok := f.doc.Resolve(f.descriptor.Get("MissingWidth")).Float(); ok && mw >= 0 {
		f.defaultW = mw / 1000
	}
}

// readComposite reads a font addressed by character identifier: its
// descendant holds the widths, the descriptor and the map to glyphs.
func (f *Font) readComposite() {
	f.defaultW = 1
	arr, ok := f.doc.Resolve(f.dict.Get("DescendantFonts")).Array()
	if !ok || len(arr) == 0 {
		return
	}
	kid, ok := f.doc.Resolve(arr[0]).Dict()
	if !ok {
		return
	}
	if v, ok := f.doc.Resolve(kid.Get("DW")).Float(); ok {
		f.defaultW = v / 1000
	}
	f.readCIDWidths(kid)
	f.descriptor, _ = f.doc.GetDict(kid, "FontDescriptor")
	f.symbolic = symbolicFlag(f.doc, f.descriptor)
	if s, ok := f.doc.Resolve(kid.Get("CIDToGIDMap")).Stream(); ok {
		if data, ok := f.decode(s); ok {
			f.cidToGID = data
		}
	}
}

// readCIDWidths reads the /W array, which names widths one identifier at a
// time ("c [w1 w2 …]") or a run at a time ("first last w").
func (f *Font) readCIDWidths(kid pdf.Dict) {
	arr, ok := f.doc.Resolve(kid.Get("W")).Array()
	if !ok {
		return
	}
	var b cidWidthBuilder
	for i := 0; i < len(arr); {
		first, ok := f.doc.Resolve(arr[i]).Int()
		if !ok || i+1 >= len(arr) {
			break
		}
		next := f.doc.Resolve(arr[i+1])
		if list, ok := next.Array(); ok {
			for k, e := range list {
				if v, ok := f.doc.Resolve(e).Float(); ok {
					b.add(first+int64(k), first+int64(k), v/1000)
				}
			}
			i += 2
			continue
		}
		last, ok := next.Int()
		if !ok || i+2 >= len(arr) {
			break
		}
		w, ok := f.doc.Resolve(arr[i+2]).Float()
		if !ok {
			break
		}
		if last >= first && last-first < 1<<20 {
			b.add(first, last, w/1000)
		}
		i += 3
	}
	f.cidWidths = b.build()
}

// floatArray reads an array of numbers; ok is false when it is not one.
func floatArray(d *pdf.Document, o pdf.Object) ([]float64, bool) {
	arr, ok := d.Resolve(o).Array()
	if !ok {
		return nil, false
	}
	out := make([]float64, 0, len(arr))
	for _, e := range arr {
		v, ok := d.Resolve(e).Float()
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}
