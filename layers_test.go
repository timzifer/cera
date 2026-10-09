package cera

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"
)

// layersPDF has three groups: Walls (on), Dims (off) and Plot (on for
// viewing from zoom 2 on, off for printing), two membership dictionaries
// and a form that is optional content of its own.
//
// Page 0 has five columns 40 pt wide, filled blue in the content of
// Walls, Dims, Plot, "Walls and Dims both off" and "Walls and not Dims".
// Page 1 nests, clips and draws the form; page 2 shows text; page 3 names
// a property list that does not exist.
func layersPDF() []byte {
	catalog := `/OCProperties << /OCGs [100 0 R 101 0 R 102 0 R]
		/D << /Name (Default) /OFF [101 0 R] /Locked [100 0 R]
			/Order [100 0 R [101 0 R] [(Extra) 102 0 R]] /RBGroups [[100 0 R 101 0 R]]
			/AS [<< /Event /Print /OCGs [102 0 R] /Category [/Print] >>
				<< /Event /View /OCGs [102 0 R] /Category [/View /Zoom] >>] >>
		/Configs [<< /Name <FEFF0041006C006C0020006F00660066> /BaseState /OFF >>
			<< /Name (Same) /BaseState /Unchanged /ON [101 0 R] >>] >>`
	form := "0 0 1 rg 40 0 40 100 re f"
	pages := []string{
		`0 0 1 rg
		/OC /a BDC 0 0 40 100 re f EMC
		/OC /b BDC 40 0 40 100 re f EMC
		/OC /c BDC 80 0 40 100 re f EMC
		/OC /none BDC 120 0 40 100 re f EMC
		/OC /ve BDC 160 0 40 100 re f EMC`,
		`q /OC /b BDC 120 0 80 100 re W n EMC 0 1 0 rg 0 0 200 100 re f Q
		/OC /a BDC /OC /b BDC 1 0 0 rg 0 0 40 100 re f EMC EMC
		/X Do
		/Tag BMC /OC /b BDC EMC 0 0 0 rg 80 0 40 100 re f EMC EMC`,
		`/OC /b BDC BT /F1 12 Tf 10 50 Td (Hidden) Tj ET EMC BT /F1 12 Tf 10 20 Td (Shown) Tj ET`,
		`/OC /missing BDC 0 0 1 rg 0 0 40 100 re f EMC`,
	}
	res := `/Resources << /Properties << /a 100 0 R /b 101 0 R /c 102 0 R /none 103 0 R /ve 104 0 R >>
		/XObject << /X 105 0 R >> /Font << /F1 106 0 R >> >>`
	return buildPDFCatalog(catalog, pages, res,
		"<< /Type /OCG /Name (Walls) >>",
		"<< /Type /OCG /Name (Dims) >>",
		"<< /Type /OCG /Name (Plot) /Usage << /View << /ViewState /ON >> /Print << /PrintState /OFF >> /Zoom << /min 2 >> >> >>",
		"<< /Type /OCMD /OCGs [100 0 R 101 0 R] /P /AllOff >>",
		"<< /Type /OCMD /VE [/And 100 0 R [/Not 101 0 R]] >>",
		"<< /Type /XObject /Subtype /Form /BBox [0 0 200 100] /OC 101 0 R /Length "+itoa(len(form))+" >>\nstream\n"+form+"\nendstream",
		helvetica,
	)
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		if n /= 10; n == 0 {
			return string(b[i:])
		}
	}
}

