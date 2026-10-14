package cera

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"
	"slices"

	"github.com/go-opentype/opentype"
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfedit"
	"github.com/timzifer/cera/internal/pdffont"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// Subsetting embedded fonts (SaveOptions.SubsetFonts). A font embedded
// whole — the file the application had, megabytes for a CJK face — keeps
// only the glyphs the pages show. The pages are run for the glyphs each
// font shows, as the renderer takes them from the codes: in forms,
// patterns, Type 3 glyphs and the appearances of annotations too, all
// layers shown. The glyphs not shown are emptied and keep their numbers,
// so that the encoding, /CIDToGIDMap and /W stay as they are.
//
// A program is subset only where that is safe: TrueType programs and
// CFF programs keyed by name (bare, or in an OpenType file). A program the
// pages could not be read through for, a font of the form (/DR, whose
// fonts a viewer writes typed text with), a font cera draws with a stand-in
// or could not read, and CID-keyed CFF and Type 1 programs stay whole. The
// subset is read back and every glyph used must have the outline it had,
// or the program stays whole.

// glyphMeter collects the glyphs the pages show, by font.
type glyphMeter struct {
	nullDevice
	fonts map[*Font]*shown
}

// shown are the glyphs of a font the pages show, and the codes they show
// them by.
type shown struct {
	gids  map[opentype.GlyphIndex]bool
	codes map[int]bool
}

func (m *glyphMeter) ShowText(run *GlyphRun, _ TextMode) {
	f := run.Font
	if f == nil {
		return
	}
	u := m.fonts[f]
	if u == nil {
		u = &shown{gids: map[opentype.GlyphIndex]bool{}, codes: map[int]bool{}}
		m.fonts[f] = u
	}
	for _, g := range run.Glyphs {
		if g.GID >= 0 && g.GID <= math.MaxUint16 {
			u.gids[opentype.GlyphIndex(g.GID)] = true
		}
		u.codes[g.Code] = true
	}
}

// progKey is a font program, a stream of a document.
type progKey struct {
	r   *pdf.Document
	num int32
}

// progUse is a font program and what the pages show of it.
type progUse struct {
	d     *Document
	key   pdf.Name // FontFile2 or FontFile3
	s     *pdf.Stream
	fonts []*Font
	codes []map[int]bool // of each font, the codes shown
	gids  map[opentype.GlyphIndex]bool
	whole bool
}

// fontPlan subsets the font programs of the pages of doc.
func (e *Editor) fontPlan(doc *pdfedit.Doc) error {
	m := &glyphMeter{fonts: map[*Font]*shown{}}
	unsure := map[*Document]bool{} // pages that did not run to the end
	seen := map[srcObj]bool{}
	for _, ep := range e.pages {
		if ep.blank {
			continue
		}
		pk := srcObj{ep.src.r, int32(ep.index)}
		if seen[pk] {
			continue
		}
		seen[pk] = true
		p, err := ep.src.Page(ep.index)
		if err != nil {
			unsure[ep.src] = true
			continue
		}
		var st Stats
		if err := p.RunWith(context.Background(), m, RunOptions{Scale: 1, Layers: &Visibility{}, Stats: &st}); err != nil {
			return err
		}
		if st.Errors > 0 || len(st.Unsupported) > 0 {
			unsure[ep.src] = true
		}
	}

	progs := map[progKey]*progUse{}
	order := []progKey{}
	for f, sh := range m.fonts {
		d := fontDoc(e, f)
		if d == nil {
			continue
		}
		key, s, ref, ok := programOf(d, f)
		if !ok {
			continue
		}
		k := progKey{d.r, ref.Num}
		u := progs[k]
		if u == nil {
			u = &progUse{d: d, key: key, s: s, gids: map[opentype.GlyphIndex]bool{}}
			progs[k] = u
			order = append(order, k)
		}
		u.fonts = append(u.fonts, f)
		u.codes = append(u.codes, sh.codes)
		for g := range sh.gids {
			u.gids[g] = true
		}
		if f.substituted || f.bad || f.program == nil || f.stdGlyphs || unsure[d] {
			u.whole = true
		}
	}
	// The fonts of the forms stay whole: a viewer writes what is typed
	// into a field with them, any character.
	forms := map[*Document]bool{}
	for _, k := range order {
		d := progs[k].d
		if forms[d] {
			continue
		}
		forms[d] = true
		cat, _ := d.r.Catalog()
		dr := d.dict(d.dict(cat.Get("AcroForm")).Get("DR"))
		for _, o := range d.dict(dr.Get("Font")).All() {
			f := d.font(o)
			if f == nil {
				continue
			}
			if _, _, ref, ok := programOf(d, f); ok {
				if u := progs[progKey{d.r, ref.Num}]; u != nil {
					u.whole = true
				}
			}
		}
	}
	slices.SortFunc(order, func(a, b progKey) int { return int(a.num - b.num) })
	for _, k := range order {
		u := progs[k]
		if u.whole {
			continue
		}
		if err := u.subset(doc, k); err != nil {
			continue // whole, as it was
		}
	}
	return nil
}

