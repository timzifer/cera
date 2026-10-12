package cera

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/testpdf"
)

// plainDoc is a document of two pages of 300 by 200 points, the first
// turned by rotate degrees.
func plainDoc(rotate int) []byte {
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R>>",
			2: "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 300 200]>>",
			3: fmt.Sprintf("<</Type /Page /Parent 2 0 R /Rotate %d>>", rotate),
			4: "<</Type /Page /Parent 2 0 R>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// oneOfEach are annotations of every type the editor writes, on a page of
// 200 by 150 points or more, in points of the displayed page.
func oneOfEach() []NewAnnotation {
	when := time.Date(2026, 10, 12, 9, 30, 0, 0, time.FixedZone("", 2*3600))
	red, blue, yellow := []float64{1, 0, 0}, []float64{0, 0, 1}, []float64{1, 1, 0}
	return []NewAnnotation{
		{Subtype: "Text", Contents: "A note", Name: "n1", Markup: Markup{
			Bounds: Rect{10, 10, 30, 30}, Author: "Ada", Subject: "Remark", Created: when, Modified: when, Color: yellow, Icon: "Comment",
			Popup: &Popup{Bounds: Rect{40, 10, 140, 60}, Open: true}}},
		{Subtype: "FreeText", Contents: "Free text, Привет", Markup: Markup{
			Bounds: Rect{60, 70, 190, 110}, Color: []float64{1, 1, 0.8}, BorderWidth: 1, Align: AlignCenter,
			DefaultAppearance: "/Helv 9 Tf 0 0.4 0 rg", Callout: [][2]float64{{20, 140}, {40, 100}, {60, 100}}, LineEndings: [2]string{"OpenArrow"},
			Intent: "FreeTextCallout"}},
		{Subtype: "Square", Markup: Markup{Bounds: Rect{10, 40, 50, 60}, Color: red, Interior: yellow, BorderWidth: 2, Opacity: 0.5}},
		{Subtype: "Circle", Markup: Markup{Bounds: Rect{60, 40, 100, 60}, Interior: blue}},
		{Subtype: "Line", Contents: "see here", Markup: Markup{
			Line: [][2]float64{{110, 45}, {180, 55}}, LineEndings: [2]string{"Circle", "ClosedArrow"}, Color: blue, Interior: red, BorderWidth: 1.5}},
		{Subtype: "Polygon", Markup: Markup{Vertices: [][2]float64{{110, 10}, {140, 10}, {125, 35}}, BorderWidth: 1, BorderStyle: BorderDashed, Dash: []float64{2, 1}}},
		{Subtype: "PolyLine", Markup: Markup{Vertices: [][2]float64{{150, 10}, {165, 30}, {180, 10}}, BorderWidth: 1, Color: red, LineEndings: [2]string{"", "OpenArrow"}}},
		{Subtype: "Ink", Markup: Markup{Ink: [][][2]float64{{{10, 120}, {20, 135}, {35, 122}}, {{40, 130}}}, BorderWidth: 2, Color: blue}},
		{Subtype: "Highlight", Markup: Markup{Quads: []Quad{QuadOf(Rect{60, 120, 120, 132})}}},
		{Subtype: "Squiggly", Markup: Markup{Quads: []Quad{QuadOf(Rect{130, 120, 190, 132})}, Color: red}},
		{Subtype: "Caret", Markup: Markup{Bounds: Rect{10, 70, 22, 84}}},
		{Subtype: "Stamp", Markup: Markup{Bounds: Rect{100, 135, 190, 148}, Icon: "Approved"}},
		{Subtype: "Link", Link: &LinkTarget{URI: "https://example.com/", Page: -1}, Markup: Markup{Bounds: Rect{30, 70, 50, 84}, BorderWidth: 1, Color: blue}},
		{Subtype: "Link", Link: &LinkTarget{Page: 1, Fit: "XYZ", Params: []float64{10, math.NaN(), 0}}, Markup: Markup{Quads: []Quad{QuadOf(Rect{30, 90, 50, 100})}}},
	}
}

// TestAddAnnotation writes annotations of every type and reads them back,
// and draws them from their appearance streams as it does without.
func TestAddAnnotation(t *testing.T) {
	for _, rotate := range []int{0, 90, 180} {
		src := plainDoc(rotate)
		doc := editOpen(t, src)
		add := func(e *Editor) {
			for k, a := range oneOfEach() {
				index, err := e.AddAnnotation(0, a)
				if err != nil {
					t.Fatalf("%s: %v", a.Subtype, err)
				}
				if index != k {
					t.Errorf("%s has index %d, want %d", a.Subtype, index, k)
				}
			}
		}
		e := doc.Edit()
		add(e)
		// Without appearance streams the renderer generates what the
		// editor wrote into them.
		bare := doc.Edit()
		bare.noAppearance = true
		add(bare)
		generated := renderBorders(t, editOpen(t, editSave(t, bare)))
		if n, _ := pixDiff(generated, renderBorders(t, doc)); n < 1000 {
			t.Fatalf("rotate %d: the annotations draw %d pixels", rotate, n)
		}

		for name, out := range map[string][]byte{"save": editSave(t, e), "update": editUpdate(t, e, src)} {
			t.Run(fmt.Sprintf("rotate=%d/%s", rotate, name), func(t *testing.T) {
				d := editOpen(t, out)
				if n, most := pixDiff(generated, renderBorders(t, d)); n > 0 {
					t.Errorf("the appearance streams render unlike the generated appearances: %d pixels, by up to %d", n, most)
				}
				// The border of a link the editor wrote is in its appearance
				// stream: drawn whatever the options say.
				if n, _ := pixDiff(generated, renderState(t, d, nil)); n > 0 {
					t.Errorf("the border of the link needs LinkBorders: %d pixels differ", n)
				}
				p, _ := d.Page(0)
				annots := p.Annotations()
				want := oneOfEach()
				// The annotations in the order added, then the popups of the note and the line.
				if s := subtypes(t, d); s != "Text FreeText Square Circle Line Polygon PolyLine Ink Highlight Squiggly Caret Stamp Link Link Popup Popup" {
					t.Fatalf("annotations: %s", s)
				}
				for k, w := range want {
					a := &annots[k]
					got, ok := p.EditAnnotation(k)
					if !ok {
						t.Fatalf("%s cannot be edited", w.Subtype)
					}
					if a.Subtype != w.Subtype || a.Contents != w.Contents || a.Name != w.Name {
						t.Errorf("%s: read back as %s %q %q", w.Subtype, a.Subtype, a.Contents, a.Name)
					}
					if _, hasAP := a.dict.Lookup("AP"); !hasAP && !(w.Subtype == "Link" && w.BorderWidth == 0) {
						t.Errorf("%s has no appearance", w.Subtype)
					}
					sameAnnotation(t, w, got)
				}
				note := annots[0].Markup
				if note.Popup == nil || !note.Popup.Open || !sameRect(note.Popup.Bounds, Rect{40, 10, 140, 60}) || note.Popup.Index != 14 {
					t.Errorf("popup of the note: %+v", note.Popup)
				}
				if lp := annots[4].Markup.Popup; lp == nil || lp.Open || lp.Index != 15 {
					t.Errorf("popup of the line: %+v", lp)
				}
				if annots[0].Flags != AnnotPrint|AnnotNoZoom|AnnotNoRotate || annots[2].Flags != AnnotPrint {
					t.Errorf("flags %b and %b", annots[0].Flags, annots[2].Flags)
				}
				links := p.Links()
				if len(links) != 2 || links[0].Target.URI != "https://example.com/" || links[1].Target.Page != 1 || links[1].Target.Fit != "XYZ" ||
					len(links[1].Target.Params) != 3 || links[1].Target.Params[0] != 10 || !math.IsNaN(links[1].Target.Params[1]) {
					t.Errorf("links %+v", links)
				}
				if p1, _ := d.Page(1); len(p1.Annotations()) != 0 {
					t.Error("annotations on the second page")
				}
			})
		}
	}
}

// renderBorders renders the first page of d with the borders of links
// that have no appearance stream.
func renderBorders(t *testing.T, d *Document) *image.RGBA {
	t.Helper()
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	dst := image.NewRGBA(p.Bounds(2))
	if err := p.Render(context.Background(), dst, RenderOptions{Scale: 2, Background: white, Workers: 1, LinkBorders: true}); err != nil {
		t.Fatal(err)
	}
	return dst
}

func sameRect(a, b Rect) bool {
	return math.Abs(a.X0-b.X0) < 1e-6 && math.Abs(a.Y0-b.Y0) < 1e-6 && math.Abs(a.X1-b.X1) < 1e-6 && math.Abs(a.Y1-b.Y1) < 1e-6
}

func samePoints(a, b [][2]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i][0]-b[i][0]) > 1e-6 || math.Abs(a[i][1]-b[i][1]) > 1e-6 {
			return false
		}
	}
	return true
}

