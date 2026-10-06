package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestQuantile(t *testing.T) {
	v := []float64{4, 1, 3, 2}
	for _, c := range []struct{ q, want float64 }{{0, 1}, {0.5, 2.5}, {1, 4}, {0.25, 1.75}} {
		if got := quantile(v, c.q); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("quantile(%v) = %v, want %v", c.q, got, c.want)
		}
	}
	if !math.IsNaN(median(nil)) {
		t.Error("median of nothing is not NaN")
	}
	if !slices.Equal(v, []float64{4, 1, 3, 2}) {
		t.Error("quantile sorted its input")
	}
}

func TestRotateTakesEveryPosition(t *testing.T) {
	s := []string{"a", "b", "c"}
	first := map[string]bool{}
	for k := range 3 {
		r := rotate(s, k)
		if len(r) != 3 {
			t.Fatalf("rotate(%d) = %v", k, r)
		}
		first[r[0]] = true
	}
	if len(first) != 3 {
		t.Errorf("not every engine goes first: %v", first)
	}
}

func TestFinite(t *testing.T) {
	for _, v := range []float64{0, -1, math.Inf(1), math.NaN()} {
		if finite(v) {
			t.Errorf("finite(%v)", v)
		}
	}
	if !finite(0.5) {
		t.Error("finite(0.5) is false")
	}
}

// TestAggregateFromCSV checks that the summary comes from the CSV files
// alone, as a geometric mean of the page ratios, and counts failed pages.
func TestAggregateFromCSV(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("meta.json", `{"ref":"mupdf","cores":2,"runs":5,"dpi":150,"versions":{"mupdf":"m","cera":"c"}}`)
	write("pages.csv", "file,category,page,engine,ratio,spread,ink,width,height,allocs,bytes,again,error\n"+
		"a.pdf,text,1,mupdf,1.0000,0,0.1,1,1,,,,\n"+
		"a.pdf,text,1,cera,0.5000,0.1,0.1,1,1,3,10,0.5,\n"+
		"a.pdf,text,2,mupdf,1.0000,0,0.1,1,1,,,,\n"+
		"a.pdf,text,2,cera,2.0000,0.1,0.1,1,1,5,20,0.5,\n"+
		"a.pdf,text,3,mupdf,1.0000,0,0.1,1,1,,,,\n"+
		"a.pdf,text,3,cera,,,,,,,,,broken\n")
	write("files.csv", "file,category,pages,engine,first,multi,multi_spread,speedup,pooled,peak_rss_mb,error\n"+
		"a.pdf,text,3,mupdf,1.0000,1.0000,0,1.5,true,10.0,\n"+
		"a.pdf,text,3,cera,0.2500,0.5000,0,2.0,false,20.0,\n")
	if err := aggregate(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s summary
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	g := s.Single["text"]["cera"]
	if math.Abs(g.Ratio-1) > 1e-9 || g.N != 2 || g.Failed != 1 {
		t.Errorf("single text cera = %+v, want ratio 1 (geometric mean of 0.5 and 2), n 2, failed 1", g)
	}
	if r := s.First["all"]["cera"].Ratio; math.Abs(r-0.25) > 1e-9 {
		t.Errorf("first = %v", r)
	}
	if r := s.Multi["all"]["cera"].Ratio; math.Abs(r-0.5) > 1e-9 {
		t.Errorf("multi = %v", r)
	}
	if s.CeraAllocs.Median != 4 {
		t.Errorf("allocs median = %v", s.CeraAllocs.Median)
	}
}