// fontDoc returns the document of the pages of e that loaded f.
func fontDoc(e *Editor, f *Font) *Document {
	for _, ep := range e.pages {
		if ep.blank {
			continue
		}
		d := ep.src
		d.fontMu.Lock()
		for _, g := range d.fonts {
			if g == f {
				d.fontMu.Unlock()
				return d
			}
		}
		d.fontMu.Unlock()
	}
	return nil
}

// programOf returns the program stream f embeds, of key FontFile2 or
// FontFile3, and its reference.
func programOf(d *Document, f *Font) (pdf.Name, *pdf.Stream, pdf.Ref, bool) {
	if f.pdf.Kind() == pdffont.Type3 {
		return "", nil, pdf.Ref{}, false
	}
	desc := f.pdf.Descriptor()
	for _, key := range []pdf.Name{"FontFile2", "FontFile3"} {
		o := desc.Get(key)
		ref, ok := o.Ref()
		if !ok {
			continue
		}
		if s, ok := d.resolve(o).Stream(); ok {
			return key, s, ref, true
		}
	}
	return "", nil, pdf.Ref{}, false
}

// subset writes the program of u with the glyphs it shows, and tags the
// fonts that use it as subsets.
func (u *progUse) subset(doc *pdfedit.Doc, k progKey) error {
	d := u.d
	dec := d.r.Decode(u.s)
	if dec.Recovered || dec.Image != "" {
		return errors.New("the program does not decode")
	}
	data := dec.Data
	gids := make([]opentype.GlyphIndex, 0, len(u.gids))
	for g := range u.gids {
		gids = append(gids, g)
	}
	slices.Sort(gids)
	var sub []byte
	var err error
	sfnt := len(data) >= 4 && (binary.BigEndian.Uint32(data) == 0x00010000 || string(data[:4]) == "true" || string(data[:4]) == "OTTO")
	// What a PDF is not drawn with goes: layout and variation tables.
	// A font written top to bottom keeps its substitutions, which have
	// its vertical forms.
	drop := map[string][]byte{}
	for _, tag := range []string{"GSUB", "GPOS", "GDEF", "BASE", "JSTF", "MATH", "kern", "morx", "mort", "feat", "prop", "trak", "meta",
		"fvar", "gvar", "avar", "cvar", "HVAR", "VVAR", "MVAR", "STAT"} {
		drop[tag] = nil
	}
	for _, f := range u.fonts {
		if f.vertical {
			delete(drop, "GSUB")
		}
	}
	switch {
	case sfnt && hasTable(data, "glyf"):
		sub, err = subsetGlyf(data, gids, drop)
	case sfnt && hasTable(data, "CFF "):
		var p *opentype.Font
		if p, err = opentype.Parse(data); err == nil {
			var table []byte
			if table, err = p.SubsetCFF(gids); err == nil {
				drop["CFF "] = table
				sub, err = replaceTables(data, drop)
			}
		}
	case !sfnt && u.key == "FontFile3":
		// A bare program: in the smallest OpenType file that holds it,
		// for the subsetter, which works on such files.
		var p *opentype.Font
		if p, err = opentype.ParseCFF(data); err == nil {
			if p, err = opentype.Parse(wrapCFF(data, p.NumGlyphs())); err == nil {
				sub, err = p.SubsetCFF(gids)
			}
		}
	default:
		return errors.New("no program cera subsets")
	}
	if err != nil {
		return err
	}
	if len(sub)+64 > len(data) {
		return errors.New("no smaller")
	}
	// Read back as cera reads programs: every glyph used has its outline,
	// and every code of a simple font, whose glyphs the program finds,
	// shows the glyph and reads the text it did.
	p, err := readProgram(u.key, sub)
	if err != nil {
		return err
	}
	face := p.NewFace(p.UnitsPerEm())
	for i, f := range u.fonts {
		f.mu.Lock()
		for _, g := range gids {
			a, _ := f.face.GlyphOutline(g)
			b, _ := face.GlyphOutline(g)
			if !slices.Equal(a, b) {
				f.mu.Unlock()
				return errors.New("a glyph changed")
			}
		}
		f.mu.Unlock()
		if f.composite() {
			continue
		}
		nf := d.loadFontWith(f.pdf.Dict(), u.key, sub)
		if nf.program == nil || nf.bad || nf.substituted {
			return errors.New("the subset does not read")
		}
		for code := range u.codes[i] {
			g1, o1 := f.glyph(code)
			g2, o2 := nf.glyph(code)
			t1, _ := f.Text(code)
			t2, _ := nf.Text(code)
			if g1 != g2 || t1 != t2 || !samePath(o1, o2) {
				return errors.New("a code shows otherwise")
			}
		}
	}

	// The program, under Flate, with the entries of the stream that do
	// not describe its encoding.
	dict := pdf.NewDict()
	for key, v := range u.s.Dict.All() {
		switch key {
		case "Filter", "DecodeParms", "Length", "DL", "Length1", "Length2", "Length3":
		default:
			dict = dict.With(key, v)
		}
	}
	if u.key == "FontFile2" {
		dict = dict.With("Length1", pdf.Integer(int64(len(sub))))
	}
	put := func(num int32, o pdf.Object) {
		if doc.Replace == nil {
			doc.Replace = map[*pdf.Document]map[int32]pdf.Object{}
		}
		if doc.Replace[k.r] == nil {
			doc.Replace[k.r] = map[int32]pdf.Object{}
		}
		doc.Replace[k.r][num] = o
	}
	put(k.num, pdfwrite.Flate(dict, sub).Object())

	// The names say that it is a subset (PDF 2.0, 9.6.4), and /CIDSet,
	// which lists the glyphs of a subset, goes.
	h := fnv.New32a()
	for _, g := range gids {
		h.Write([]byte{byte(g >> 8), byte(g)})
	}
	tag, sum := make([]byte, 7), h.Sum32()
	for i := range 6 {
		tag[i] = 'A' + byte(sum%26)
		sum /= 26
	}
	tag[6] = '+'
	tagged := func(o pdf.Object) (pdf.Object, bool) {
		n, ok := d.resolve(o).Name()
		if !ok || len(n) == 0 {
			return o, false
		}
		if isTagged(string(n)) {
			return o, false
		}
		return pdf.Name(string(tag) + string(n)).Object(), true
	}
	for num, o := range fontObjects(d, u.fonts) {
		dd, ok := o.Dict()
		if !ok {
			continue
		}
		changed := false
		for _, key := range []pdf.Name{"BaseFont", "FontName"} {
			if v, ok := tagged(dd.Get(key)); ok {
				dd, changed = dd.With(key, v), true
			}
		}
		if dd.Has("CIDSet") {
			dd, changed = without(dd, "CIDSet"), true
		}
		if changed {
			put(num, dd.Object())
		}
	}
	return nil
}

