package cera

import (
	"context"
	"fmt"
	"image"
	"strings"
	"testing"
)

// TestFullHairlines checks RenderOptions.FullHairlines: thin strokes are
// drawn at their width by default and one pixel wide at full coverage with
// it, in the page and in the cell of a tiling pattern; strokes of width 0
// are drawn one pixel wide either way. Switching interprets the page again.
func TestFullHairlines(t *testing.T) {
	var c strings.Builder
	// Left: lines a quarter of a pixel wide, one per pixel row.
	for y := 0; y < 100; y++ {
		fmt.Fprintf(&c, "0 %d.5 m 50 %d.5 l ", y, y)
	}
	c.WriteString("0.25 w S ")
	// Middle: lines of width 0.
	for y := 0; y < 100; y += 2 {
		fmt.Fprintf(&c, "60 %d.5 m 110 %d.5 l ", y, y)
	}
	c.WriteString("0 w S ")
	// Right: a pattern whose cell has a quarter-pixel line.
	c.WriteString("/Pattern cs /P scn 120 0 50 100 re f")
	pat := streamObj("/Type /Pattern /PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 4 4] /XStep 4 /YStep 4 /Resources <<>>",
		[]byte("0.25 w 0 2.5 m 4 2.5 l S"))
	doc, err := Open(buildPDF([]string{c.String()}, "/Resources << /Pattern << /P 100 0 R >> >>", pat))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := doc.Page(0)
	dst := image.NewRGBA(p.Bounds(1))
	thin := rgba(191, 191, 191, 255) // a quarter of each pixel black
	black := rgba(0, 0, 0, 255)
	for _, full := range []bool{false, true, false} {
		var st Stats
		if err := p.Render(context.Background(), dst, RenderOptions{Background: white, Stats: &st, FullHairlines: full}); err != nil {
			t.Fatal(err)
		}
		if st.Reused {
			t.Errorf("full %v: list of the other setting reused", full)
		}
		want := thin
		if full {
			want = black
		}
		assertNear(t, dst, 25, 50, want, 2)
		assertNear(t, dst, 85, 49, black, 0)  // page y 50.5: a line of width 0
		assertNear(t, dst, 145, 57, want, 2)  // page y 42.5: the line of a cell
		assertNear(t, dst, 145, 58, white, 0) // between the cells' lines
	}
}
