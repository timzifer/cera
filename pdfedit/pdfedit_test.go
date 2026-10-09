package pdfedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/timzifer/cera"
	"github.com/timzifer/cera/internal/pdf"
)

func extract(t *testing.T, src []byte, pages ...Page) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Extract(&out, src, pages); err != nil {
		t.Fatal(err)
	}
	strict(t, out.Bytes())
	return out.Bytes()
}

// strict checks that b reads without a repair: every offset of its table
// holds the object it names.
func strict(t *testing.T, b []byte) *pdf.Document {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("%PDF-1.7\n%")) {
		t.Fatalf("header %q", b[:min(len(b), 16)])
	}
	d, err := pdf.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := d.Trailer().Get("Size").Int()
	for n := int32(1); n < int32(size); n++ {
		if o, err := d.Get(pdf.Ref{Num: n}); err != nil || o.IsNull() {
			t.Fatalf("object %d: %v %v", n, o, err)
		}
	}
	if d.Repaired() {
		t.Fatal("the output needed a repair")
	}
	if d.Encrypted() {
		t.Fatal("the output is encrypted")
	}
	return d
}

func pages(idx ...int) []Page {
	out := make([]Page, len(idx))
	for i, x := range idx {
		out[i] = Page{Index: x}
	}
	return out
}

func render(t *testing.T, doc *cera.Document, i int) (*image.RGBA, *cera.Page) {
	t.Helper()
	p, err := doc.Page(i)
	if err != nil {
		t.Fatal(err)
	}
	const scale = 2
	dst := image.NewRGBA(p.Bounds(scale))
	err = p.Render(context.Background(), dst, cera.RenderOptions{
		Scale: scale, Background: color.RGBA{255, 255, 255, 255}, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst, p
}

func open(t *testing.T, b []byte) *cera.Document {
	t.Helper()
	d, err := cera.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// sameRenders checks that each output page renders like the source page
// it was made from, pixel for pixel, or with its sides swapped when it was
// turned by a quarter.
func sameRenders(t *testing.T, src, out []byte, sel []Page) {
	t.Helper()
	sd, od := open(t, src), open(t, out)
	if od.NumPages() != len(sel) {
		t.Fatalf("%d pages, want %d", od.NumPages(), len(sel))
	}
	for i, p := range sel {
		want, sp := render(t, sd, p.Index)
		got, op := render(t, od, i)
		if op.Rotate != rotation(pdf.Integer(int64(sp.Rotate)), p.Rotate) {
			t.Errorf("page %d: /Rotate %d, source %d + %d", i, op.Rotate, sp.Rotate, p.Rotate)
		}
		wb, gb := want.Bounds(), got.Bounds()
		switch (p.Rotate%360 + 360) % 360 {
		case 0:
			if wb != gb || !bytes.Equal(want.Pix, got.Pix) {
				t.Errorf("page %d (source %d) renders differently", i, p.Index)
			}
		case 180:
			if wb != gb {
				t.Errorf("page %d: %v, want %v", i, gb, wb)
			}
		default:
			if wb.Dx() != gb.Dy() || wb.Dy() != gb.Dx() {
				t.Errorf("page %d: %v, want %v turned", i, gb, wb)
			}
		}
	}
}

func TestExtractRenders(t *testing.T) {
	tests := []struct {
		name string
		sel  []Page
	}{
		{"all", pages(0, 1, 2, 3)},
		{"subset", pages(1, 3)},
		{"one", pages(2)},
		{"reorder", pages(3, 0, 2, 1)},
		{"repeat", pages(2, 2, 0, 2)},
		{"rotate", []Page{{pageA, 90}, {pageB, 180}, {pageC, 270}, {pageD, -90}, {pageA, 360}, {pageC, -720}}},
	}
	for _, objStm := range []bool{false, true} {
		src := sampleFile(objStm)
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s/objstm=%v", tt.name, objStm), func(t *testing.T) {
				out := extract(t, src, tt.sel...)
				sameRenders(t, src, out, tt.sel)
			})
		}
	}
}

func TestSourceIsCompressed(t *testing.T) {
	// The builder's object streams are read as such, not repaired.
	d, err := pdf.Open(sampleFile(true))
	if err != nil {
		t.Fatal(err)
	}
	if d.PageCount() != 4 || d.Repaired() {
		t.Fatalf("%d pages, repaired %v", d.PageCount(), d.Repaired())
	}
	if typ, _ := d.Trailer().Get("Type").Name(); typ != "XRef" {
		t.Fatalf("trailer /Type %v", typ)
	}
}

