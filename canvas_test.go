package cera

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/go-opentype/fonts/arimo"
	"github.com/timzifer/cera/internal/stdfont"
	"github.com/timzifer/cera/internal/testpdf"
)

// pixelAt returns the pixel of img, rendered at scale 2, at a point of the
// displayed page.
func pixelAt(img *image.RGBA, x, y float64) color.RGBA {
	return img.RGBAAt(img.Rect.Min.X+int(x*2), img.Rect.Min.Y+int(y*2))
}

// wantPixels checks pixels of the first page of out.
func wantPixels(t *testing.T, name string, out []byte, page int, want map[[2]float64]color.RGBA) {
	t.Helper()
	img, _ := editRender(t, editOpen(t, out), page)
	for at, c := range want {
		if got := pixelAt(img, at[0], at[1]); !near(got, c, 6) {
			t.Errorf("%s: at %v the page is %v, want %v", name, at, got, c)
		}
	}
}

// pageString returns the text of page i.
func pageString(t *testing.T, d *Document, i int) string {
	t.Helper()
	p, err := d.Page(i)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := p.Text(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return pt.String()
}

// canvasBase is a page of 200 by 100 with a black box in its middle, from
// (50, 20) to (150, 80), drawn under a transform the content does not take
// back.
func canvasBase(rotate string) []byte {
	return buildPDF([]string{"0.5 0 0 0.5 0 0 cm 0 0 0 rg 100 40 200 120 re f"}, "/Rotate "+rotate)
}

// TestCanvasNewDocument makes a document of a blank page with shapes, text
// and an image on it.
func TestCanvasNewDocument(t *testing.T) {
	bold, err := StandardFont("Helvetica-Bold")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := NewCanvasFont(arimo.TTF)
	if err != nil {
		t.Fatal(err)
	}
	pix := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < len(pix.Pix); i += 4 {
		copy(pix.Pix[i:], []byte{0, 0, 255, 255})
	}
	e := NewEditor()
	if err := e.AddPage(0, 200, 100); err != nil {
		t.Fatal(err)
	}
	var widths [2]float64
	err = e.Draw(0, true, func(c *Canvas) {
		if w, h := c.Size(); w != 200 || h != 100 {
			t.Errorf("canvas of %v by %v", w, h)
		}
		c.FillRect(Rect{10, 10, 60, 40}, red)
		var tri Path
		tri.MoveTo(100, 10)
		tri.LineTo(150, 10)
		tri.LineTo(125, 40)
		tri.Close()
		c.Fill(&tri, NonZero, blue)
		var line Path
		line.MoveTo(10, 60)
		line.LineTo(190, 60)
		c.Stroke(&line, StrokeStyle{Width: 6}, green)
		widths[0] = c.DrawText("Hello Wörld", 10, 90, bold, 12, color.Black)
		widths[1] = c.DrawText("Γειά Привет", 110, 90, embedded, 12, nil)
		c.DrawImage(NewCanvasImage(pix), Rect{160, 10, 190, 40})
	})
	if err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	d := editOpen(t, out)
	p, _ := d.Page(0)
	if w, h := p.Size(); d.NumPages() != 1 || w != 200 || h != 100 {
		t.Fatalf("%d pages, the first %v by %v", d.NumPages(), w, h)
	}
	wantPixels(t, "new", out, 0, map[[2]float64]color.RGBA{
		{30, 25}: red, {125, 20}: blue, {100, 60}: green, {175, 25}: blue,
		{5, 5}: white, {80, 25}: white, {105, 35}: white,
	})
	text := pageString(t, d, 0)
	for _, s := range []string{"Hello Wörld", "Γειά Привет"} {
		if !strings.Contains(text, s) {
			t.Errorf("text %q lacks %q", text, s)
		}
	}
	if w := bold.Width("Hello Wörld", 12); w != widths[0] || w < 50 || w > 80 {
		t.Errorf("width %v, drawn %v", w, widths[0])
	}
	if w := embedded.Width("Γειά Привет", 12); w != widths[1] || w < 50 || w > 80 {
		t.Errorf("width %v, drawn %v", w, widths[1])
	}
	// The font is a subset.
	if len(out) > len(arimo.TTF)/4 {
		t.Errorf("the file has %d bytes, the font %d", len(out), len(arimo.TTF))
	}
	// Saved again, it is the same document.
	again, _ := editRender(t, editOpen(t, editSave(t, e)), 0)
	first, _ := editRender(t, d, 0)
	if !bytes.Equal(first.Pix, again.Pix) {
		t.Error("saved again, the page renders unlike before")
	}
}

// TestCanvasOverUnder draws under and over the content of a page of the
// edited document, of an imported page, and in an update.
func TestCanvasOverUnder(t *testing.T) {
	base := canvasBase("0")
	draw := func(t *testing.T, e *Editor) {
		t.Helper()
		if err := e.Draw(0, false, func(c *Canvas) { c.FillRect(Rect{0, 0, 200, 100}, red) }); err != nil {
			t.Fatal(err)
		}
		if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{90, 40, 110, 60}, blue) }); err != nil {
			t.Fatal(err)
		}
		// Later drawings lie over earlier ones.
		if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{100, 40, 110, 60}, green) }); err != nil {
			t.Fatal(err)
		}
	}
	want := map[[2]float64]color.RGBA{
		{10, 10}: red, {60, 30}: black, {95, 50}: blue, {105, 50}: green, {140, 70}: black, {190, 90}: red,
	}
	t.Run("save", func(t *testing.T) {
		e := editOpen(t, base).Edit()
		draw(t, e)
		wantPixels(t, "save", editSave(t, e), 0, want)
	})
	t.Run("update", func(t *testing.T) {
		e := editOpen(t, base).Edit()
		draw(t, e)
		wantPixels(t, "update", editUpdate(t, e, base), 0, want)
	})
	t.Run("imported", func(t *testing.T) {
		e := NewEditor()
		if err := e.ImportPages(0, editOpen(t, base), EditPage{Index: 0}); err != nil {
			t.Fatal(err)
		}
		draw(t, e)
		wantPixels(t, "imported", editSave(t, e), 0, want)
	})
	t.Run("moved and deleted", func(t *testing.T) {
		e := editOpen(t, buildPDF([]string{"", ""}, "")).Edit()
		if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{0, 0, 200, 100}, red) }); err != nil {
			t.Fatal(err)
		}
		if err := e.Draw(1, true, func(c *Canvas) { c.FillRect(Rect{0, 0, 200, 100}, blue) }); err != nil {
			t.Fatal(err)
		}
		if err := e.AddPage(0, 50, 50); err != nil {
			t.Fatal(err)
		}
		if err := e.MovePage(2, 0); err != nil {
			t.Fatal(err)
		}
		out := editSave(t, e)
		wantPixels(t, "moved", out, 0, map[[2]float64]color.RGBA{{10, 10}: blue})
		wantPixels(t, "moved", out, 1, map[[2]float64]color.RGBA{{10, 10}: white})
		wantPixels(t, "moved", out, 2, map[[2]float64]color.RGBA{{10, 10}: red})
		if err := e.DeletePages(0); err != nil {
			t.Fatal(err)
		}
		out = editSave(t, e)
		if d := editOpen(t, out); d.NumPages() != 2 {
			t.Fatalf("%d pages", d.NumPages())
		}
		wantPixels(t, "deleted", out, 1, map[[2]float64]color.RGBA{{10, 10}: red})
	})
}