// isTagged reports whether a font name carries a subset tag: six upper
// case letters and a plus.
func isTagged(n string) bool {
	if len(n) < 7 || n[6] != '+' {
		return false
	}
	for i := range 6 {
		if n[i] < 'A' || n[i] > 'Z' {
			return false
		}
	}
	return true
}

// fontObjects returns the font dictionaries and descriptors of fonts that
// are objects of their own, by number: a Type 0 font with its descendant.
func fontObjects(d *Document, fonts []*Font) map[int32]pdf.Object {
	out := map[int32]pdf.Object{}
	add := func(o pdf.Object) pdf.Dict {
		r, ok := o.Ref()
		dd := d.dict(o)
		if ok && !dd.IsZero() {
			out[r.Num] = dd.Object()
		}
		return dd
	}
	d.fontMu.Lock()
	refs := map[*Font][]pdf.Ref{}
	for r, f := range d.fonts {
		refs[f] = append(refs[f], r)
	}
	d.fontMu.Unlock()
	for _, f := range fonts {
		for _, r := range refs[f] {
			fd := add(r.Object())
			if a, _ := d.resolve(fd.Get("DescendantFonts")).Array(); len(a) > 0 {
				add(d.dict(a[0]).Get("FontDescriptor"))
				add(a[0])
			}
			add(fd.Get("FontDescriptor"))
		}
	}
	return out
}

