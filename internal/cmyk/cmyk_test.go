package cmyk

import (
	"math"
	"testing"
)

// TestPatches checks flat inks against transicc -t 1 -b of the profile
// gen reads, to sRGB (Little CMS 2.14).
func TestPatches(t *testing.T) {
	for _, c := range []struct {
		cmyk [4]float64
		rgb  [3]float64
	}{
		{[4]float64{1, 0, 0, 0}, [3]float64{0, 174, 240}},
		{[4]float64{0, 1, 0, 0}, [3]float64{236, 11, 141}},
		{[4]float64{0, 0, 1, 0}, [3]float64{255, 242, 0}},
		{[4]float64{0, 0, 0, 1}, [3]float64{43, 40, 41}},
		{[4]float64{0.5, 0, 0, 0}, [3]float64{118, 208, 246}},
		{[4]float64{0, 0.5, 0.5, 0}, [3]float64{245, 152, 125}},
		{[4]float64{1, 1, 0, 0}, [3]float64{55, 53, 147}},
		{[4]float64{0, 0, 0, 0.5}, [3]float64{151, 153, 155}},
		{[4]float64{0, 0, 0, 0}, [3]float64{255, 255, 255}},
		{[4]float64{1, 1, 1, 1}, [3]float64{0, 0, 0}},
	} {
		r, g, b := RGB(c.cmyk[0], c.cmyk[1], c.cmyk[2], c.cmyk[3])
		for i, v := range [3]float64{r, g, b} {
			if math.Abs(v*255-c.rgb[i]) > 1 {
				t.Errorf("%v: %.1f %.1f %.1f, want %v", c.cmyk, r*255, g*255, b*255, c.rgb)
				break
			}
		}
	}
}

func TestOutOfRange(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, 2} {
		r, g, b := RGB(v, v, v, v)
		for _, x := range [3]float64{r, g, b} {
			if !(x >= 0 && x <= 1) {
				t.Errorf("%v: %v %v %v", v, r, g, b)
			}
		}
	}
}

func TestRoundTrip(t *testing.T) {
	b, err := SWOP().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(swop) {
		t.Error("swop.bin does not read back as written")
	}
	var p Profile
	for n := range len(swop) {
		if p.UnmarshalBinary(swop[:n]) == nil {
			t.Fatalf("a table of %d bytes read", n)
		}
	}
}

func BenchmarkRGB8(b *testing.B) {
	var s uint8
	i := 0
	for b.Loop() {
		r, g, bl := RGB8(uint8(i), uint8(i*7), uint8(i*13), uint8(i*29))
		s += r + g + bl
		i++
	}
	_ = s
}