// sameAnnotation checks that got, read back, is what want wrote.
func sameAnnotation(t *testing.T, want, got NewAnnotation) {
	t.Helper()
	fail := func(what string, w, g any) { t.Errorf("%s: %s %v, want %v", want.Subtype, what, g, w) }
	if got.Author != want.Author || got.Subject != want.Subject || got.Intent != want.Intent {
		fail("author, subject, intent", []string{want.Author, want.Subject, want.Intent}, []string{got.Author, got.Subject, got.Intent})
	}
	if !got.Created.Equal(want.Created) || !got.Modified.Equal(want.Modified) {
		fail("dates", want.Modified, got.Modified)
	}
	wantColor := want.Color
	switch {
	case len(wantColor) > 0:
	case want.Subtype == "Highlight":
		wantColor = []float64{1, 1, 0}
	case want.Subtype == "Polygon":
		wantColor = []float64{0} // a border without a colour is black
	}
	if !reflect.DeepEqual(got.Color, wantColor) && len(got.Color)+len(wantColor) > 0 {
		fail("colour", wantColor, got.Color)
	}
	if !reflect.DeepEqual(got.Interior, want.Interior) && len(got.Interior)+len(want.Interior) > 0 {
		fail("interior", want.Interior, got.Interior)
	}
	if o := max(want.Opacity, 0); o == 0 && got.Opacity != 1 || o != 0 && got.Opacity != o {
		fail("opacity", want.Opacity, got.Opacity)
	}
	if bordered[want.Subtype] && (got.BorderWidth != want.BorderWidth || got.BorderStyle != want.BorderStyle) {
		fail("border", want.BorderWidth, got.BorderWidth)
	}
	if want.Bounds != (Rect{}) && !sameRect(got.Bounds, want.Bounds) {
		fail("bounds", want.Bounds, got.Bounds)
	}
	if len(got.Quads) != len(want.Quads) {
		fail("quads", want.Quads, got.Quads)
	}
	for i := range min(len(got.Quads), len(want.Quads)) {
		if !samePoints(got.Quads[i].P[:], want.Quads[i].P[:]) {
			fail("quad", want.Quads[i], got.Quads[i])
		}
	}
	if !samePoints(got.Vertices, want.Vertices) || !samePoints(got.Line, want.Line) || !samePoints(got.Callout, want.Callout) {
		fail("points", []any{want.Vertices, want.Line, want.Callout}, []any{got.Vertices, got.Line, got.Callout})
	}
	if len(got.Ink) != len(want.Ink) {
		fail("ink", want.Ink, got.Ink)
	}
	for i := range min(len(got.Ink), len(want.Ink)) {
		if !samePoints(got.Ink[i], want.Ink[i]) {
			fail("stroke", want.Ink[i], got.Ink[i])
		}
	}
	wantLE := want.LineEndings
	if want.Subtype == "Line" || want.Subtype == "PolyLine" {
		for i := range wantLE {
			if wantLE[i] == "" {
				wantLE[i] = "None"
			}
		}
	}
	if got.LineEndings != wantLE || got.Icon != want.Icon || got.Align != want.Align {
		fail("endings, icon, alignment", []any{wantLE, want.Icon, want.Align}, []any{got.LineEndings, got.Icon, got.Align})
	}
	if want.Subtype == "FreeText" && got.DefaultAppearance != want.DefaultAppearance {
		fail("default appearance", want.DefaultAppearance, got.DefaultAppearance)
	}
}

