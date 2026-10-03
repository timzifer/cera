package exact

import (
	"math"
	"testing"
)

func render(t *testing.T, content string, w, h float64) []byte {
	t.Helper()
	pg, err := Interpret(content, w, h, 1)
	if err != nil {
		t.Fatal(err)
	}
	img := pg.Render()
	if img.Rect.Dx() != int(math.Ceil(w)) || img.Rect.Dy() != int(math.Ceil(h)) {
		t.Fatalf("size %v", img.Rect)
	}
	return img.Pix
}

// at is the red level of pixel (x, y) of an image w pixels wide.
func at(pix []byte, w, x, y int) int { return int(pix[4*(y*w+x)]) }

func TestRectCoverage(t *testing.T) {
	// A black rectangle from x 2.25 to 5.5, y (device) 1 to 3.
	pix := render(t, "0 g 2.25 7 3.25 2 re f", 10, 10)
	for _, c := range []struct{ x, y, want int }{
		{1, 1, 255}, {2, 1, 64}, {3, 2, 0}, {4, 1, 0}, {5, 2, 128}, {6, 2, 255}, {3, 0, 255}, {3, 3, 255},
	} {
		if got := at(pix, 10, c.x, c.y); got != c.want {
			t.Errorf("pixel %d,%d: %d, want %d", c.x, c.y, got, c.want)
		}
	}
}

func TestHorizontalEdge(t *testing.T) {
	// An edge a quarter into a pixel row: 75 % coverage, within the
	// sampling bound.
	pix := render(t, "0 g 0 0 4 3.25 re f", 4, 4)
	if got := at(pix, 4, 1, 0); math.Abs(float64(got)-191.25) > 255.0/(2*Rows)+0.5 {
		t.Errorf("row 0: %d, want 191", got)
	}
}

func TestCircleArea(t *testing.T) {
	// A disk as a round cap of a zero-length stroke: radius 10.
	pg, err := Interpret("0 G 20 w 1 J 32 32 m 32 32 l S", 64, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	img := pg.Render()
	ink := 0.0
	for i := 0; i < len(img.Pix); i += 4 {
		ink += float64(255-img.Pix[i]) / 255
	}
	if want := math.Pi * 100; math.Abs(ink-want) > 0.5 {
		t.Errorf("ink %.3f, want %.3f", ink, want)
	}
}

func TestStrokeUnionAndJoins(t *testing.T) {
	// An L of width 2 with a miter join: the corner square is covered
	// once, not twice.
	pix := render(t, "0 G 2 w 0 j 2 2 m 8 2 l 8 8 l S", 10, 10)
	if got := at(pix, 10, 8, 8); got != 0 {
		t.Errorf("miter corner: %d, want 0", got)
	}
	// Bevel: the outer corner pixel is half covered.
	pix = render(t, "0 G 2 w 2 j 2 2 m 8 2 l 8 8 l S", 10, 10)
	if got := at(pix, 10, 8, 8); got < 120 || got > 136 {
		t.Errorf("bevel corner: %d, want about 128", got)
	}
}

func TestEvenOdd(t *testing.T) {
	two := "0 g 1 1 8 8 re 3 3 4 4 re "
	if got := at(render(t, two+"f", 10, 10), 10, 5, 5); got != 0 {
		t.Errorf("nonzero hole: %d", got)
	}
	if got := at(render(t, two+"f*", 10, 10), 10, 5, 5); got != 255 {
		t.Errorf("even-odd hole: %d", got)
	}
}

func TestClipAndOrder(t *testing.T) {
	// Red over black, clipped to the left half.
	pix := render(t, "q 0 0 5 10 re W n 0 g 0 0 10 10 re f 1 0 0 rg 0 0 10 10 re f Q 0 0 1 rg 7 0 1 10 re f", 10, 10)
	if r, g := pix[4*(5*10+2)], pix[4*(5*10+2)+1]; r != 255 || g != 0 {
		t.Errorf("clipped red: %d %d", r, g)
	}
	if got := at(pix, 10, 6, 5); got != 255 {
		t.Errorf("outside clip: %d", got)
	}
	if b, r := pix[4*(5*10+7)+2], pix[4*(5*10+7)]; b != 255 || r != 0 {
		t.Errorf("blue after Q: %d %d", r, b)
	}
}

func TestHairline(t *testing.T) {
	// A hairline along a pixel centre row covers it exactly.
	pix := render(t, "0 G 0 w 0 5.5 m 10 5.5 l S", 10, 10)
	if got := at(pix, 10, 5, 4); got != 0 {
		t.Errorf("hairline row: %d", got)
	}
	if got := at(pix, 10, 5, 3); got != 255 {
		t.Errorf("next row: %d", got)
	}
}

func TestUnsupported(t *testing.T) {
	if _, err := Interpret("BT ET", 10, 10, 1); err == nil {
		t.Error("text accepted")
	}
}