// TestCanvasRotated draws in the points of the page as it is displayed,
// and the drawing turns with the page afterwards.
func TestCanvasRotated(t *testing.T) {
	corner := func(c *Canvas) { c.FillRect(Rect{0, 0, 20, 20}, red) }
	for _, rotate := range []string{"0", "90", "180", "270"} {
		base := canvasBase(rotate)
		e := editOpen(t, base).Edit()
		w, h := 200.0, 100.0
		if rotate == "90" || rotate == "270" {
			w, h = h, w
		}
		err := e.Draw(0, true, func(c *Canvas) {
			if cw, ch := c.Size(); cw != w || ch != h {
				t.Errorf("/Rotate %s: canvas of %v by %v", rotate, cw, ch)
			}
			corner(c)
		})
		if err != nil {
			t.Fatal(err)
		}
		wantPixels(t, "/Rotate "+rotate, editUpdate(t, e, base), 0, map[[2]float64]color.RGBA{
			{10, 10}: red, {w - 10, 10}: white, {10, h - 10}: white, {w - 10, h - 10}: white,
		})
		// Turned a quarter more, the corner is the top right one.
		if err := e.RotatePage(0, 90); err != nil {
			t.Fatal(err)
		}
		wantPixels(t, "/Rotate "+rotate+" and turned", editSave(t, e), 0, map[[2]float64]color.RGBA{
			{h - 10, 10}: red, {10, 10}: white, {10, w - 10}: white, {h - 10, w - 10}: white,
		})
		// Drawn on the turned page, it is the top left one again.
		if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{0, 0, 20, 20}, blue) }); err != nil {
			t.Fatal(err)
		}
		wantPixels(t, "/Rotate "+rotate+", turned and drawn on", editSave(t, e), 0, map[[2]float64]color.RGBA{
			{h - 10, 10}: red, {10, 10}: blue,
		})
	}
	// An imported page turned on the way, and a blank page turned.
	e := NewEditor()
	if err := e.ImportPages(0, editOpen(t, canvasBase("0")), EditPage{Index: 0, Rotate: 90}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddPage(1, 200, 100); err != nil {
		t.Fatal(err)
	}
	if err := e.RotatePage(1, 270); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := e.Draw(i, true, corner); err != nil {
			t.Fatal(err)
		}
	}
	out := editSave(t, e)
	for i := range 2 {
		p, _ := editOpen(t, out).Page(i)
		if w, h := p.Size(); w != 100 || h != 200 {
			t.Errorf("page %d is %v by %v", i, w, h)
		}
		wantPixels(t, "turned", out, i, map[[2]float64]color.RGBA{{10, 10}: red, {90, 10}: white, {10, 190}: white})
	}
}