// TestAddAnnotationReply threads annotations added by one editor.
func TestAddAnnotationReply(t *testing.T) {
	doc := editOpen(t, markupFile())
	e := doc.Edit()
	note, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Text", Contents: "new", Markup: Markup{Bounds: Rect{10, 10, 30, 30}}})
	if err != nil {
		t.Fatal(err)
	}
	if note != 11 {
		t.Fatalf("index %d of the note, want 11: the page has 11 annotations", note)
	}
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Text", Contents: "to the new one",
		Markup: Markup{Bounds: Rect{10, 10, 30, 30}, Reply: &Reply{To: note}, State: "Accepted", StateModel: "Review"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Text", Contents: "to the old one",
		Markup: Markup{Bounds: Rect{10, 10, 30, 30}, Reply: &Reply{To: 0, Group: true}}}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []int{14, 99, 1 << 20} {
		if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Text", Markup: Markup{Bounds: Rect{10, 10, 30, 30}, Reply: &Reply{To: to}}}); err == nil {
			t.Errorf("a reply to annotation %d was added", to)
		}
	}
	p, _ := editOpen(t, editSave(t, e)).Page(0)
	annots := p.Annotations()
	if r := annots[12].Markup; r.Reply == nil || *r.Reply != (Reply{To: 11}) || r.State != "Accepted" || r.StateModel != "Review" {
		t.Errorf("reply to the new note: %+v, %+v", r.Reply, r)
	}
	if r := annots[13].Markup.Reply; r == nil || *r != (Reply{To: 0, Group: true}) {
		t.Errorf("reply to the old note: %+v", r)
	}
}

