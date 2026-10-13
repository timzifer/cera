package cera

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// linearObjs is a document of four pages with what the parts of a
// linearised file are made of: fonts and an image that pages share, an
// outline, a form, a thumbnail, objects of single pages.
func linearObjs() map[int]string {
	page := func(i int, extra string) string {
		return fmt.Sprintf("<</Type /Page /Parent 2 0 R /Contents %d 0 R /Resources <</Font <</F1 30 0 R /F2 31 0 R>> /XObject <</Im %d 0 R /Sh 34 0 R>>>>%s>>",
			20+i, 40+i, extra)
	}
	content := func(i int) string {
		return testpdf.Stream("", fmt.Appendf(nil, "BT /F1 14 Tf 20 170 Td (Page %d) Tj /F2 9 Tf 0 -20 Td (of four) Tj ET q 30 0 0 30 20 80 cm /Im Do Q q 30 0 0 30 %d 80 cm /Sh Do Q", i+1, 60+20*i))
	}
	image := func(v byte) string {
		return testpdf.Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8", bytes.Repeat([]byte{v}, 4))
	}
	return map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /Outlines 50 0 R /PageMode /UseNone /AcroForm <</Fields [60 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 30 0 R>>>>>>" +
			" /ViewerPreferences 70 0 R /Names <</Dests 71 0 R>>>>",
		2:  "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R 6 0 R] /Count 4 /MediaBox [0 0 200 200]>>",
		3:  page(0, " /Annots [60 0 R] /Thumb 45 0 R"),
		4:  page(1, ""),
		5:  page(2, " /Annots [61 0 R]"),
		6:  page(3, ""),
		20: content(0),
		21: content(1),
		22: content(2),
		23: content(3),
		30: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		31: "<</Type /Font /Subtype /Type1 /BaseFont /Courier /Encoding 32 0 R>>",
		32: "<</Type /Encoding /BaseEncoding /WinAnsiEncoding>>",
		34: image(128), // on every page
		40: image(10),
		41: image(60),
		42: image(110),
		43: image(160),
		45: image(250),
		50: "<</Type /Outlines /First 51 0 R /Last 52 0 R /Count 2>>",
		51: "<</Title (One) /Parent 50 0 R /Next 52 0 R /Dest [3 0 R /Fit]>>",
		52: "<</Title (Three) /Parent 50 0 R /Prev 51 0 R /Dest [5 0 R /Fit]>>",
		60: "<</Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (old) /Rect [20 20 120 40] /P 3 0 R>>",
		61: "<</Type /Annot /Subtype /Link /Rect [20 20 60 40] /Dest [4 0 R /Fit] /Border [0 0 0]>>",
		70: "<</DisplayDocTitle true>>",
		71: "<</Names [(first) [3 0 R /Fit]]>>",
		72: "<</Title (Linear)>>",
	}
}

const linearTrailer = "/Root 1 0 R /Info 72 0 R"

func linearFile() []byte { return testpdf.File{Objs: linearObjs(), Trailer: linearTrailer}.Bytes() }

// bitReader reads the fields of a hint table.
type bitReader struct {
	buf []byte
	pos int // in bits
}

func (b *bitReader) read(n int) (v uint64) {
	for ; n > 0; n-- {
		v <<= 1
		if b.pos/8 < len(b.buf) && b.buf[b.pos/8]&(0x80>>(b.pos%8)) != 0 {
			v |= 1
		}
		b.pos++
	}
	return v
}

func (b *bitReader) align() { b.pos = (b.pos + 7) &^ 7 }