// sfnt tables.

type subTable struct {
	tag  string
	data []byte
}

// subTables returns the tables of an sfnt file, in the order of its
// directory.
func subTables(b []byte) ([]subTable, bool) {
	if len(b) < 12 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(b[4:]))
	if len(b) < 12+16*n {
		return nil, false
	}
	out := make([]subTable, 0, n)
	for i := range n {
		rec := b[12+16*i:]
		off, length := int(binary.BigEndian.Uint32(rec[8:])), int(binary.BigEndian.Uint32(rec[12:]))
		if off < 0 || length < 0 || off+length > len(b) {
			return nil, false
		}
		out = append(out, subTable{string(rec[:4]), b[off : off+length]})
	}
	return out, true
}

func hasTable(b []byte, tag string) bool {
	ts, _ := subTables(b)
	return slices.ContainsFunc(ts, func(t subTable) bool { return t.tag == tag })
}

// replaceTables writes the sfnt file b with the tables of repl in place of
// its own, its digital signature left out: the file it signs is another.
func replaceTables(b []byte, repl map[string][]byte) ([]byte, error) {
	ts, ok := subTables(b)
	if !ok {
		return nil, errors.New("not an sfnt file")
	}
	ts = slices.DeleteFunc(ts, func(t subTable) bool { return t.tag == "DSIG" })
	for i := range ts {
		if r, ok := repl[ts[i].tag]; ok {
			ts[i].data = r
		}
	}
	ts = slices.DeleteFunc(ts, func(t subTable) bool { return t.data == nil })
	return writeTables(b[:4], ts), nil
}