func TestRotation(t *testing.T) {
	tests := []struct {
		src   pdf.Object
		extra int
		want  int
	}{
		{pdf.Null, 0, 0},
		{pdf.Integer(90), 0, 90},
		{pdf.Integer(90), 270, 0},
		{pdf.Integer(-90), 0, 270},
		{pdf.Integer(450), 90, 180},
		{pdf.Real(180.0), -90, 90},
		{pdf.Integer(0), -450, 270},
	}
	for _, tt := range tests {
		if got := rotation(tt.src, tt.extra); got != tt.want {
			t.Errorf("rotation(%v, %d) = %d, want %d", tt.src, tt.extra, got, tt.want)
		}
	}
}

// TestStructure checks what Extract copies, rewrites and drops.
func TestStructure(t *testing.T) {
	for _, objStm := range []bool{false, true} {
		t.Run(fmt.Sprintf("objstm=%v", objStm), func(t *testing.T) {
			out := extract(t, sampleFile(objStm), pages(pageA, pageB, pageA)...)
			d := strict(t, out)
			cat, err := d.Catalog()
			if err != nil {
				t.Fatal(err)
			}
			var keys []pdf.Name
			for k := range cat.All() {
				keys = append(keys, k)
			}
			if fmt.Sprint(keys) != "[Type Pages OCProperties Lang]" {
				t.Errorf("catalogue keys %v", keys)
			}
			info, _ := d.GetDict(d.Trailer(), "Info")
			if title, _ := info.Get("Title").Str(); string(title) != "Sample" {
				t.Errorf("/Info /Title %q", title)
			}

			images := 0
			size, _ := d.Trailer().Get("Size").Int()
			for n := int32(1); n < int32(size); n++ {
				o, _ := d.Get(pdf.Ref{Num: n})
				dict, _ := o.Dict()
				if s, _ := dict.Get("Subtype").Name(); s == "Image" || dict.Has("Width") {
					images++
				}
				if typ, _ := dict.Get("Type").Name(); typ == "Bead" || typ == "Thread" || typ == "StructTreeRoot" || typ == "Outlines" {
					t.Errorf("object %d is a %s", n, typ)
				}
			}
			if images != 1 {
				t.Errorf("%d images, want the shared one alone (no thumbnail)", images)
			}

			for i, want := range [][4]int{{0, 0, 200, 100}, {0, 0, 120, 160}, {0, 0, 200, 100}} {
				page, err := d.Page(i + 1)
				if err != nil {
					t.Fatal(err)
				}
				for _, k := range []pdf.Name{"B", "Thumb", "StructParents"} {
					if page.Has(k) {
						t.Errorf("page %d has /%s", i, k)
					}
				}
				// Inherited attributes are on the page itself.
				parent, _ := d.GetDict(page, "Parent")
				if parent.Has("MediaBox") || parent.Has("Resources") || !page.Has("Resources") {
					t.Errorf("page %d: attributes still inherited", i)
				}
				box, _ := d.Resolve(page.Get("MediaBox")).Array()
				if fmt.Sprint(box) != fmt.Sprint([]int{want[0], want[1], want[2], want[3]}) {
					t.Errorf("page %d /MediaBox %v, want %v", i, box, want)
				}
			}

			doc := open(t, out)
			var refs [2][]pdf.Object
			for _, i := range []int{0, 2} {
				p, err := doc.Page(i)
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, a := range p.Annotations() {
					s := a.Subtype
					if a.Link != nil {
						s += fmt.Sprintf("→%d%s", a.Link.Page, a.Link.URI)
					}
					got = append(got, s)
				}
				// The links to page B (one by name) now go to the copy of
				// B; those to C and D, which are not copied, are gone.
				const want = "[Link→1 Link→1 Square Popup Link→-1https://example.com]"
				if fmt.Sprint(got) != want {
					t.Errorf("page %d annotations %v, want %s", i, got, want)
				}
				page, _ := d.Page(i + 1)
				annots, _ := d.Resolve(page.Get("Annots")).Array()
				refs[i/2] = annots
				sq, _ := d.Resolve(annots[2]).Dict()
				if sq.Has("P") || sq.Has("StructParent") {
					t.Errorf("page %d: the square keeps %v", i, sq)
				}
				popup, _ := d.Resolve(annots[3]).Dict()
				if popup.Get("Parent") != annots[2] || sq.Get("Popup") != annots[3] {
					t.Errorf("page %d: popup %v, square %v", i, popup, sq)
				}
				named, _ := d.Resolve(annots[1]).Dict()
				if dest, _ := d.Resolve(named.Get("Dest")).Array(); len(dest) == 0 {
					t.Errorf("page %d: named destination not made explicit: %v", i, named)
				}
			}
			// Each copy of a page has annotations of its own.
			for j := range refs[0] {
				if refs[0][j] == refs[1][j] {
					t.Errorf("annotation %d is shared by both copies of page A", j)
				}
			}
		})
	}
}

