package main

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/timzifer/figure"
	"github.com/timzifer/figure/facet"
	"github.com/timzifer/figure/geom"
	"github.com/timzifer/figure/ir"
	"github.com/timzifer/figure/palette"
	"github.com/timzifer/figure/scale"
	"github.com/timzifer/figure/theme"
)

// The charts are drawn with figure (github.com/timzifer/figure), each twice:
// with light and with dark tokens, and the page shows the one its colour
// scheme asks for.

// engineColors are the categorical slots of the engines, in engineOrder,
// validated for colour-vision deficiencies in both modes.
var engineColors = [2][]ir.Color{
	{rgb(0x2a78d6), rgb(0xeb6834), rgb(0x1baf7a), rgb(0xeda100), rgb(0xe87ba4)},
	{rgb(0x3987e5), rgb(0xd95926), rgb(0x199e70), rgb(0xc98500), rgb(0xd55181)},
}

// blues is the sequential ramp of the heatmap, from near nothing to much.
var blues = [2]palette.Ramp{
	// Light mode stops at a mid blue: figure gives the labels on the cells
	// dark ink, which the darker steps would swallow.
	{rgb(0xf0f5fc), rgb(0xcde2fb), rgb(0xb7d3f6), rgb(0x9ec5f4), rgb(0x86b6ef), rgb(0x6da7ec), rgb(0x5598e7)},
	{rgb(0x24292f), rgb(0x0d366b), rgb(0x104281), rgb(0x184f95), rgb(0x1c5cab), rgb(0x2a78d6), rgb(0x5598e7)},
}

func rgb(v uint32) ir.Color { return ir.RGB(uint8(v>>16), uint8(v>>8), uint8(v)) }

// chartTheme is figure's theme on the page's surface, with the engines'
// colours as its palette.
func chartTheme(dark bool, engines []string) theme.Theme {
	t := theme.LightTokens
	t.Background = rgb(0xfcfcfb)
	t.Ink, t.InkMuted, t.InkSubtle = rgb(0x0b0b0b), rgb(0x52514e), rgb(0x7a7974)
	t.LineSubtle = rgb(0xe4e3df)
	m := 0
	if dark {
		t = theme.DarkTokens
		t.Background = rgb(0x1a1a19)
		t.Ink, t.InkMuted, t.InkSubtle = rgb(0xffffff), rgb(0xc3c2b7), rgb(0x9a998f)
		t.LineSubtle = rgb(0x383835)
		m = 1
	}
	t.Palette = enginePalette(engines, dark)
	t.Sequential = blues[m]
	t.FontSize = 12
	return theme.Build(t)
}

// enginePalette gives each engine its own slot whichever engines ran, so
// that an engine keeps its colour when another is missing.
func enginePalette(engines []string, dark bool) palette.Qualitative {
	m := 0
	if dark {
		m = 1
	}
	var q palette.Qualitative
	for _, e := range engines {
		q = append(q, engineColors[m][slot(e)-1])
	}
	return q
}

// figures counts the SVGs of the page, to scope their ids.
var figures int

// idRef matches an SVG id and every reference to one.
var idRef = regexp.MustCompile(`(\bid="|href="#|url\(#|aria-labelledby="|aria-describedby=")([^"\)\s]+)`)

// scopeIDs prefixes the ids of an SVG and the references to them: the SVGs
// of one page share a document, and figure numbers its clip paths and
// markers from 1 in each.
func scopeIDs(svg, prefix string) string {
	return idRef.ReplaceAllString(svg, "${1}"+prefix+"${2}")
}

// both renders the plot build makes in light and in dark.
func both(build func(dark bool) *figure.Plot) template.HTML {
	var out strings.Builder
	for _, dark := range []bool{false, true} {
		var b bytes.Buffer
		cls := "fig-light"
		if dark {
			cls = "fig-dark"
		}
		if err := build(dark).Render(figure.SVGWriter(&b)); err != nil {
			fmt.Fprintf(&out, `<p class="note">chart failed: %s</p>`, esc(err.Error()))
			continue
		}
		figures++
		fmt.Fprintf(&out, `<div class="%s">%s</div>`, cls, scopeIDs(b.String(), fmt.Sprintf("f%d-", figures)))
	}
	return template.HTML(`<div class="fig">` + out.String() + `</div>`)
}

