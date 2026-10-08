package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/timzifer/figure"
	"github.com/timzifer/figure/facet"
	"github.com/timzifer/figure/geom"
	"github.com/timzifer/figure/ir"
	"github.com/timzifer/figure/palette"
	"github.com/timzifer/figure/scale"
	"github.com/timzifer/figure/theme"
)

// The charts are dot plots on a logarithmic axis, one row per engine, one
// panel per category, written once with light and once with dark tokens
// (README and docs pick one with <picture>). The engines' names are on
// the axis; colour only sets cera apart from the others.

func rgb(v uint32) ir.Color { return ir.RGB(uint8(v>>16), uint8(v>>8), uint8(v)) }

func chartTheme(dark bool) theme.Theme {
	t := theme.LightTokens
	t.Background = rgb(0xffffff)
	t.Ink, t.InkMuted, t.InkSubtle = rgb(0x1f2328), rgb(0x59636e), rgb(0x818b98)
	t.LineSubtle = rgb(0xe4e3df)
	if dark {
		t = theme.DarkTokens
		t.Background = rgb(0x0d1117)
		t.Ink, t.InkMuted, t.InkSubtle = rgb(0xf0f6fc), rgb(0x9198a1), rgb(0x6e7681)
		t.LineSubtle = rgb(0x30363d)
	}
	t.FontSize = 12
	return theme.Build(t)
}

// dotColors: cera in the accent (lighter for its WebAssembly build), the
// others in a neutral grey.
func dotColors(names []string, dark bool) palette.Qualitative {
	accent, accent2, grey := rgb(0x2a78d6), rgb(0x7fb0ea), rgb(0x8c959f)
	if dark {
		accent, accent2, grey = rgb(0x4493f8), rgb(0x1f5fae), rgb(0x6e7681)
	}
	var q palette.Qualitative
	for _, n := range names {
		switch n {
		case engineLabels["cera"]:
			q = append(q, accent)
		case engineLabels["cera-wasm"], engineLabels["cera-v8-wasm"]:
			q = append(q, accent2)
		default:
			q = append(q, grey)
		}
	}
	return q
}

// writeCharts writes <name>-light.svg and <name>-dark.svg for the ratio
// table t (category → engine → group).
func writeCharts(dir, name, title, ref string, t map[string]map[string]group, names []string, cats []string) error {
	var panel, engine, label []string
	var ratio, labelX []float64
	for _, c := range cats {
		for _, n := range names {
			g, ok := t[c][n]
			if !ok || g.N == 0 || g.Ratio <= 0 || math.IsNaN(g.Ratio) {
				continue
			}
			r := g.Ratio
			if n == ref {
				r = 1
			}
			cl := categoryLabels[c]
			if c == "all" && name == "multi" {
				cl = "all files"
			}
			panel = append(panel, fmt.Sprintf("%s · %d", cl, g.N))
			engine = append(engine, engineLabels[n])
			ratio = append(ratio, r)
			labelX = append(labelX, r*1.18) // right of the dot, on the log scale
			label = append(label, strconv.FormatFloat(r, 'f', 2, 64)+"×")
		}
	}
	if len(ratio) == 0 {
		return nil
	}
	var order []string
	for _, n := range names {
		if slices.Contains(engine, engineLabels[n]) {
			order = append(order, engineLabels[n])
		}
	}
	rows := slices.Clone(order)
	slices.Reverse(rows) // the first engine on top
	lo, hi := slices.Min(ratio), slices.Max(ratio)
	lo, hi = math.Min(lo, 1)/1.5, math.Max(hi, 1)*2.5 // room right of a dot for its label
	cols := min(3, len(cats))
	nrows := (len(cats) + cols - 1) / cols
	src := figure.NewTable().String("panel", panel).String("engine", engine).Float64("ratio", ratio).
		Float64("lx", labelX).String("label", label)
	for _, dark := range []bool{false, true} {
		p := figure.New(
			figure.Theme(chartTheme(dark)),
			figure.Size(360*cols, 70+nrows*(40+24*len(order))),
			figure.Title(title),
			figure.XTitle(fmt.Sprintf("time relative to %s (log scale; left is faster)", engineLabels[ref])),
			figure.Legend(false),
		)
		p.X(scale.Log(scale.LogDomain(lo, hi), scale.LogFormat(func(v float64) string {
			return strconv.FormatFloat(v, 'g', 3, 64) + "×"
		})))
		p.Y(scale.Ordinal(scale.Categories(rows...)))
		p.Add(
			geom.VLine(1),
			geom.Scatter(src, geom.X("ratio"), geom.Y("engine"),
				geom.ColorBy("engine", scale.Qualitative(dotColors(order, dark))), geom.Size(9)),
			geom.Text(src, geom.X("lx"), geom.Y("engine"), geom.TextBy("label"),
				geom.Align(ir.AlignStart, ir.AlignMiddle), geom.FontSize(11)),
		)
		if len(cats) > 1 {
			p.Facet(facet.Wrap("panel", facet.Columns(cols)))
		}
		var b bytes.Buffer
		if err := p.Render(figure.SVGWriter(&b)); err != nil {
			return err
		}
		mode := "light"
		if dark {
			mode = "dark"
		}
		if err := os.WriteFile(filepath.Join(dir, name+"-"+mode+".svg"), b.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}
