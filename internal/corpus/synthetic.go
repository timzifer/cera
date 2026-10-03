package corpus

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

// A3 landscape in points.
const pageW, pageH = 1190.55, 841.89

// Scene is a synthetic technical drawing: content with a known structure
// that isolates one cost of the renderer (the same scenes as the stilus core
// benchmarks, written as real PDFs so parser and interpreter are included).
type Scene struct {
	Name    string
	Content func(b *strings.Builder)
}

// Scenes returns the synthetic drawings.
func Scenes() []Scene {
	return []Scene{
		{"hatch-2000-hairline", func(b *strings.Builder) { hatch(b, 0) }},
		{"hatch-2000-0.35mm", func(b *strings.Builder) { hatch(b, 1) }},
		{"short-20000-0.35mm", func(b *strings.Builder) { short(b, 1) }},
		{"short-20000-hairline", func(b *strings.Builder) { short(b, 0) }},
		{"contours-3000", contours},
		{"hatch-2000-maskclip", maskclip},
		{"joins-caps", joinsCaps},
		{"fills-subpixel", fills},
	}
}

// 0.35 mm in points.
const pen = 0.35 * 72 / 25.4

func hatch(b *strings.Builder, width float64) {
	fmt.Fprintf(b, "0 G %.4f w\n", width*pen)
	for i := range 2000 {
		x := float64(i)*pageW/1000 - pageW
		fmt.Fprintf(b, "%.2f 0 m %.2f %.2f l S\n", x, x+pageH, pageH)
	}
}

func short(b *strings.Builder, width float64) {
	r := rand.New(rand.NewPCG(1, 2))
	fmt.Fprintf(b, "0 G %.4f w 1 J\n", width*pen)
	for range 20000 {
		x, y := r.Float64()*pageW, r.Float64()*pageH
		dx, dy := r.Float64()*20-10, r.Float64()*20-10
		fmt.Fprintf(b, "%.2f %.2f m %.2f %.2f l S\n", x, y, x+dx, y+dy)
	}
}

func contours(b *strings.Builder) {
	r := rand.New(rand.NewPCG(3, 4))
	fmt.Fprintf(b, "0 G %.4f w 1 j\n", pen)
	for range 3000 {
		x, y := r.Float64()*pageW, r.Float64()*pageH
		fmt.Fprintf(b, "%.2f %.2f m", x, y)
		for range 8 {
			x += r.Float64()*30 - 15
			y += r.Float64()*30 - 15
			fmt.Fprintf(b, " %.2f %.2f l", x, y)
		}
		b.WriteString(" S\n")
	}
	b.WriteString("0.8 0.1 0.1 RG\n")
	for range 600 {
		x, y, rad := r.Float64()*pageW, r.Float64()*pageH, 2+r.Float64()*20
		circle(b, x, y, rad)
		b.WriteString(" S\n")
	}
}

func maskclip(b *strings.Builder) {
	b.WriteString("q ")
	circle(b, pageW/2, pageH/2, pageH*0.45)
	b.WriteString(" W n\n")
	hatch(b, 0)
	b.WriteString("Q\n")
}

// joinsCaps strokes zigzags of 6 pt with every cap and join, sharp angles
// past the miter limit, closed paths, and the same under a skew, where
// round caps and joins become ellipses.
func joinsCaps(b *strings.Builder) {
	colors := []string{"0 0 0", "0.75 0.1 0.1", "0.1 0.3 0.8"}
	zig := func(x, y, a float64) {
		fmt.Fprintf(b, "%.2f %.2f m", x, y)
		for i := 1; i <= 5; i++ {
			fmt.Fprintf(b, " %.2f %.2f l", x+float64(i)*28, y+float64(i%2)*a)
		}
	}
	for cap := range 3 {
		for join := range 3 {
			x := 40 + float64(join)*380
			y := 60 + float64(cap)*250
			fmt.Fprintf(b, "%s RG 6 w %d J %d j 10 M\n", colors[(cap+join)%3], cap, join)
			zig(x, y, 60) // wide angles
			b.WriteString(" S\n")
			zig(x, y+100, 120) // sharp angles, past the limit
			b.WriteString(" S\n")
			fmt.Fprintf(b, "2 M %.2f %.2f m %.2f %.2f l %.2f %.2f l h S\n", x+190, y, x+330, y+20, x+200, y+70)
			fmt.Fprintf(b, "q 1 0 0.6 0.5 %.2f %.2f cm 0 0 m 60 0 l 60 60 l S Q\n", x+190, y+110)
		}
	}
	// Thin lines at fractional positions.
	b.WriteString("0 G 0.3 w 0 J\n")
	for i := range 40 {
		x := 1150 + float64(i)*0.37
		fmt.Fprintf(b, "%.2f 40 m %.2f 800 l S\n", x, x+3)
	}
}