// writeTables writes an sfnt file of version and tables, with their
// checksums and the font's.
func writeTables(version []byte, ts []subTable) []byte {
	n := len(ts)
	out := make([]byte, 12+16*n)
	copy(out, version)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	es := 0
	for 1<<(es+1) <= n {
		es++
	}
	binary.BigEndian.PutUint16(out[6:], uint16(16<<es))
	binary.BigEndian.PutUint16(out[8:], uint16(es))
	binary.BigEndian.PutUint16(out[10:], uint16(16*n-16<<es))
	headAt := -1
	for i, t := range ts {
		data := t.data
		if t.tag == "head" && len(data) >= 12 {
			data = slices.Clone(data)
			binary.BigEndian.PutUint32(data[8:], 0) // checkSumAdjustment, below
			headAt = len(out)
		}
		rec := out[12+16*i:]
		copy(rec, t.tag)
		binary.BigEndian.PutUint32(rec[4:], tableSum(data))
		binary.BigEndian.PutUint32(rec[8:], uint32(len(out)))
		binary.BigEndian.PutUint32(rec[12:], uint32(len(data)))
		out = append(out, data...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	if headAt >= 0 {
		binary.BigEndian.PutUint32(out[headAt+8:], 0xB1B0AFBA-tableSum(out))
	}
	return out
}

// tableSum is the checksum of an sfnt table: the sum of its words.
func tableSum(b []byte) uint32 {
	var s uint32
	for i := 0; i < len(b); i += 4 {
		var w [4]byte
		copy(w[:], b[i:])
		s += binary.BigEndian.Uint32(w[:])
	}
	return s
}

// subsetGlyf empties the glyphs of a TrueType program that are not in
// gids, glyph 0 and the components of composite glyphs kept: the glyphs
// keep their numbers. repl are further tables to replace, nil to leave
// out.
func subsetGlyf(b []byte, gids []opentype.GlyphIndex, repl map[string][]byte) ([]byte, error) {
	ts, ok := subTables(b)
	if !ok {
		return nil, errors.New("not an sfnt file")
	}
	table := func(tag string) []byte {
		for _, t := range ts {
			if t.tag == tag {
				return t.data
			}
		}
		return nil
	}
	head, maxp, loca, glyf := table("head"), table("maxp"), table("loca"), table("glyf")
	if len(head) < 54 || len(maxp) < 6 || loca == nil || glyf == nil {
		return nil, errors.New("tables missing")
	}
	n := int(binary.BigEndian.Uint16(maxp[4:]))
	long := binary.BigEndian.Uint16(head[50:]) == 1
	offsets := make([]int, n+1)
	for i := range n + 1 {
		switch {
		case long && 4*i+4 <= len(loca):
			offsets[i] = int(binary.BigEndian.Uint32(loca[4*i:]))
		case !long && 2*i+2 <= len(loca):
			offsets[i] = 2 * int(binary.BigEndian.Uint16(loca[2*i:]))
		default:
			return nil, errors.New("loca too short")
		}
	}
	glyph := func(g int) []byte {
		a, z := offsets[g], offsets[g+1]
		if a >= z || z > len(glyf) {
			return nil
		}
		return glyf[a:z]
	}
	keep := make([]bool, n)
	var walk func(g int, depth int)
	walk = func(g int, depth int) {
		if g < 0 || g >= n || keep[g] || depth > 16 {
			return
		}
		keep[g] = true
		for _, c := range glyphComponents(glyph(g)) {
			walk(c, depth+1)
		}
	}
	walk(0, 0)
	for _, g := range gids {
		if int(g) >= n {
			return nil, errors.New("glyph out of range")
		}
		walk(int(g), 0)
	}
	var nglyf []byte
	noff := make([]int, n+1)
	for g := range n {
		noff[g] = len(nglyf)
		if keep[g] {
			nglyf = append(nglyf, glyph(g)...)
			for len(nglyf)%4 != 0 {
				nglyf = append(nglyf, 0)
			}
		}
	}
	noff[n] = len(nglyf)
	short := noff[n]/2 <= math.MaxUint16
	var nloca []byte
	for _, o := range noff {
		if short {
			nloca = binary.BigEndian.AppendUint16(nloca, uint16(o/2))
		} else {
			nloca = binary.BigEndian.AppendUint32(nloca, uint32(o))
		}
	}
	nhead := slices.Clone(head)
	if short {
		binary.BigEndian.PutUint16(nhead[50:], 0)
	} else {
		binary.BigEndian.PutUint16(nhead[50:], 1)
	}
	repl["glyf"], repl["loca"], repl["head"] = nglyf, nloca, nhead
	return replaceTables(b, repl)
}

// glyphComponents returns the glyphs a composite glyph is made of.
func glyphComponents(g []byte) []int {
	if len(g) < 10 || int16(binary.BigEndian.Uint16(g)) >= 0 {
		return nil
	}
	var out []int
	for p := 10; p+4 <= len(g); {
		flags := binary.BigEndian.Uint16(g[p:])
		out = append(out, int(binary.BigEndian.Uint16(g[p+2:])))
		p += 4
		if flags&1 != 0 { // ARG_1_AND_2_ARE_WORDS
			p += 4
		} else {
			p += 2
		}
		switch {
		case flags&8 != 0: // WE_HAVE_A_SCALE
			p += 2
		case flags&0x40 != 0: // WE_HAVE_AN_X_AND_Y_SCALE
			p += 4
		case flags&0x80 != 0: // WE_HAVE_A_TWO_BY_TWO
			p += 8
		}
		if flags&0x20 == 0 { // MORE_COMPONENTS
			break
		}
	}
	return out
}

// wrapCFF returns an OpenType file of a bare CFF program of n glyphs, with
// the tables a reader asks for and no more.
func wrapCFF(cff []byte, n int) []byte {
	head := make([]byte, 54)
	binary.BigEndian.PutUint32(head, 0x00010000)
	binary.BigEndian.PutUint32(head[12:], 0x5F0F3CF5)
	binary.BigEndian.PutUint16(head[18:], 1000)
	maxp := make([]byte, 6)
	binary.BigEndian.PutUint32(maxp, 0x00005000)
	binary.BigEndian.PutUint16(maxp[4:], uint16(n))
	hhea := make([]byte, 36)
	binary.BigEndian.PutUint32(hhea, 0x00010000)
	binary.BigEndian.PutUint16(hhea[34:], 1)
	hmtx := make([]byte, 4+2*max(n-1, 0))
	tables := []subTable{{"CFF ", cff}, {"head", head}, {"hhea", hhea}, {"hmtx", hmtx}, {"maxp", maxp}}
	return writeTables([]byte("OTTO"), tables)
}

// samePath reports whether two glyph outlines are the same.
func samePath(a, b *Path) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.Verbs, b.Verbs) && slices.Equal(a.Points, b.Points)
}