func percent(v float64) string {
	switch {
	case v == 0:
		return "0 %"
	case v < 0.01:
		return "<0.01 %"
	case v < 10:
		return strconv.FormatFloat(v, 'f', 2, 64) + " %"
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " %"
}

// barFacets draws the outlier share per engine as horizontal bars, one
// panel per category, all on one scale.
func barFacets(v *view, engines []string) template.HTML {
	var panel, engine, label []string
	var share []float64
	add := func(name string, vals map[string]float64) {
		for _, e := range engines {
			x, ok := vals[e]
			if !ok || math.IsNaN(x) {
				continue
			}
			panel, engine = append(panel, name), append(engine, e)
			share, label = append(share, 100*x), append(label, "  "+percent(100*x))
		}
	}
	add(fmt.Sprintf("all · %d pages", v.Compared), v.Outlier)
	for _, c := range v.Categories {
		add(fmt.Sprintf("%s · %d pages", c.Category, c.Pages), c.Outlier)
	}
	if len(share) == 0 {
		return ""
	}
	src := figure.NewTable().String("panel", panel).String("engine", engine).Float64("share", share).String("label", label)
	panels := 1 + len(v.Categories)
	rows := (panels + 2) / 3
	order := slices.Clone(engines)
	slices.Reverse(order) // the first engine on top
	return both(func(dark bool) *figure.Plot {
		p := figure.New(
			figure.Theme(chartTheme(dark, engines)),
			figure.Size(1080, 60+rows*190),
			figure.XTitle("share of the inked area"),
			figure.Legend(false),
		)
		// Room right of the longest bar for its label.
		p.X(scale.Linear(scale.Domain(0, slices.Max(share)*1.2), scale.Nice(),
			scale.Format(func(v float64) string { return strconv.FormatFloat(v, 'g', 4, 64) + " %" })))
		p.Y(scale.Ordinal(scale.Categories(order...)))
		p.Add(
			geom.Bar(src, geom.Y("engine"), geom.X("share"), geom.Orient(geom.Horizontal),
				geom.ColorBy("engine", scale.Qualitative(enginePalette(engines, dark))), geom.Corner(4)),
			geom.Text(src, geom.X("share"), geom.Y("engine"), geom.TextBy("label"),
				geom.Align(ir.AlignStart, ir.AlignMiddle), geom.FontSize(11)),
		)
		p.Facet(facet.Wrap("panel", facet.Columns(3)))
		return p
	})
}

// heatmap draws how often each pair of engines differs.
func heatmap(v *view, engines []string) template.HTML {
	var a, b, label []string
	var share []float64
	top := 0.0
	for _, x := range engines {
		for _, y := range engines {
			if x == y {
				continue
			}
			s := v.Pair[x][y]
			if math.IsNaN(s) {
				continue
			}
			a, b = append(a, x), append(b, y)
			share, label = append(share, 100*s), append(label, percent(100*s))
			top = max(top, 100*s)
		}
	}
	if len(share) == 0 {
		return ""
	}
	src := figure.NewTable().String("a", a).String("b", b).Float64("share", share).String("label", label)
	order := slices.Clone(engines)
	slices.Reverse(order)
	return both(func(dark bool) *figure.Plot {
		p := figure.New(
			figure.Theme(chartTheme(dark, engines)),
			figure.Size(560, 460),
		)
		p.X(scale.Ordinal(scale.Categories(engines...), scale.OrdinalPadding(0.04)))
		p.Y(scale.Ordinal(scale.Categories(order...), scale.OrdinalPadding(0.04)))
		m := 0
		if dark {
			m = 1
		}
		opts := []geom.Option{geom.X("a"), geom.Y("b"),
			geom.ColorBy("share", scale.Sequential(blues[m], scale.ColorDomain(0, top)))}
		p.Add(
			geom.Rect(src, append(opts, geom.Corner(4))...),
			geom.Text(src, append(opts, geom.TextBy("label"), geom.FontSize(11), geom.Align(ir.AlignCenter, ir.AlignMiddle))...),
		)
		return p
	})
}

// inkPlot draws, per drawing, each engine's ink against the exact
// rendering: 1 is exact, right of it heavier. Engines are dodged
// vertically within a drawing's row, so that equal values stay visible.
func inkPlot(rep *report) template.HTML {
	var scenes []string
	for _, r := range rep.Exact {
		if !slices.Contains(scenes, r.Scene) {
			scenes = append(scenes, r.Scene)
		}
	}
	var engine []string
	var ink, pos []float64
	lo, hi := 1.0, 1.0
	for _, r := range rep.Exact {
		if math.IsNaN(r.InkRatio) || math.IsInf(r.InkRatio, 0) {
			continue
		}
		engine = append(engine, r.Engine)
		ink = append(ink, r.InkRatio)
		pos = append(pos, float64(slices.Index(scenes, r.Scene))+(float64(slot(r.Engine))-3)*0.13)
		lo, hi = min(lo, r.InkRatio), max(hi, r.InkRatio)
	}
	if len(ink) == 0 {
		return ""
	}
	span := max(1-lo, hi-1, 0.05) * 1.08
	src := figure.NewTable().String("engine", engine).Float64("ink", ink).Float64("row", pos)
	ticks := make([]float64, len(scenes))
	for i := range ticks {
		ticks[i] = float64(i)
	}
	present := slices.DeleteFunc(slices.Clone(rep.Engines), func(e string) bool { return !slices.Contains(engine, e) })
	return both(func(dark bool) *figure.Plot {
		p := figure.New(
			figure.Theme(chartTheme(dark, present)),
			figure.Size(900, 90+len(scenes)*46),
			figure.XTitle("ink against the exact rendering (1× is exact)"),
		)
		p.X(scale.Linear(scale.Domain(1-span, 1+span), scale.Format(func(v float64) string {
			return strconv.FormatFloat(v, 'g', 3, 64) + "×"
		})))
		p.Y(scale.Linear(scale.Domain(-0.6, float64(len(scenes))-0.4), scale.Reverse(), scale.TickValues(ticks...),
			scale.Format(func(v float64) string {
				if i := int(math.Round(v)); i >= 0 && i < len(scenes) && math.Abs(v-float64(i)) < 1e-9 {
					return scenes[i]
				}
				return ""
			})))
		p.Add(
			geom.VLine(1, geom.Label("exact")),
			geom.Scatter(src, geom.X("ink"), geom.Y("row"),
				geom.ColorBy("engine", scale.Qualitative(enginePalette(present, dark))), geom.Size(8)),
		)
		return p
	})
}