// checkLinear checks that file is linearised and that what its first
// object and its hint tables say is so in the file: the lengths, the
// places of the pages and of what they share.
func checkLinear(t *testing.T, name string, file []byte, packed bool) {
	t.Helper()
	d, err := pdf.Open(file)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if d.Repaired() {
		t.Fatalf("%s: the file needed a repair", name)
	}
	if d.Packed() != packed {
		t.Errorf("%s: packed %v, want %v", name, d.Packed(), packed)
	}
	// The first object, in the first kilobyte.
	m := regexp.MustCompile(`^%PDF-\d\.\d\n%[^\n]*\n(\d+) 0 obj\n<</Linearized 1 /L (\d+) /H \[ (\d+) (\d+) \] /O (\d+) /E (\d+) /N (\d+) /T (\d+)>>\nendobj *\n`).FindSubmatch(file[:min(len(file), 1024)])
	if m == nil {
		t.Fatalf("%s: the file starts %q", name, file[:min(len(file), 200)])
	}
	num := func(i int) int { v, _ := strconv.Atoi(string(m[i])); return v }
	length, hintOff, hintLen, first, firstEnd, pages, beforeZero := num(2), num(3), num(4), num(5), num(6), num(7), num(8)
	if length != len(file) || pages != d.PageCount() {
		t.Errorf("%s: /L %d of %d bytes, /N %d of %d pages", name, length, len(file), pages, d.PageCount())
	}
	at := func(off, num int) bool {
		return off >= 0 && off < len(file) && bytes.HasPrefix(file[off:], fmt.Appendf(nil, "%d 0 obj\n", num))
	}
	ends := func(off int) bool { return off >= 7 && off <= len(file) && string(file[off-7:off]) == "endobj\n" }
	if r, _ := d.PageRef(1); int(r.Num) != first {
		t.Errorf("%s: /O %d, the first page is object %d", name, first, r.Num)
	}
	if !ends(firstEnd) || !ends(hintOff) || !ends(hintOff+hintLen) {
		t.Errorf("%s: /E %d or /H [%d %d] is not where an object ends", name, firstEnd, hintOff, hintLen)
	}
	if beforeZero >= len(file) || file[beforeZero] != '\n' {
		t.Fatalf("%s: /T %d is not at a line end", name, beforeZero)
	}
	if after := file[beforeZero+1:]; packed != regexp.MustCompile(`^\d+ 0 obj\n<<[^>]*/Type /XRef`).Match(after) ||
		packed == bytes.HasPrefix(after, []byte("0000000000 65535 f")) {
		t.Errorf("%s: after /T comes %q", name, after[:min(len(after), 40)])
	}
	// The file ends with the place of the first-page section, which has
	// the trailer and leads on to the main one.
	tail := regexp.MustCompile(`startxref\n(\d+)\n%%EOF\n$`).FindSubmatch(file)
	if tail == nil {
		t.Fatalf("%s: the file ends %q", name, file[len(file)-40:])
	}
	firstXref, _ := strconv.Atoi(string(tail[1]))
	if firstXref > 1024 || !regexp.MustCompile(`^(xref\n|\d+ 0 obj\n<</Type /XRef)`).Match(file[firstXref:]) {
		t.Errorf("%s: the first-page section is at %d: %q", name, firstXref, file[firstXref:firstXref+20])
	}

	// The hint stream.
	hm := regexp.MustCompile(`^(\d+) 0 obj\n`).FindSubmatch(file[hintOff:])
	if hm == nil {
		t.Fatalf("%s: no object at /H %d", name, hintOff)
	}
	hn, _ := strconv.Atoi(string(hm[1]))
	ho, err := d.Get(pdf.Ref{Num: int32(hn)})
	hs, ok := ho.Stream()
	if err != nil || !ok {
		t.Fatalf("%s: the hint stream is %v: %v", name, ho, err)
	}
	dec := d.Decode(hs)
	sharedAt, _ := hs.Dict.Get("S").Int()
	if dec.Recovered || sharedAt <= 0 || int(sharedAt) >= len(dec.Data) {
		t.Fatalf("%s: the hint stream has %d bytes, /S %d: %v", name, len(dec.Data), sharedAt, dec.Cause)
	}
	// Offsets in the tables count as if the hint stream were not there.
	adjust := func(off int) int {
		if off >= hintOff {
			return off + hintLen
		}
		return off
	}
	h := &bitReader{buf: dec.Data}
	minObjects, firstOff, bitsObjects := int(h.read(32)), int(h.read(32)), int(h.read(16))
	minLength, bitsLength := int(h.read(32)), int(h.read(16))
	h.read(32 + 16 + 32 + 16)
	bitsShared, bitsIndex := int(h.read(16)), int(h.read(16))
	h.read(16 + 16)
	if !at(adjust(firstOff), first) {
		t.Errorf("%s: the hints have the first page at %d", name, adjust(firstOff))
	}
	objects, lengths, nshared := make([]int, pages), make([]int, pages), make([]int, pages)
	for i := range objects {
		objects[i] = minObjects + int(h.read(bitsObjects))
	}
	h.align()
	for i := range lengths {
		lengths[i] = minLength + int(h.read(bitsLength))
	}
	h.align()
	for i := range nshared {
		nshared[i] = int(h.read(bitsShared))
	}
	h.align()
	shared := make([][]int, pages)
	for i := range shared {
		for range nshared[i] {
			shared[i] = append(shared[i], int(h.read(bitsIndex)))
		}
	}
	// The pages follow each other: the first where the hints say, the
	// others from the end of the first on, each as long as its objects.
	off := adjust(firstOff)
	for i := range pages {
		r, _ := d.PageRef(i + 1)
		if i == 1 && off != firstEnd {
			t.Errorf("%s: the first page ends at %d, /E is %d", name, off, firstEnd)
		}
		if !at(off, int(r.Num)) {
			t.Errorf("%s: page %d, object %d, is not at %d", name, i, r.Num, off)
		}
		// Its objects are numbered on from it and lie one after another.
		end := off
		for k := range objects[i] {
			if !at(end, int(r.Num)+k) {
				t.Errorf("%s: page %d: object %d is not at %d", name, i, int(r.Num)+k, end)
				break
			}
			end += bytes.Index(file[end:], []byte("endobj\n")) + 7
		}
		if end-off != lengths[i] {
			t.Errorf("%s: page %d is %d bytes, the hints say %d", name, i, end-off, lengths[i])
		}
		off += lengths[i]
	}
	if nshared[0] != 0 {
		t.Errorf("%s: the first page lists %d shared objects", name, nshared[0])
	}

	// The shared objects: those of the first page, then those of part 8,
	// which follow the pages.
	s := &bitReader{buf: dec.Data[sharedAt:]}
	firstShared, firstSharedOff, sharedFirst, sharedTotal := int(s.read(32)), int(s.read(32)), int(s.read(32)), int(s.read(32))
	s.read(16)
	minGroup, bitsGroup := int(s.read(32)), int(s.read(16))
	if sharedFirst != objects[0] || sharedTotal < sharedFirst {
		t.Errorf("%s: %d shared objects, %d of the first page, which has %d", name, sharedTotal, sharedFirst, objects[0])
	}
	groups := make([]int, sharedTotal)
	for i := range groups {
		groups[i] = minGroup + int(s.read(bitsGroup))
	}
	if sharedTotal > sharedFirst {
		if firstSharedOff = adjust(firstSharedOff); firstSharedOff != off || !at(firstSharedOff, firstShared) {
			t.Errorf("%s: the shared objects start at %d with object %d; the pages end at %d", name, firstSharedOff, firstShared, off)
		}
		for i := sharedFirst; i < sharedTotal; i++ {
			if !at(off, firstShared+i-sharedFirst) || !ends(off+groups[i]) {
				t.Errorf("%s: shared object %d is not at %d with %d bytes", name, i, off, groups[i])
			}
			off += groups[i]
		}
	}
	for i := range shared {
		for _, x := range shared[i] {
			if x >= sharedTotal {
				t.Errorf("%s: page %d needs shared object %d of %d", name, i, x, sharedTotal)
			}
		}
	}
	if pages > 1 && sharedTotal > sharedFirst && len(shared[1]) == 0 {
		t.Errorf("%s: the second page needs no shared object", name)
	}
}

