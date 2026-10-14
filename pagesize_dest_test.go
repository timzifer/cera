package cera

import (
	"math"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// destFile is two pages of 200 by 100 points; the first is the target of
// destinations of every kind: /OpenAction, the outline (a destination and
// a GoTo action), the old /Dests dictionary, the /Dests name tree, a link
// on the second page and one on the first itself.
func destFile() []byte {
	return testpdf.File{Objs: map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /OpenAction [3 0 R /XYZ 40 80 2] /Outlines 10 0 R" +
			" /Dests <</old [3 0 R /FitV 30]>> /Names <</Dests 20 0 R>>>>",
		2:  "<</Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /MediaBox [0 0 200 100]>>",
		3:  "<</Type /Page /Parent 2 0 R /Annots [30 0 R]>>",
		4:  "<</Type /Page /Parent 2 0 R /Annots [31 0 R]>>",
		10: "<</Type /Outlines /First 11 0 R /Last 12 0 R /Count 2>>",
		11: "<</Title (R) /Parent 10 0 R /Next 12 0 R /Dest [3 0 R /FitR 20 10 60 50]>>",
		12: "<</Title (H) /Parent 10 0 R /Prev 11 0 R /A <</S /GoTo /D [3 0 R /FitH 70]>>>>",
		20: "<</Names [(new) [3 0 R /FitBH 90] (other) [4 0 R /FitH 70]]>>",
		30: "<</Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest [3 0 R /XYZ 100 50 null]>>",
		31: "<</Type /Annot /Subtype /Link /Rect [0 0 10 10] /A <</S /GoTo /D [3 0 R /XYZ 10 20 0]>>>>",
	}, Trailer: "/Root 1 0 R"}.Bytes()
}

// destNums returns the numbers of a destination array, NaN for null.
func destNums(d *Document, o pdf.Object) (string, []float64) {
	a, _ := d.resolve(o).Array()
	if len(a) < 2 {
		return "", nil
	}
	mode, _ := d.name(a[1])
	var v []float64
	for _, e := range a[2:] {
		f, ok := d.num(e)
		if !ok {
			f = math.NaN()
		}
		v = append(v, f)
	}
	return string(mode), v
}

// TestResizeDestinations moves the destinations that lead to a resized
// page with it, wherever they are, when saved and when updated; those of
// other pages stay.
func TestResizeDestinations(t *testing.T) {
	src := destFile()
	for _, mode := range []string{"save", "update"} {
		t.Run(mode, func(t *testing.T) {
			e := editOpen(t, src).Edit()
			// To half its size: the matrix halves both ways.
			if err := e.ResizePage(0, 100, 50, FitStretch); err != nil {
				t.Fatal(err)
			}
			var out []byte
			if mode == "save" {
				out = editSave(t, e)
			} else {
				out = editUpdate(t, e, src)
			}
			d := editOpen(t, out)
			cat, _ := d.r.Catalog()
			outline := d.dict(cat.Get("Outlines"))
			first := d.dict(outline.Get("First"))
			second := d.dict(first.Get("Next"))
			names, _ := d.resolve(d.dict(d.dict(cat.Get("Names")).Get("Dests")).Get("Names")).Array()
			p1, _ := d.r.Page(1)
			p2, _ := d.r.Page(2)
			link := func(pd pdf.Dict) pdf.Dict {
				a, _ := d.resolve(pd.Get("Annots")).Array()
				return d.dict(a[0])
			}
			l1, l2 := link(p1), link(p2)
			destOf := func(ld pdf.Dict) pdf.Object {
				if v := ld.Get("Dest"); !v.IsNull() {
					return v
				}
				return d.dict(ld.Get("A")).Get("D")
			}
			nan := math.NaN()
			for _, c := range []struct {
				name string
				o    pdf.Object
				mode string
				want []float64
			}{
				{"open action", cat.Get("OpenAction"), "XYZ", []float64{20, 40, 2}},
				{"outline FitR", first.Get("Dest"), "FitR", []float64{10, 5, 30, 25}},
				{"outline GoTo", d.dict(second.Get("A")).Get("D"), "FitH", []float64{35}},
				{"old dests", d.dict(cat.Get("Dests")).Get("old"), "FitV", []float64{15}},
				{"name tree", names[1], "FitBH", []float64{45}},
				{"name tree, other page", names[3], "FitH", []float64{70}},
				{"link on the page", destOf(l1), "XYZ", []float64{50, 25, nan}},
				{"link to the page", destOf(l2), "XYZ", []float64{5, 10, 0}},
			} {
				m, v := destNums(d, c.o)
				ok := m == c.mode && len(v) == len(c.want)
				for i := 0; ok && i < len(v); i++ {
					ok = math.IsNaN(c.want[i]) && math.IsNaN(v[i]) || math.Abs(v[i]-c.want[i]) < 1e-3
				}
				if !ok {
					t.Errorf("%s: %s %v, want %s %v", c.name, m, v, c.mode, c.want)
				}
			}
		})
	}
}