// TestLayers checks that optional content stays hidden: without
// /OCProperties the hidden layer would cover page D in red.
func TestLayers(t *testing.T) {
	src := sampleFile(false)
	out := extract(t, src, pages(pageD)...)
	sameRenders(t, src, out, pages(pageD))
	cfg := open(t, out).Layers()
	if cfg == nil || len(cfg.Layers) != 1 || cfg.Layers[0].Name != "Hidden" || cfg.Layers[0].Visible {
		t.Fatalf("layers %+v", cfg)
	}
	img, _ := render(t, open(t, out), 0)
	if c := img.RGBAAt(4, 4); c.R == 255 && c.G == 0 {
		t.Fatal("the hidden layer shows")
	}
}

func TestEncrypted(t *testing.T) {
	const all = int32(-4)
	t.Run("allowed", func(t *testing.T) {
		src := encrypted("", all)
		sel := pages(1, 0)
		out := extract(t, src, sel...)
		sameRenders(t, src, out, sel)
		d := strict(t, out)
		info, _ := d.GetDict(d.Trailer(), "Info")
		if title, _ := info.Get("Title").Str(); string(title) != "Secret (title)" {
			t.Errorf("/Info /Title %q", title)
		}
		// Decrypted, still filtered.
		page, _ := d.Page(2)
		cs, _ := d.Resolve(page.Get("Contents")).Stream()
		if f, _ := cs.Dict.Get("Filter").Name(); f != "FlateDecode" {
			t.Errorf("content stream /Filter %v", cs.Dict.Get("Filter"))
		}
		if got := string(d.Decode(cs).Data); got != "1 0 0 rg 10 10 80 60 re f" {
			t.Errorf("content %q", got)
		}
	})
	for _, tt := range []struct {
		name     string
		password string
		perm     int32
	}{
		{"no assembly", "", all &^ (1 << 10)},
		{"user password", "secret", all},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Extract(&out, encrypted(tt.password, tt.perm), pages(0))
			if !errors.Is(err, ErrEncrypted) {
				t.Fatalf("err = %v, want ErrEncrypted", err)
			}
			if out.Len() != 0 {
				t.Fatal("output written")
			}
		})
	}
}

func TestErrors(t *testing.T) {
	src := sampleFile(false)
	tests := []struct {
		name string
		src  []byte
		sel  []Page
	}{
		{"no pages", src, nil},
		{"past the end", src, pages(0, 4)},
		{"negative", src, pages(-1)},
		{"rotation", src, []Page{{0, 45}}},
		{"not a pdf", []byte("hello"), pages(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Extract(&out, tt.src, tt.sel)
			if err == nil || errors.Is(err, ErrEncrypted) {
				t.Fatalf("err = %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("output written")
			}
		})
	}
}

func FuzzExtract(f *testing.F) {
	f.Add(sampleFile(false), uint8(0), uint8(1))
	f.Add(sampleFile(true), uint8(3), uint8(0))
	f.Add(encrypted("", -4), uint8(1), uint8(2))
	f.Fuzz(func(t *testing.T, b []byte, idx, rot uint8) {
		var out bytes.Buffer
		sel := []Page{{Index: int(idx % 4), Rotate: int(rot%4) * 90}, {Index: 0}}
		if Extract(&out, b, sel) != nil {
			return
		}
		d, err := pdf.Open(out.Bytes())
		if err != nil {
			t.Fatalf("the output does not open: %v", err)
		}
		if d.PageCount() != len(sel) || d.Repaired() {
			t.Fatalf("%d pages, repaired %v", d.PageCount(), d.Repaired())
		}
	})
}
