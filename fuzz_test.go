package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Fuzz targets for the readers of untrusted structure of ADRs 0001–0008
// (ADR 0010): tiling pattern cells, ICC profiles, optional content and
// visibility expressions, AcroForm trees. Meshes are fuzzed by
// FuzzShading, CMaps by internal/cmap's FuzzParse, CMYK profiles also by
// internal/cmyk's FuzzParse.

// fuzzRender opens data and renders every page serially and from the
// cached display list in parallel bands; only a panic fails.
func fuzzRender(t *testing.T, data []byte, opt RenderOptions) *Document {
	t.Helper()
	doc, err := Open(data)
	if err != nil {
		return nil
	}
	for i := range min(doc.NumPages(), 4) {
		p, err := doc.Page(i)
		if err != nil {
			continue
		}
		dst := image.NewRGBA(p.Bounds(0.5))
		for _, workers := range []int{1, 3} {
			o := opt
			o.Scale, o.Workers, o.Deadline = 0.5, workers, time.Now().Add(2*time.Second)
			err = p.Render(context.Background(), dst, o)
			var pe *PanicError
			if errors.As(err, &pe) {
				t.Fatalf("%v\n%s", pe.Value, pe.Stack)
			}
		}
	}
	return doc
}

func FuzzPattern(f *testing.F) {
	for _, s := range []struct {
		dict, cell string
	}{
		{"/PaintType 1 /TilingType 1 /BBox [0 0 10 10] /XStep 10 /YStep 10", "1 0 0 rg 0 0 2 2 re f"},
		{"/PaintType 2 /TilingType 2 /BBox [-5 -5 15 15] /XStep 10 /YStep 7 /Matrix [0.3 0.1 -0.1 0.3 5 5]", "0 0 m 10 10 l S"},
		{"/PaintType 1 /TilingType 3 /BBox [0 0 1000 1000] /XStep 1e-3 /YStep -4", "/Pattern cs /P scn 0 0 5 5 re f"},
		{"/PaintType 1 /BBox [0 0 10 10] /XStep 10 /YStep 10 /Matrix [100 0 0 100 0 0]", "/Sh sh BT /F1 9 Tf (A) Tj ET /X Do"},
	} {
		f.Add(s.dict, s.cell)
	}
	f.Fuzz(func(t *testing.T, dict, cell string) {
		res := "/Resources << /Pattern << /P 100 0 R >> /Shading << /Sh 101 0 R >> /Font << /F1 102 0 R >> /XObject << /X 103 0 R >> >>"
		pat := streamObj("/PatternType 1 "+dict+" "+res, []byte(cell))
		data := buildPDF([]string{
			"/Pattern cs /P scn 0 0 200 100 re f",
			"q 7 0 0 3 10 10 cm /Pattern CS /P SCN 4 w 0 0 m 20 20 l S Q BT /F1 30 Tf /Pattern cs /P scn (Hi) Tj ET",
			"[/Pattern /DeviceRGB] cs 0.2 0.4 0.6 /P scn 0 0 100 50 re f",
		}, res, pat,
			"<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 10 0] /Function << /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >> >>",
			helvetica,
			stream("/Type /XObject /Subtype /Form /BBox [0 0 10 10] "+res, "/Pattern cs /P scn 0 0 10 10 re f"))
		fuzzRender(t, data, RenderOptions{})
	})
}

func FuzzICC(f *testing.F) {
	f.Add(iccBytes(2.2), 3)
	f.Add(iccBytes(1), 1)
	f.Add(iccBytes(0.01)[:140], 4)
	for _, name := range []string{"toy-lut16.icc", "swop-mab.icc"} {
		if b, err := os.ReadFile(filepath.Join("internal", "cmyk", "testdata", name)); err == nil {
			f.Add(b, 4)
		}
	}
	f.Fuzz(func(t *testing.T, profile []byte, n int) {
		n = []int{1, 3, 4}[uint(n)%3]
		if cs, _ := iccProfile(profile, n); cs != nil {
			for _, v := range [][]float64{{0, 0, 0, 0}, {1, 1, 1, 1}, {0.5, -1, 2, 0.3}} {
				cs.rgb(v[:n])
			}
		}
		alt := []string{1: "/DeviceGray", 3: "/DeviceRGB", 4: "/DeviceCMYK"}[n]
		comps := []string{1: "0.5", 3: "0.2 0.5 0.9", 4: "0.1 0.2 0.3 0.4"}[n]
		data := buildPDF([]string{
			"/C cs " + comps + " sc 0 0 100 100 re f /C CS " + comps + " SC 0 0 m 200 100 l S",
			"q 100 0 0 50 0 0 cm /Im Do Q /Sh sh",
		}, "/Resources << /ColorSpace << /C [/ICCBased 100 0 R] >> /XObject << /Im 101 0 R >> /Shading << /Sh 102 0 R >> >>",
			streamObj(fmt.Sprintf("/N %d /Alternate %s", n, alt), profile),
			streamObj("/Type /XObject /Subtype /Image /Width 2 /Height 1 /BitsPerComponent 8 /ColorSpace [/ICCBased 100 0 R]",
				make([]byte, 2*n)),
			"<< /ShadingType 2 /ColorSpace [/ICCBased 100 0 R] /Coords [0 0 200 0] /Function << /FunctionType 2 /Domain [0 1] /N 1 >> >>")
		fuzzRender(t, data, RenderOptions{})
	})
}