// TestResizeDestinationsImported moves the destinations imported pages
// bring — outline, names, links — with the imported page they lead to.
func TestResizeDestinationsImported(t *testing.T) {
	e := NewEditor()
	if err := e.ImportPagesWith(0, editOpen(t, destFile()), ImportOptions{Outline: true, Destinations: true}, EditPage{Index: 0}, EditPage{Index: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.ResizePage(0, 100, 50, FitStretch); err != nil {
		t.Fatal(err)
	}
	d := editOpen(t, editSave(t, e))
	cat, _ := d.r.Catalog()
	first := d.dict(d.dict(cat.Get("Outlines")).Get("First"))
	p2, _ := d.r.Page(2)
	a, _ := d.resolve(p2.Get("Annots")).Array()
	if m, v := destNums(d, first.Get("Dest")); m != "FitR" || len(v) != 4 || v[0] != 10 || v[3] != 25 {
		t.Errorf("outline: %s %v", m, v)
	}
	if m, v := destNums(d, d.dict(a[0]).Get("Dest")); m != "XYZ" || len(v) != 3 || v[0] != 5 || v[1] != 10 {
		t.Errorf("link: %s %v", m, v)
	}
	if got := destPage(d, "new"); got != 0 {
		t.Errorf("the named destination leads to page %d", got)
	}
}

// TestResizeAnnotationMeasures scales what an annotation measures in its
// own terms with the page: border, leader lines, its own font size.
func TestResizeAnnotationMeasures(t *testing.T) {
	src := testpdf.File{Objs: map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R>>",
		2: "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100]>>",
		3: "<</Type /Page /Parent 2 0 R /Annots [10 0 R 11 0 R]>>",
		10: "<</Type /Annot /Subtype /Line /Rect [10 10 90 90] /L [20 20 80 80] /LL 10 /LLE 5 /LLO 2 /BS <</W 2 /D [3 1]>>" +
			" /Border [0 0 2]>>",
		11: "<</Type /Annot /Subtype /FreeText /Rect [100 10 190 90] /DA (/Helv 12 Tf 0 0 1 rg) /Contents (Note)>>",
	}, Trailer: "/Root 1 0 R"}.Bytes()
	e := editOpen(t, src).Edit()
	if err := e.ResizePage(0, 100, 50, FitStretch); err != nil {
		t.Fatal(err)
	}
	d := editOpen(t, editSave(t, e))
	p, _ := d.r.Page(1)
	a, _ := d.resolve(p.Get("Annots")).Array()
	line, text := d.dict(a[0]), d.dict(a[1])
	bs := d.dict(line.Get("BS"))
	for name, c := range map[string]struct {
		got  pdf.Object
		want string
	}{
		"W":      {bs.Get("W"), "1"},
		"D":      {bs.Get("D"), "[1.5 0.5]"},
		"Border": {line.Get("Border"), "[0 0 1]"},
		"LL":     {line.Get("LL"), "5"},
		"LLE":    {line.Get("LLE"), "2.5"},
		"LLO":    {line.Get("LLO"), "1"},
	} {
		if c.got.String() != c.want {
			t.Errorf("%s is %v, want %s", name, c.got, c.want)
		}
	}
	if da, _ := d.resolve(text.Get("DA")).Str(); string(da) != "/Helv 6 Tf 0 0 1 rg" {
		t.Errorf("/DA is %q", da)
	}
}