// TestSaveLinearized writes linearised files: with a cross-reference
// table and packed, plain and encrypted. They say of themselves what is
// so, and are the documents they were.
func TestSaveLinearized(t *testing.T) {
	src := linearFile()
	doc := editOpen(t, src)
	want := formString(doc)
	for _, packed := range []bool{false, true} {
		name := fmt.Sprintf("packed %v", packed)
		opt := SaveOptions{Linearize: true, ObjectStreams: ObjectStreamsNever}
		if packed {
			opt.ObjectStreams = ObjectStreamsAlways
		}
		out := saveWith(t, doc.Edit(), opt)
		checkLinear(t, name, out, packed)
		samePages(t, name, src, out, 0, 1, 2, 3)
		od := editOpen(t, out)
		if got := formString(od); got != want {
			t.Errorf("%s: the form reads\n%s, want\n%s", name, got, want)
		}
		if o := od.Outline(); len(o) != 2 || o[1].Title != "Three" || o[1].Target == nil {
			t.Errorf("%s: the outline is %+v", name, o)
		} else if v, ok := od.Destination(o[1].Target); !ok || v.Page != 2 {
			t.Errorf("%s: the second entry of the outline leads to %+v", name, v)
		}
		if meta := od.Metadata(); meta.Title != "Linear" {
			t.Errorf("%s: the title is %q", name, meta.Title)
		}
		// Nothing but what the document has is in the file, and what
		// leads through it: the first object with its room, the hints, a
		// second cross-reference section, object streams by part.
		if plain := saveWith(t, doc.Edit(), SaveOptions{ObjectStreams: opt.ObjectStreams}); len(out) > len(plain)+2500 {
			t.Errorf("%s: %d bytes linearised, %d plain", name, len(out), len(plain))
		}

		// An update of it is a file like any other.
		e := od.Edit()
		state := od.Form().NewState()
		if err := state.SetValue(od.Form().Field("name"), TextValue("new")); err != nil {
			t.Fatal(err)
		}
		if err := e.SetFields(state); err != nil {
			t.Fatal(err)
		}
		var upd bytes.Buffer
		if err := e.Update(&upd); err != nil {
			t.Fatalf("%s: update: %v", name, err)
		}
		ud := editOpen(t, upd.Bytes())
		if ud.Form().Field("name").Saved.Text() != "new" || ud.NumPages() != 4 || ud.r.Repaired() {
			t.Errorf("%s: updated, the field reads %q", name, ud.Form().Field("name").Saved.Text())
		}

		// With the outline shown when the document opens, it is part of
		// the first page's section.
		objs := linearObjs()
		objs[1] = strings.Replace(objs[1], "/UseNone", "/UseOutlines", 1)
		shown := testpdf.File{Objs: objs, Trailer: linearTrailer}.Bytes()
		out2 := saveWith(t, editOpen(t, shown).Edit(), opt)
		checkLinear(t, name+", outline shown", out2, packed)
		if first, second := bytes.Index(out, []byte("(Three)")), bytes.Index(out2, []byte("(Three)")); !packed && (first < 0 || second < 0 || second >= first) {
			t.Errorf("%s: the outline is at %d, shown at opening at %d", name, first, second)
		}

		// One page: there is no second half but the page tree.
		one := doc.Edit()
		if err := one.DeletePages(1, 2, 3); err != nil {
			t.Fatal(err)
		}
		out = saveWith(t, one, opt)
		checkLinear(t, name+", one page", out, packed)
		samePages(t, name+", one page", src, out, 0)

		// With everything that makes it smaller.
		opt.PruneResources, opt.JoinDuplicates, opt.Compress, opt.Drop = true, true, true, DropThumbnails
		out = saveWith(t, doc.Edit(), opt)
		checkLinear(t, name+", smaller", out, packed)
		samePages(t, name+", smaller", src, out, 0, 1, 2, 3)
	}

	// Encrypted: the hint stream too, and the document opens as it did.
	for _, method := range []string{"rc4", "aes256"} {
		ed, err := OpenWithPassword(testpdf.EncryptedWith(method, "user", -1), "user")
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []ObjectStreamMode{ObjectStreamsNever, ObjectStreamsAlways} {
			ee := ed.Edit()
			ee.unguarded = true
			var enc bytes.Buffer
			if err := ee.SaveWith(&enc, SaveOptions{Linearize: true, ObjectStreams: mode}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(enc.Bytes()[:200], []byte("/Linearized 1")) || bytes.Contains(enc.Bytes(), []byte("(old)")) {
				t.Errorf("%s, mode %d: the file starts %q", method, mode, enc.Bytes()[:60])
			}
			od, err := OpenWithPassword(enc.Bytes(), "user")
			if err != nil {
				t.Fatalf("%s, mode %d: %v", method, mode, err)
			}
			if !od.Encrypted() || od.r.Repaired() || od.NumPages() != ed.NumPages() || od.Form().Field("secret").Saved.Text() != "old" {
				t.Errorf("%s, mode %d: encrypted %v, repaired %v, the field reads %q", method, mode, od.Encrypted(), od.r.Repaired(), od.Form().Field("secret").Saved.Text())
			}
			for i := range ed.NumPages() {
				a, _ := editRender(t, ed, i)
				b, _ := editRender(t, od, i)
				if n, _ := pixDiff(a, b); n != 0 {
					t.Errorf("%s, mode %d: page %d differs in %d pixels", method, mode, i, n)
				}
			}
		}
	}

	// A new document.
	ne := NewEditor()
	if err := ne.ImportPages(0, doc, EditPage{Index: 3}, EditPage{Index: 1}); err != nil {
		t.Fatal(err)
	}
	out := saveWith(t, ne, SaveOptions{Linearize: true})
	checkLinear(t, "new", out, false)
	samePages(t, "new", src, out, 3, 1)
}