// TestCanvasState keeps and restores the transform, the clip, the opacity
// and the blend mode.
func TestCanvasState(t *testing.T) {
	e := NewEditor()
	if err := e.AddPage(0, 200, 100); err != nil {
		t.Fatal(err)
	}
	err := e.Draw(0, true, func(c *Canvas) {
		c.FillRect(Rect{0, 0, 100, 100}, black)
		c.FillRect(Rect{100, 0, 200, 100}, color.RGBA{0, 255, 255, 255})

		c.Save()
		var clip Path
		clip.Rect(0, 0, 20, 20)
		c.Clip(&clip, NonZero)
		c.FillRect(Rect{0, 0, 100, 40}, red)
		c.Restore()

		c.Save()
		c.Transform(Matrix{1, 0, 0, 1, 40, 0})
		c.FillRect(Rect{0, 0, 10, 10}, green)
		c.Restore()

		c.Save()
		c.SetOpacity(0.5)
		c.FillRect(Rect{0, 50, 20, 70}, white)
		c.Save() // left open: restored when the drawing ends
		c.Restore()
		c.Restore()
		c.Restore() // one too many
		c.FillRect(Rect{30, 50, 50, 70}, white)
		c.FillRect(Rect{60, 50, 80, 70}, color.NRGBA{255, 255, 255, 128})

		c.SetBlendMode(BlendMultiply)
		c.FillRect(Rect{120, 20, 180, 80}, color.RGBA{255, 255, 0, 255})
		c.Save()
	})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing of the first drawing's state reaches the next.
	if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{0, 80, 20, 100}, white) }); err != nil {
		t.Fatal(err)
	}
	grey := color.RGBA{128, 128, 128, 255}
	wantPixels(t, "state", editSave(t, e), 0, map[[2]float64]color.RGBA{
		{10, 10}: red, {30, 10}: black, {10, 30}: black,
		{45, 5}: green, {5, 45}: black,
		{10, 60}: grey, {40, 60}: white, {70, 60}: grey,
		{150, 50}: {0, 255, 0, 255}, {110, 50}: {0, 255, 255, 255},
		{10, 90}: white,
	})
}