func openLayers(t *testing.T) *Document {
	t.Helper()
	doc, err := Open(layersPDF())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// columns renders page 0 and reports which of its columns are blue.
func columns(t *testing.T, p *Page, opt RenderOptions) (blue [5]bool, st Stats) {
	t.Helper()
	opt.Background, opt.Stats = white, &st
	img := image.NewRGBA(p.Bounds(opt.Scale))
	if err := p.Render(context.Background(), img, opt); err != nil {
		t.Fatal(err)
	}
	s := normScale(opt.Scale)
	for i := range blue {
		c := img.RGBAAt(int((float64(i)*40+20)*s), int(50*s))
		switch c {
		case color.RGBA{0, 0, 255, 255}:
			blue[i] = true
		case white:
		default:
			t.Errorf("column %d is %v", i, c)
		}
	}
	return blue, st
}

func TestLayersDefault(t *testing.T) {
	doc := openLayers(t)
	p, _ := doc.Page(0)
	got, st := columns(t, p, RenderOptions{})
	if want := [5]bool{true, false, false, false, true}; got != want {
		t.Errorf("columns %v, want %v", got, want)
	}
	if len(st.Unsupported) != 0 {
		t.Errorf("unsupported %v", st.Unsupported)
	}
}

func TestLayersToggle(t *testing.T) {
	doc := openLayers(t)
	p, _ := doc.Page(0)
	columns(t, p, RenderOptions{})
	cfg := doc.Layers()
	walls, dims := cfg.Layers[0], cfg.Layers[1]
	def := cfg.Visibility()
	off := def.With(walls, false)
	got, st := columns(t, p, RenderOptions{Layers: &off})
	if want := [5]bool{false, false, false, true, false}; got != want {
		t.Errorf("walls off: columns %v, want %v", got, want)
	}
	if !st.Reused {
		t.Error("toggling a layer interpreted the page again")
	}
	on := def.With(dims, true)
	if got, _ := columns(t, p, RenderOptions{Layers: &on}); got != [5]bool{true, true, false, false, false} {
		t.Errorf("dims on: columns %v", got)
	}
	if !def.Visible(walls) || def.Visible(dims) {
		t.Error("With changed the visibility it was called on")
	}
	all := Visibility{}
	if got, _ := columns(t, p, RenderOptions{Layers: &all}); got != [5]bool{true, true, true, false, false} {
		t.Errorf("zero Visibility: columns %v", got)
	}
}

func TestLayersUsage(t *testing.T) {
	doc := openLayers(t)
	p, _ := doc.Page(0)
	if got, _ := columns(t, p, RenderOptions{Scale: 2}); !got[2] {
		t.Error("Plot hidden at zoom 2")
	}
	if got, _ := columns(t, p, RenderOptions{Scale: 2, Usage: UsagePrint}); got[2] {
		t.Error("Plot printed")
	}
	plot := doc.Layers().Layers[2]
	v := doc.Layers().Visibility().With(plot, true) // drops the zoom limit
	if got, _ := columns(t, p, RenderOptions{Layers: &v}); !got[2] {
		t.Error("Plot switched on is hidden at zoom 1")
	}
}

func TestLayerConfig(t *testing.T) {
	doc := openLayers(t)
	c := doc.Layers()
	if c == nil {
		t.Fatal("no layers")
	}
	var names []string
	for _, l := range c.Layers {
		names = append(names, l.Name)
	}
	if c.Name != "Default" || strings.Join(names, ",") != "Walls,Dims,Plot" {
		t.Errorf("config %q, layers %v", c.Name, names)
	}
	if !c.Layers[0].Visible || c.Layers[1].Visible || !c.Layers[2].Visible || !c.Layers[0].Locked || c.Layers[1].Locked {
		t.Errorf("states %+v %+v %+v", *c.Layers[0], *c.Layers[1], *c.Layers[2])
	}
	o := c.Order
	if len(o) != 2 || o[0].Layer != c.Layers[0] || len(o[0].Children) != 1 || o[0].Children[0].Layer != c.Layers[1] ||
		o[1].Label != "Extra" || len(o[1].Children) != 1 || o[1].Children[0].Layer != c.Layers[2] {
		t.Errorf("order %+v", o)
	}
	if len(c.RBGroups) != 1 || len(c.RBGroups[0]) != 2 {
		t.Errorf("radio groups %v", c.RBGroups)
	}
	if c.VisibilityFor(UsagePrint).Visible(c.Layers[2]) {
		t.Error("Plot visible for printing")
	}
	cs := doc.LayerConfigs()
	if len(cs) != 2 || cs[0].Name != "All off" || cs[1].Name != "Same" {
		t.Fatalf("configs %v", cs)
	}
	for i, l := range cs[0].Layers {
		if l.Visible || cs[0].Visibility().Visible(l) {
			t.Errorf("All off: layer %d visible", i)
		}
	}
	for i, l := range cs[1].Layers {
		if !l.Visible {
			t.Errorf("Same: layer %d off", i)
		}
	}
}

func TestLayersNesting(t *testing.T) {
	doc := openLayers(t)
	p, _ := doc.Page(1)
	check := func(img *image.RGBA, name string, want [5]color.RGBA) {
		t.Helper()
		for i, c := range want {
			if got := img.RGBAAt(i*40+20, 50); got != c {
				t.Errorf("%s: column %d is %v, want %v", name, i, got, c)
			}
		}
	}
	img, st, err := renderPage(t, layersPDF(), 1, RenderOptions{Background: white})
	if err != nil {
		t.Fatal(err)
	}
	// The clip in hidden content clips; the nested red and the form are
	// hidden; the black after the inner EMC is not.
	check(img, "default", [5]color.RGBA{white, white, black, green, green})
	if st.Errors != 0 || len(st.Unsupported) != 0 {
		t.Errorf("errors %d, unsupported %v", st.Errors, st.Unsupported)
	}
	v := doc.Layers().Visibility().With(doc.Layers().Layers[1], true)
	img = image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), img, RenderOptions{Background: white, Layers: &v}); err != nil {
		t.Fatal(err)
	}
	check(img, "dims on", [5]color.RGBA{red, blue, black, green, green})
}