// fills paints rectangles at sub-pixel positions and sizes, self-crossing
// stars under both winding rules, nested paths, and an even-odd clip.
func fills(b *strings.Builder) {
	for i := range 20 {
		for j := range 12 {
			x, y := 30+float64(i)*14+float64(i)*0.07, 30+float64(j)*14+float64(j)*0.13
			fmt.Fprintf(b, "%.2f g %.3f %.3f %.3f %.3f re f\n", float64(i*j%7)/7, x, y, 0.3+float64(i)*0.5, 0.2+float64(j)*0.8)
		}
	}
	star := func(cx, cy, r float64) {
		for k := range 5 {
			a := math.Pi/2 + float64(k)*4*math.Pi/5
			op := "l"
			if k == 0 {
				op = "m"
			}
			fmt.Fprintf(b, "%.3f %.3f %s ", cx+r*math.Cos(a), cy+r*math.Sin(a), op)
		}
		b.WriteString("h ")
	}
	for k, rule := range []string{"f", "f*"} {
		cx := 450 + float64(k)*260
		fmt.Fprintf(b, "0.1 0.3 0.8 rg ")
		star(cx, 150, 110)
		b.WriteString(rule + "\n0.75 0.1 0.1 rg ")
		circle(b, cx, 420, 100)
		b.WriteString(" ")
		circle(b, cx+0.4, 420.3, 60)
		b.WriteString(" " + rule + "\n")
	}
	b.WriteString("q ")
	circle(b, 1000, 420, 150)
	b.WriteString(" ")
	circle(b, 1000, 420, 70)
	b.WriteString(" W* n 0 g 0.5 w\n")
	for i := range 150 {
		x := 800 + float64(i)*2.71
		fmt.Fprintf(b, "%.2f 250 m %.2f 600 l S\n", x, x+40)
	}
	b.WriteString("Q\n")
}

// circle appends a closed circle of four Bézier arcs.
func circle(b *strings.Builder, x, y, r float64) {
	k := 0.5523 * r
	fmt.Fprintf(b, "%.2f %.2f m %.2f %.2f %.2f %.2f %.2f %.2f c %.2f %.2f %.2f %.2f %.2f %.2f c %.2f %.2f %.2f %.2f %.2f %.2f c %.2f %.2f %.2f %.2f %.2f %.2f c h",
		x+r, y,
		x+r, y+k, x+k, y+r, x, y+r,
		x-k, y+r, x-r, y+k, x-r, y,
		x-r, y-k, x-k, y-r, x, y-r,
		x+k, y-r, x+r, y-k, x+r, y)
}

// WriteScenes writes every scene as a one-page A3 PDF to
// dir/synthetic/<name>.pdf.
func WriteScenes(dir string) ([]string, error) {
	var paths []string
	for _, s := range Scenes() {
		var c strings.Builder
		s.Content(&c)
		path := filepath.Join(dir, "synthetic", s.Name+".pdf")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, PDF(pageW, pageH, c.String()), 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// PDF returns a one-page PDF of size w×h with a Flate-compressed content
// stream.
func PDF(w, h float64, content string) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte(content))
	zw.Close()

	var b bytes.Buffer
	var off [5]int
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	obj := func(n int, body string) {
		off[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Contents 4 0 R >>", w, h))
	off[4] = b.Len()
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d /Filter /FlateDecode >>\nstream\n", z.Len())
	b.Write(z.Bytes())
	b.WriteString("\nendstream\nendobj\n")
	xref := b.Len()
	b.WriteString("xref\n0 5\n0000000000 65535 f \n")
	for _, o := range off[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}