// TestCanvasImages draws images with alpha and JPEGs, each once in a file.
func TestCanvasImages(t *testing.T) {
	half := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(half.Pix); i += 4 {
		copy(half.Pix[i:], []byte{255, 255, 255, 128})
	}
	gray := image.NewGray(image.Rect(0, 0, 4, 4))
	for i := range gray.Pix {
		gray.Pix[i] = 128
	}
	pre := image.NewRGBA(image.Rect(0, 0, 4, 4)) // premultiplied: white at half opacity
	for i := range pre.Pix {
		pre.Pix[i] = 128
	}
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < len(src.Pix); i += 4 {
		copy(src.Pix[i:], []byte{255, 0, 0, 255})
	}
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	photo, err := NewCanvasJPEG(jb.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if w, h := photo.Size(); w != 16 || h != 16 {
		t.Errorf("JPEG of %d by %d", w, h)
	}
	if _, err := NewCanvasJPEG([]byte("no image")); err == nil {
		t.Error("bytes that are no JPEG made an image")
	}
	if NewCanvasImage(image.NewRGBA(image.Rectangle{})) != nil {
		t.Error("an empty image made one")
	}
	images := []*CanvasImage{NewCanvasImage(half), NewCanvasImage(gray), NewCanvasImage(pre), photo}

	e := NewEditor()
	for i := range 3 {
		if err := e.AddPage(i, 200, 100); err != nil {
			t.Fatal(err)
		}
		err := e.Draw(i, true, func(c *Canvas) {
			c.FillRect(Rect{0, 0, 200, 100}, black)
			for k, im := range images {
				c.DrawImage(im, Rect{float64(10 + 40*k), 10, float64(40 + 40*k), 40})
			}
			c.DrawImage(nil, Rect{0, 0, 10, 10})
			c.SetOpacity(0.5)
			c.DrawImage(photo, Rect{10, 60, 40, 90})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	out := editSave(t, e)
	grey := color.RGBA{128, 128, 128, 255}
	for i := range 3 {
		wantPixels(t, "images", out, i, map[[2]float64]color.RGBA{
			{25, 25}: grey, {65, 25}: grey, {105, 25}: grey, {145, 25}: {254, 0, 0, 255},
			{25, 75}: {127, 0, 0, 255}, {5, 5}: black,
		})
	}
	// Four images and two soft masks, whatever the number of pages.
	if n := countImages(t, out); n != 6 {
		t.Errorf("%d image objects, want 6", n)
	}
}

// TestCanvasFonts draws text in standard fonts, in embedded fonts of both
// kinds of outlines, and what a font lacks in another.
func TestCanvasFonts(t *testing.T) {
	if _, err := StandardFont("Arial"); err == nil {
		t.Error("Arial is a standard font")
	}
	if _, err := NewCanvasFont([]byte("no font")); err == nil {
		t.Error("bytes that are no font made one")
	}
	e := NewEditor()
	if err := e.AddPage(0, 400, 400); err != nil {
		t.Fatal(err)
	}
	var want []string
	err := e.Draw(0, true, func(c *Canvas) {
		y := 20.0
		for _, name := range standardFonts {
			f, err := StandardFont(name)
			if err != nil {
				t.Fatal(err)
			}
			s := "Sphinx of " + name
			if name == "Symbol" || name == "ZapfDingbats" {
				continue
			}
			want = append(want, s)
			w := c.DrawText(s, 10, y, f, 10, black)
			if asc, desc := f.Metrics(); w <= 0 || asc < 0.5 || desc > -0.1 {
				t.Errorf("%s: width %v, metrics %v %v", name, w, asc, desc)
			}
			y += 14
		}
		// Courier is monospaced, bold is wider than regular.
		courier, _ := StandardFont("Courier")
		helv, _ := StandardFont("Helvetica")
		bold, _ := StandardFont("Helvetica-Bold")
		if a, b := courier.Width("iii", 10), courier.Width("WWW", 10); a != b || a != 18 {
			t.Errorf("Courier: %v and %v", a, b)
		}
		if a, b := helv.Width("Hamburg", 10), bold.Width("Hamburg", 10); a >= b {
			t.Errorf("Helvetica %v, bold %v", a, b)
		}
		// What Helvetica lacks, a fallback font shows.
		want = append(want, "Preis: 5 λ", "tab stop")
		c.DrawText("Preis: 5 λ", 10, y, helv, 10, black)
		c.DrawText("tab\tstop", 200, y, helv, 10, black)
		y += 14

		if stdfont.Gyre {
			cff, err := NewCanvasFont(stdfont.HelveticaRegular)
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, "Outlines of CFF")
			if w := c.DrawText("Outlines of CFF", 10, y, cff, 10, black); w < 40 || w > 100 {
				t.Errorf("CFF: width %v", w)
			}
			if asc, desc := cff.Metrics(); asc < 0.5 || desc > -0.1 {
				t.Errorf("CFF: metrics %v %v", asc, desc)
			}
		}
		// Nothing is drawn of nothing.
		if w := c.DrawText("", 0, 0, helv, 10, black); w != 0 {
			t.Errorf("width %v of no text", w)
		}
		c.DrawText("x", 0, 0, nil, 10, black)
		c.DrawText("x", 0, 0, helv, 0, black)
	})
	if err != nil {
		t.Fatal(err)
	}
	out := editSave(t, e)
	d := editOpen(t, out)
	text := pageString(t, d, 0)
	for _, s := range want {
		if !strings.Contains(text, s) {
			t.Errorf("text lacks %q:\n%s", s, text)
		}
	}
	// Every line shows: dark pixels where it is.
	img, _ := editRender(t, d, 0)
	for k := range want {
		if k == len(want)-2 {
			continue // the second text of a line
		}
		y, dark := 20+14*float64(k), 0
		if k == len(want)-1 && stdfont.Gyre {
			y -= 14
		}
		for x := 10.0; x < 80; x += 0.5 {
			for dy := -7.0; dy < 0; dy += 0.5 {
				if c := pixelAt(img, x, y+dy); c.R < 128 {
					dark++
				}
			}
		}
		if dark < 40 {
			t.Errorf("line %d (%q) shows %d dark pixels", k, want[k], dark)
		}
	}
}

// TestCanvasDrawPage puts pages on a sheet, and a page of the edited
// document on another of its pages.
func TestCanvasDrawPage(t *testing.T) {
	file := highlightDoc("0")
	src := editOpen(t, file)
	e := NewEditor()
	if err := e.AddPage(0, 400, 100); err != nil {
		t.Fatal(err)
	}
	if err := e.AddPage(1, 400, 100); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		err := e.Draw(i, true, func(c *Canvas) {
			// The colour and the line of the canvas are not the page's.
			helv, _ := StandardFont("Helvetica")
			c.DrawText(" ", 0, 0, helv, 10, red)
			var none Path
			none.MoveTo(-10, -10)
			none.LineTo(-5, -5)
			c.Stroke(&none, StrokeStyle{Width: 9, Dash: []float64{1, 1}}, red)
			for k := range 2 {
				if err := c.DrawPage(src, k, Rect{float64(200 * k), 0, float64(200*k + 200), 100}); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.DrawPage(src, 2, Rect{0, 0, 10, 10}); err == nil {
				t.Error("a page the document does not have was drawn")
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	out := editSave(t, e)
	d := editOpen(t, out)
	sheet, _ := editRender(t, d, 0)
	for k := range 2 {
		want, _ := editRender(t, src, k)
		got := sheet.SubImage(image.Rect(400*k, 0, 400*k+400, 200)).(*image.RGBA)
		same := true
		for y := 0; y < 200 && same; y++ {
			same = bytes.Equal(want.Pix[y*want.Stride:][:1600], got.Pix[got.PixOffset(400*k, y):][:1600])
		}
		if !same {
			t.Errorf("page %d on the sheet renders unlike the page", k)
		}
	}
	if text := pageString(t, d, 1); !strings.Contains(text, "alpha beta gamma") || !strings.Contains(text, "delta") {
		t.Errorf("text of the sheet: %q", text)
	}
	// Each page is one form in the file, however often it is drawn.
	if n := bytes.Count(out, []byte("/Subtype /Form")) + bytes.Count(out, []byte("/Subtype/Form")); n != 4 {
		t.Errorf("%d forms, want 4: two drawings and two pages", n)
	}

	// A turned page, drawn as it is displayed.
	turned := editOpen(t, highlightDoc("90"))
	e = NewEditor()
	if err := e.AddPage(0, 100, 200); err != nil {
		t.Fatal(err)
	}
	if err := e.Draw(0, true, func(c *Canvas) {
		if err := c.DrawPage(turned, 0, Rect{0, 0, 100, 200}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	want, _ := editRender(t, turned, 0)
	got, _ := editRender(t, editOpen(t, editSave(t, e)), 0)
	if n, _ := pixDiff(want, got); n > 0 {
		t.Errorf("the turned page drawn differs in %d pixels", n)
	}

	// A page of the edited document as a stamp on another, in an update.
	e = src.Edit()
	if err := e.Draw(1, true, func(c *Canvas) {
		if err := c.DrawPage(src, 0, Rect{100, 50, 200, 100}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	for mode, b := range map[string][]byte{"update": editUpdate(t, e, file), "save": editSave(t, e)} {
		if text := pageString(t, editOpen(t, b), 1); !strings.Contains(text, "delta") || !strings.Contains(text, "alpha beta gamma") {
			t.Errorf("%s: text of the stamped page: %q", mode, text)
		}
	}
}

// TestCanvasErrors refuses pages and drawings that cannot be, and drawing
// against the permissions of a document.
func TestCanvasErrors(t *testing.T) {
	e := NewEditor()
	for _, size := range [][2]float64{{0, 100}, {100, -1}, {100, 20000}} {
		if err := e.AddPage(0, size[0], size[1]); err == nil {
			t.Errorf("a page of %v was added", size)
		}
	}
	if err := e.AddPage(1, 100, 100); err == nil {
		t.Error("a page was added after the end")
	}
	if err := e.Draw(0, true, func(*Canvas) { t.Error("drawn on no page") }); err == nil {
		t.Error("a page that is not there was drawn on")
	}
	var buf bytes.Buffer
	if err := e.Save(&buf); err == nil {
		t.Error("a document without pages was saved")
	}
	if err := e.AddPage(0, 100, 100); err != nil {
		t.Fatal(err)
	}
	if err := e.Update(&buf); !errors.Is(err, ErrNoUpdate) {
		t.Errorf("update of a new document: %v", err)
	}
	// An empty drawing is none.
	if err := e.Draw(0, true, func(*Canvas) {}); err != nil || e.drawsOn() {
		t.Errorf("an empty drawing: %v", err)
	}

	// Drawing changes the content: it needs the permission to modify.
	const all = int32(-4)
	for perm, want := range map[int32]error{all: nil, all &^ (1 << 3): ErrNotPermitted} {
		e := editOpen(t, testpdf.EncryptedWith("aes128", "", perm)).Edit()
		if err := e.Draw(0, true, func(c *Canvas) { c.FillRect(Rect{0, 0, 10, 10}, red) }); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := e.Update(&out); !errors.Is(err, want) {
			t.Errorf("permissions %d: %v, want %v", perm, err, want)
		}
	}
}