func FuzzLayers(f *testing.F) {
	f.Add(`/OCGs [100 0 R 101 0 R] /D << /OFF [101 0 R] /Order [100 0 R [101 0 R]] /RBGroups [[100 0 R 101 0 R]] >>`,
		`<< /Type /OCMD /VE [/And 100 0 R [/Not 101 0 R]] >>`)
	f.Add(`/OCGs [100 0 R] /D << /BaseState /OFF /AS [<< /Event /View /OCGs [100 0 R] /Category [/Zoom] >>] >> /Configs [<< /ON [100 0 R] >>]`,
		`<< /Type /OCMD /OCGs [100 0 R 101 0 R 102 0 R] /P /AnyOff >>`)
	f.Add(`/OCGs [100 0 R 102 0 R] /D << /Order [[[[[[[[[[[[[[[[[[(x)]]]]]]]]]]]]]]]]]] >>`,
		`<< /Type /OCMD /VE [/Or [/Or [/Or [/Or [/Not 102 0 R]]]] 100 0 R] >>`)
	f.Fuzz(func(t *testing.T, props, ocmd string) {
		res := "/Resources << /Properties << /a 100 0 R /b 101 0 R /m 102 0 R >> /XObject << /X 103 0 R >> >>"
		data := buildPDFCatalog("/OCProperties << "+props+" >>", []string{
			"/OC /a BDC 0 0 1 rg 0 0 40 100 re f EMC /OC /m BDC 40 0 40 100 re f /OC /b BDC 1 0 0 rg 0 0 9 9 re f EMC EMC /X Do",
		}, res,
			"<< /Type /OCG /Name (A) /Usage << /View << /ViewState /OFF >> /Zoom << /min 1 /max 4 >> >> >>",
			"<< /Type /OCG /Name (B) >>",
			ocmd,
			stream("/Type /XObject /Subtype /Form /BBox [0 0 200 100] /OC 102 0 R", "0 1 0 rg 80 0 40 100 re f"))
		doc := fuzzRender(t, data, RenderOptions{})
		if doc == nil {
			return
		}
		for _, c := range doc.LayerConfigs() {
			v := c.Visibility()
			for _, l := range c.Layers {
				v = v.With(l, !v.Visible(l))
			}
			fuzzRender(t, data, RenderOptions{Layers: &v, Usage: UsagePrint})
		}
	})
}

func FuzzForm(f *testing.F) {
	f.Add("/Fields [100 0 R 101 0 R] /NeedAppearances true /DA (/Helv 0 Tf 0 g) "+helvDR,
		"<< /Subtype /Widget /FT /Tx /T (a) /Rect [10 10 110 40] /V (Hello) /Ff 4096 /MaxLen 3 /Q 1 >>",
		"<< /FT /Btn /Ff 49152 /T (r) /Kids [100 0 R] /V /x /Opt [(x) (y)] >>")
	f.Add("/Fields [101 0 R] "+helvDR,
		"<< /Subtype /Widget /Parent 101 0 R /Rect [0 0 50 50] /AS /On /MK << /CA (4) /R 270 >> >>",
		"<< /FT /Ch /Ff 131072 /T (c) /Kids [100 0 R 101 0 R] /Opt [[(a) (A)] 1 (B)] /V [(a) (B)] /I [0 7] /TI 9 >>")
	f.Add("/Fields [100 0 R] /XFA (x)",
		"<< /Subtype /Widget /FT /Tx /T (p) /Rect [0 0 1e9 -1e9] /V <FEFF0041> /Ff 8192 /DA (/Nope 1e30 Tf) >>",
		"<< /T (x) /Parent 101 0 R /Kids [101 0 R] >>")
	f.Fuzz(func(t *testing.T, acro, widget, field string) {
		data := formPDF(acro, "", []string{widget}, field)
		doc := fuzzRender(t, data, RenderOptions{})
		if doc == nil {
			return
		}
		form := doc.Form()
		if form == nil {
			return
		}
		st := form.NewState()
		for _, fl := range form.Fields {
			for _, v := range []Value{TextValue("Wörter 12345"), StateValue("On"), ChoiceValue(0, 2)} {
				_ = st.SetValue(fl, v)
			}
		}
		fuzzRender(t, data, RenderOptions{Form: st})
	})
}