func TestLayersHideGroups(t *testing.T) {
	// A hidden object with a blend mode, and a hidden transparency group
	// with a nested clip, followed by visible content.
	form := "q 0 0 20 100 re W n 0 0 1 rg 0 0 200 100 re f Q"
	c := `q /OC /a BDC /M gs 0 0 1 rg 0 0 40 100 re f EMC Q
		/OC /a BDC /G Do EMC
		1 0 0 rg 100 0 40 100 re f`
	data := buildPDFCatalog("/OCProperties << /OCGs [100 0 R] /D << /OFF [100 0 R] >> >>", []string{c},
		"/Resources << /Properties << /a 100 0 R >> /ExtGState << /M << /BM /Multiply /ca 0.5 >> >> /XObject << /G 101 0 R >> >>",
		"<< /Type /OCG /Name (A) >>",
		"<< /Type /XObject /Subtype /Form /BBox [0 0 200 100] /Group << /S /Transparency /I true >> /Length "+itoa(len(form))+" >>\nstream\n"+form+"\nendstream")
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	for _, workers := range []int{1, 4} {
		img := image.NewRGBA(p.Bounds(1))
		if err := p.Render(context.Background(), img, RenderOptions{Background: white, Workers: workers}); err != nil {
			t.Fatal(err)
		}
		assertPixel(t, img, 10, 50, white)
		assertPixel(t, img, 30, 50, white)
		assertPixel(t, img, 120, 50, red)
		v := doc.Layers().Visibility().With(doc.Layers().Layers[0], true)
		if err := p.Render(context.Background(), img, RenderOptions{Background: white, Workers: workers, Layers: &v}); err != nil {
			t.Fatal(err)
		}
		assertPixel(t, img, 10, 50, blue)
		if c := img.RGBAAt(30, 50); c == white || c == blue {
			t.Errorf("multiplied half-transparent blue is %v", c)
		}
		assertPixel(t, img, 60, 50, white)
		assertPixel(t, img, 120, 50, red)
	}
}

// countDevice counts what reaches it.
type countDevice struct {
	textDevice
	fills, clips, pops, shadings int
}

func (d *countDevice) FillShading(*Shading, Matrix, *Paint) { d.shadings++ }

func (d *countDevice) FillPath(*Path, Matrix, FillRule, *Paint) { d.fills++ }
func (d *countDevice) ClipPath(*Path, Matrix, FillRule)         { d.clips++ }
func (d *countDevice) ClipRect(Rect, Matrix)                    { d.clips++ }
func (d *countDevice) PopClip()                                 { d.pops++ }