// TestSetAnnotation changes annotations of the document: what the caller
// set, and nothing else.
func TestSetAnnotation(t *testing.T) {
	src := markupFileWith("/Mine (kept)")
	doc := editOpen(t, src)
	p, _ := doc.Page(0)
	before := renderState(t, doc, nil)

	e := doc.Edit()
	note, ok := p.EditAnnotation(0)
	if !ok {
		t.Fatal("the note cannot be edited")
	}
	note.Contents, note.Color = "Changed", []float64{0, 1, 0}
	note.Popup = &Popup{Bounds: Rect{100, 40, 150, 90}}
	if err := e.SetAnnotation(0, 0, note); err != nil {
		t.Fatal(err)
	}
	ink, _ := p.EditAnnotation(4)
	ink.Ink = [][][2]float64{{{20, 20}, {60, 80}}}
	ink.Opacity = 0.5
	if err := e.SetAnnotation(0, 4, ink); err != nil {
		t.Fatal(err)
	}
	// A polygon gets a comment, and with it a popup.
	poly, _ := p.EditAnnotation(5)
	poly.Contents = "a comment"
	if err := e.SetAnnotation(0, 5, poly); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		index int
		a     NewAnnotation
	}{
		{0, ink},   // a note is no ink annotation
		{1, note},  // a popup
		{10, note}, // a widget
		{11, note}, // none
		{-1, note}, // none
		{0, func() NewAnnotation { a := note; a.Reply = &Reply{To: 0}; return a }()},
		{0, func() NewAnnotation { a := note; a.Opacity = 2; return a }()},
	} {
		if err := e.SetAnnotation(0, tt.index, tt.a); err == nil {
			t.Errorf("annotation %d was set to a %s", tt.index, tt.a.Subtype)
		}
	}
	if _, ok := p.EditAnnotation(1); ok {
		t.Error("a popup can be edited")
	}

	for name, out := range map[string][]byte{"save": editSave(t, e), "update": editUpdate(t, e, src)} {
		d := editOpen(t, out)
		op, _ := d.Page(0)
		annots := op.Annotations()
		if s := subtypes(t, d); s != "Text Popup Text Highlight Ink Polygon Line FreeText Stamp Link Widget Popup" {
			t.Fatalf("%s: annotations %s", name, s)
		}
		n := &annots[0]
		if n.Contents != "Changed" || !reflect.DeepEqual(n.Markup.Color, []float64{0, 1, 0}) || n.Markup.Author != "Ada" ||
			n.Markup.Icon != "Comment" || n.Markup.RichText == "" || n.Markup.Opacity != 0.5 {
			t.Errorf("%s: note %+v, markup %+v", name, n, n.Markup)
		}
		if pp := n.Markup.Popup; pp == nil || pp.Index != 1 || pp.Open || !sameRect(pp.Bounds, Rect{100, 40, 150, 90}) {
			t.Errorf("%s: popup of the note %+v", name, pp)
		}
		if d.appearance(n, "") == nil {
			t.Errorf("%s: the note has no appearance", name)
		}
		k := &annots[4]
		if len(k.Markup.Ink) != 1 || !samePoints(k.Markup.Ink[0], [][2]float64{{20, 20}, {60, 80}}) || k.Markup.Opacity != 0.5 ||
			k.Markup.BorderWidth != 3 || textString(d.resolve(k.dict.Get("Mine"))) != "kept" {
			t.Errorf("%s: ink %+v, dict %v", name, k.Markup, k.dict)
		}
		if g := annots[5].Markup; annots[5].Contents != "a comment" || g.Popup == nil || g.Popup.Index != 11 || g.Intent != "PolygonCloud" {
			t.Errorf("%s: polygon %+v, popup %+v", name, g, g.Popup)
		}
		// The reply still replies to the note.
		if r := annots[2].Markup.Reply; r == nil || r.To != 0 {
			t.Errorf("%s: reply %+v", name, r)
		}
		if n, _ := pixDiff(before, renderState(t, d, nil)); n == 0 {
			t.Errorf("%s: the page renders as before", name)
		}
	}
}

// TestRemoveAnnotation removes an annotation with its popup and with what
// replies to it.
func TestRemoveAnnotation(t *testing.T) {
	src := markupFile()
	doc := editOpen(t, src)
	e := doc.Edit()
	added, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Text", Markup: Markup{Bounds: Rect{10, 10, 30, 30}, Reply: &Reply{To: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Caret", Markup: Markup{Bounds: Rect{10, 10, 30, 30}}})
	if err != nil {
		t.Fatal(err)
	}
	// The note: its popup, the reply, the free text grouped with it and
	// the reply just added go with it.
	if err := e.RemoveAnnotation(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveAnnotation(0, 8); err != nil { // the stamp
		t.Fatal(err)
	}
	if err := e.RemoveAnnotation(0, gone); err != nil {
		t.Fatal(err)
	}
	if !e.annotated() {
		t.Error("the editor has no changes")
	}
	for _, index := range []int{0, 11 + 2, -1, gone} {
		if err := e.RemoveAnnotation(0, index); err == nil {
			t.Errorf("annotation %d was removed", index)
		}
	}
	if err := e.SetAnnotation(0, 0, NewAnnotation{Subtype: "Text", Markup: Markup{Bounds: Rect{10, 10, 30, 30}}}); err == nil {
		t.Error("a removed annotation was set")
	}
	if _, err := e.AddAnnotation(0, NewAnnotation{Subtype: "Text", Markup: Markup{Bounds: Rect{10, 10, 30, 30}, Reply: &Reply{To: 0}}}); err == nil {
		t.Error("a reply to a removed annotation was added")
	}
	_ = added
	for name, out := range map[string][]byte{"save": editSave(t, e), "update": editUpdate(t, e, src)} {
		// The widget is in no form: an annotation like others.
		if s := subtypes(t, editOpen(t, out)); s != "Highlight Ink Polygon Line Link Widget" {
			t.Errorf("%s: annotations left: %s", name, s)
		}
	}
	// On another page, and on one that is not the document's.
	e = doc.Edit()
	if err := e.ImportPages(1, doc, EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveAnnotation(1, 0); err == nil || !strings.Contains(err.Error(), "not a page of the edited document") {
		t.Errorf("an annotation of an imported page was removed: %v", err)
	}
	if err := e.RemoveAnnotation(5, 0); err == nil {
		t.Error("an annotation of page 5 was removed")
	}
}

// TestAddAnnotationErrors refuses what cannot be drawn or written.
func TestAddAnnotationErrors(t *testing.T) {
	e := editOpen(t, plainDoc(0)).Edit()
	box := Rect{10, 10, 50, 50}
	for name, a := range map[string]NewAnnotation{
		"type":        {Subtype: "Sound", Markup: Markup{Bounds: box}},
		"widget":      {Subtype: "Widget", Markup: Markup{Bounds: box}},
		"no bounds":   {Subtype: "Text"},
		"empty":       {Subtype: "Square", Markup: Markup{Bounds: Rect{10, 10, 10, 50}, BorderWidth: 1}},
		"invisible":   {Subtype: "Square", Markup: Markup{Bounds: box}},
		"NaN":         {Subtype: "Circle", Markup: Markup{Bounds: Rect{math.NaN(), 0, 10, 10}, BorderWidth: 1}},
		"colour":      {Subtype: "Text", Markup: Markup{Bounds: box, Color: []float64{1, 0}}},
		"component":   {Subtype: "Text", Markup: Markup{Bounds: box, Color: []float64{2}}},
		"opacity":     {Subtype: "Text", Markup: Markup{Bounds: box, Opacity: -1}},
		"no quads":    {Subtype: "Highlight", Markup: Markup{Quads: []Quad{QuadOf(Rect{10, 10, 10, 20})}}},
		"no ink":      {Subtype: "Ink", Markup: Markup{Ink: [][][2]float64{{}}, BorderWidth: 1}},
		"thin ink":    {Subtype: "Ink", Markup: Markup{Ink: [][][2]float64{{{1, 1}, {2, 2}}}}},
		"polygon":     {Subtype: "Polygon", Markup: Markup{Vertices: [][2]float64{{1, 1}, {2, 2}}, BorderWidth: 1}},
		"line":        {Subtype: "Line", Markup: Markup{Line: [][2]float64{{1, 1}}, BorderWidth: 1}},
		"stamp":       {Subtype: "Stamp", Markup: Markup{Bounds: box, Icon: "Mine"}},
		"callout":     {Subtype: "FreeText", Markup: Markup{Bounds: box, Callout: [][2]float64{{1, 1}}}},
		"no target":   {Subtype: "Link", Markup: Markup{Bounds: box}},
		"target page": {Subtype: "Link", Link: &LinkTarget{Page: 7}, Markup: Markup{Bounds: box}},
	} {
		if _, err := e.AddAnnotation(0, a); err == nil {
			t.Errorf("%s: added", name)
		}
	}
	if _, err := e.AddAnnotation(2, NewAnnotation{Subtype: "Text", Markup: Markup{Bounds: box}}); err == nil {
		t.Error("an annotation was added to page 2")
	}
	if e.annotated() {
		t.Error("the editor has changes")
	}
	if _, err := NewEditor().AddAnnotation(0, NewAnnotation{Subtype: "Text", Markup: Markup{Bounds: box}}); err == nil {
		t.Error("an annotation was added to a new document")
	}
}

// TestAnnotationPermissions refuses changes to annotations that the
// permissions or a certification of the document do not allow.
func TestAnnotationPermissions(t *testing.T) {
	const all = int32(-4)
	note := NewAnnotation{Subtype: "Text", Markup: Markup{Bounds: Rect{10, 10, 30, 30}}}
	for _, tt := range []struct {
		name string
		perm int32
		want error
	}{
		{"annotate", all &^ (1 << 3), nil},
		{"modify", all &^ (1 << 5), nil},
		{"neither", all &^ (1<<3 | 1<<5), ErrNotPermitted},
	} {
		e := editOpen(t, testpdf.EncryptedWith("aes128", "", tt.perm)).Edit()
		if _, err := e.AddAnnotation(0, note); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := e.Update(&out); !errors.Is(err, tt.want) {
			t.Errorf("%s: err %v, want %v", tt.name, err, tt.want)
		}
		// The widget of the form is not removed as an annotation.
		if err := e.RemoveAnnotation(0, 0); err == nil {
			t.Errorf("%s: a widget of the form was removed", tt.name)
		}
	}
	for mdp, want := range map[int]error{0: nil, 1: ErrCertified, 2: ErrCertified, 3: nil} {
		e := editOpen(t, signedFile(true, mdp, false)).Edit()
		if _, err := e.AddAnnotation(0, note); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := e.Update(&out); !errors.Is(err, want) {
			t.Errorf("DocMDP %d: err %v, want %v", mdp, err, want)
		}
	}
}