func TestLayersRun(t *testing.T) {
	doc := openLayers(t)
	p, _ := doc.Page(1)
	var d countDevice
	if err := p.Run(context.Background(), &d, 1, nil); err != nil {
		t.Fatal(err)
	}
	// Green and black; the clips are the page box and the one in hidden
	// content. The hidden form is not run.
	if d.fills != 2 || d.clips != 2 || d.pops != d.clips {
		t.Errorf("fills %d, clips %d, pops %d", d.fills, d.clips, d.pops)
	}

	p, _ = doc.Page(2)
	text, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := text.String(); s != "Shown" {
		t.Errorf("text %q", s)
	}
	v := doc.Layers().Visibility().With(doc.Layers().Layers[1], true)
	var td textDevice
	if err := p.RunWith(context.Background(), &td, RunOptions{Layers: &v}); err != nil {
		t.Fatal(err)
	}
	if s := (&PageText{Chars: td.chars}).String(); s != "Hidden\n\nShown" {
		t.Errorf("text with Dims on %q", s)
	}
}

func TestLayersBadAndAbsent(t *testing.T) {
	_, st, _ := renderPage(t, layersPDF(), 3, RenderOptions{Background: white})
	if st.Unsupported["oc-bad"] != 1 {
		t.Errorf("unsupported %v", st.Unsupported)
	}
	// Without /OCProperties optional content is ignored.
	data := buildPDF([]string{"/OC /x BDC 0 0 1 rg 0 0 40 100 re f EMC"},
		"/Resources << /Properties << /x 100 0 R >> >>", "<< /Type /OCG /Name (X) >>")
	img, st, _ := renderPage(t, data, 0, RenderOptions{Background: white})
	assertPixel(t, img, 20, 50, blue)
	if len(st.Unsupported) != 0 {
		t.Errorf("unsupported %v", st.Unsupported)
	}
	doc, _ := Open(data)
	if doc.Layers() != nil || doc.LayerConfigs() != nil {
		t.Error("layers without /OCProperties")
	}
}

func TestLayersSteadyStateAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items under the race detector")
	}
	doc := openLayers(t)
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(1))
	v := doc.Layers().Visibility().With(doc.Layers().Layers[0], false)
	opt := RenderOptions{Workers: 1, Layers: &v}
	if err := p.Render(context.Background(), dst, opt); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		if err := p.Render(context.Background(), dst, opt); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 1 {
		t.Errorf("%v allocations per cached render", allocs)
	}
}

func TestLayersHideShadings(t *testing.T) {
	// An sh and a fill in a shading pattern, both in a layer that is off,
	// then the same pattern outside it.
	c := `/OC /a BDC q 0 0 40 100 re W n /S sh Q EMC
		/OC /a BDC /Pattern cs /P scn 40 0 40 100 re f EMC
		/Pattern cs /P scn 120 0 40 100 re f`
	data := buildPDFCatalog("/OCProperties << /OCGs [100 0 R] /D << /OFF [100 0 R] >> >>", []string{c},
		"/Resources << /Properties << /a 100 0 R >> /Shading << /S 101 0 R >> /Pattern << /P 102 0 R >> >>",
		"<< /Type /OCG /Name (A) >>",
		"<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 200 0] /Function << /FunctionType 2 /Domain [0 1] /C0 [0 0 1] /C1 [0 0 1] /N 1 >> >>",
		"<< /PatternType 2 /Shading 101 0 R >>")
	doc, _ := Open(data)
	p, _ := doc.Page(0)
	img := image.NewRGBA(p.Bounds(1))
	if err := p.Render(context.Background(), img, RenderOptions{Background: white}); err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, 20, 50, white)
	assertPixel(t, img, 60, 50, white)
	assertPixel(t, img, 140, 50, blue)
	v := doc.Layers().Visibility().With(doc.Layers().Layers[0], true)
	if err := p.Render(context.Background(), img, RenderOptions{Background: white, Layers: &v}); err != nil {
		t.Fatal(err)
	}
	assertPixel(t, img, 20, 50, blue)
	assertPixel(t, img, 60, 50, blue)
	var d countDevice
	if err := p.Run(context.Background(), &d, 1, nil); err != nil {
		t.Fatal(err)
	}
	if d.shadings != 1 {
		t.Errorf("Run passed %d shadings, want the visible one", d.shadings)
	}
}
