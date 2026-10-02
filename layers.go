package cera

import (
	"math"
	"slices"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/go-pdfkit/reader"
)

// Optional content (PDF 2.0, 8.11): layers that a configuration of the
// document, the usage of a render and the viewer switch on and off.
//
// The interpreter tags what it records with the membership of the marked
// content (BDC /OC) and XObjects (/OC) around it; the display list keeps
// hidden items and evaluates the tags once per render against a Visibility,
// so switching layers costs one render and no parsing.

// Layer is an optional content group as one configuration sees it.
type Layer struct {
	Name string
	// Visible and Locked are the state in the configuration, before the
	// usage of a render applies (see LayerConfig.VisibilityFor).
	Visible bool
	Locked  bool

	index int // in the document's /OCGs
	ref   reader.Ref
}

// LayerNode is an entry of the layer tree a viewer shows (/Order): a layer
// with the layers nested under it, or a label grouping layers.
type LayerNode struct {
	Layer    *Layer // nil for a label
	Label    string
	Children []LayerNode
}

// LayerConfig is a configuration of optional content (/D or an entry of
// /Configs).
type LayerConfig struct {
	Name string
	// Layers are the document's groups in the order of /OCGs; Layers[i]
	// of every configuration is the same group.
	Layers []*Layer
	// Order is the tree of /Order, for a layer panel; it may leave
	// layers out.
	Order []LayerNode
	// RBGroups are radio-button groups: at most one layer of a group is
	// meant to be visible. Visibility.With does not enforce them.
	RBGroups [][]*Layer

	vis [numUsages]Visibility
}

// Usage is what a render is for; it selects the automatic states of a
// configuration's /AS entry and the /Usage of its groups.
type Usage uint8

// Usages.
const (
	UsageView Usage = iota
	UsagePrint
	UsageExport
	numUsages
)

var usageNames = [numUsages]struct{ event, dict, state reader.Name }{
	{"View", "View", "ViewState"},
	{"Print", "Print", "PrintState"},
	{"Export", "Export", "ExportState"},
}

// Visibility selects the optional content a render draws: a set of the
// document's layers that are off, and the layers whose state depends on
// the zoom. The zero value shows every layer. A Visibility is a value;
// With returns a modified copy and leaves the original unchanged, so one
// document can be shown with different layers in several views.
type Visibility struct {
	off  []uint64 // bit i: group i of /OCGs is off; shared, never modified
	zoom []ocZoom // shared, never modified
}

// ocZoom limits group ocg to zooms (scales) in [min, max).
type ocZoom struct {
	ocg      int
	min, max float64
}

// Visible reports whether l is on, at any zoom.
func (v Visibility) Visible(l *Layer) bool { return l == nil || v.on(l.index) }

func (v Visibility) on(i int) bool {
	w := i / 64
	return i < 0 || w >= len(v.off) || v.off[w]&(1<<(i%64)) == 0
}

// With returns v with l switched on or off, for every zoom.
func (v Visibility) With(l *Layer, on bool) Visibility {
	if l == nil {
		return v
	}
	i := l.index
	n := max(len(v.off), i/64+1)
	off := make([]uint64, n)
	copy(off, v.off)
	if on {
		off[i/64] &^= 1 << (i % 64)
	} else {
		off[i/64] |= 1 << (i % 64)
	}
	v.off = off
	if slices.ContainsFunc(v.zoom, func(z ocZoom) bool { return z.ocg == i }) {
		v.zoom = slices.DeleteFunc(slices.Clone(v.zoom), func(z ocZoom) bool { return z.ocg == i })
	}
	return v
}

// visibleAt reports whether group i is on at zoom.
func (v *Visibility) visibleAt(i int, zoom float64) bool {
	if !v.on(i) {
		return false
	}
	for _, z := range v.zoom {
		if z.ocg == i && !(zoom >= z.min && zoom < z.max) {
			return false
		}
	}
	return true
}

// Visibility returns the visibility the configuration gives on screen.
func (c *LayerConfig) Visibility() Visibility { return c.VisibilityFor(UsageView) }

// VisibilityFor returns the visibility the configuration gives for usage
// u: its states with its automatic states (/AS) for u applied.
func (c *LayerConfig) VisibilityFor(u Usage) Visibility {
	if c == nil || u >= numUsages {
		return Visibility{}
	}
	return c.vis[u]
}

// Layers returns the default configuration of the document's optional
// content (/OCProperties /D), nil if the document has none.
func (d *Document) Layers() *LayerConfig { return d.ocProps().def }

// LayerConfigs returns the alternate configurations (/OCProperties
// /Configs).
func (d *Document) LayerConfigs() []*LayerConfig { return d.ocProps().configs }

// ocProps is the optional content of a document, read once.
type ocProps struct {
	ocgs    []reader.Ref
	index   map[reader.Ref]int
	def     *LayerConfig
	configs []*LayerConfig

	mu    sync.Mutex
	exprs map[reader.Ref]*ocExpr // compiled membership, nil if always on
}

func (d *Document) ocProps() *ocProps {
	d.ocOnce.Do(func() {
		d.oc = &ocProps{}
		func() {
			defer func() { _ = recover() }() // a broken catalog has no layers
			d.oc.read(d)
		}()
	})
	return d.oc
}

// defaultVisibility returns the default configuration's visibility for u.
func (d *Document) defaultVisibility(u Usage) *Visibility {
	oc := d.ocProps()
	if oc.def == nil || u >= numUsages {
		return &noLayers
	}
	return &oc.def.vis[u]
}

var noLayers Visibility

const maxLayerDepth = 16 // /Order nesting and /VE expressions

func (oc *ocProps) read(d *Document) {
	cat, err := d.r.Catalog()
	if err != nil {
		return
	}
	props := d.dict(cat["OCProperties"])
	if props == nil {
		return
	}
	arr, _ := reader.ToArray(d.resolve(props["OCGs"]))
	oc.index = make(map[reader.Ref]int, len(arr))
	oc.exprs = map[reader.Ref]*ocExpr{}
	for _, o := range arr {
		ref, ok := o.(reader.Ref)
		if _, dup := oc.index[ref]; !ok || dup || d.dict(ref) == nil {
			continue
		}
		oc.index[ref] = len(oc.ocgs)
		oc.ocgs = append(oc.ocgs, ref)
	}
	if len(oc.ocgs) == 0 {
		return
	}
	dd := d.dict(props["D"])
	oc.def = oc.config(d, dd, nil)
	cs, _ := reader.ToArray(d.resolve(props["Configs"]))
	for _, o := range cs {
		if c := d.dict(o); c != nil {
			oc.configs = append(oc.configs, oc.config(d, c, oc.def))
		}
	}
}

// config reads a configuration dictionary; def is the default one, which
// BaseState /Unchanged of an alternate configuration starts from.
func (oc *ocProps) config(d *Document, cd reader.Dict, def *LayerConfig) *LayerConfig {
	c := &LayerConfig{Name: textString(d.resolve(cd["Name"]))}
	base := true
	if n, _ := d.name(cd["BaseState"]); n == "OFF" {
		base = false
	}
	unchanged := def != nil && isName(d, cd["BaseState"], "Unchanged")
	intents := ocIntents(d, cd["Intent"])
	c.Layers = make([]*Layer, len(oc.ocgs))
	ignored := make([]bool, len(oc.ocgs))
	for i, ref := range oc.ocgs {
		g := d.dict(ref)
		l := &Layer{Name: textString(d.resolve(g["Name"])), Visible: base, index: i, ref: ref}
		if unchanged {
			l.Visible = def.Layers[i].Visible
		}
		ignored[i] = !intents.matches(ocIntents(d, g["Intent"]))
		c.Layers[i] = l
	}
	set := func(key reader.Name, f func(*Layer)) {
		a, _ := reader.ToArray(d.resolve(cd[key]))
		for _, o := range a {
			if l := c.layer(oc, o); l != nil {
				f(l)
			}
		}
	}
	set("ON", func(l *Layer) { l.Visible = true })
	set("OFF", func(l *Layer) { l.Visible = false })
	set("Locked", func(l *Layer) { l.Locked = true })
	for i, l := range c.Layers {
		if ignored[i] {
			l.Visible = true // a group of another intent is not optional here
		}
	}
	order, _ := reader.ToArray(d.resolve(cd["Order"]))
	c.Order = c.order(d, oc, order, 0)
	rbs, _ := reader.ToArray(d.resolve(cd["RBGroups"]))
	for _, o := range rbs {
		a, _ := reader.ToArray(d.resolve(o))
		var g []*Layer
		for _, e := range a {
			if l := c.layer(oc, e); l != nil {
				g = append(g, l)
			}
		}
		if len(g) > 0 {
			c.RBGroups = append(c.RBGroups, g)
		}
	}

	var off []uint64
	for i, l := range c.Layers {
		if !l.Visible {
			off = setBit(off, i)
		}
	}
	as, _ := reader.ToArray(d.resolve(cd["AS"]))
	for u := range numUsages {
		v := Visibility{off: off}
		c.autoState(d, oc, as, u, &v, ignored)
		c.vis[u] = v
	}
	return c
}

func setBit(b []uint64, i int) []uint64 {
	for len(b) <= i/64 {
		b = append(b, 0)
	}
	b[i/64] |= 1 << (i % 64)
	return b
}

// autoState applies the usage application dictionaries of /AS for usage
// u (PDF 2.0, 8.11.4.4) to v: a group is off if any of the categories
// listed for it says so, and limited to a zoom range by /Zoom.
func (c *LayerConfig) autoState(d *Document, oc *ocProps, as reader.Array, u Usage, v *Visibility, ignored []bool) {
	names := usageNames[u]
	var off []uint64
	copied := false
	for _, o := range as {
		ad := d.dict(o)
		if ev, _ := d.name(ad["Event"]); ev != names.event {
			continue
		}
		cats, _ := reader.ToArray(d.resolve(ad["Category"]))
		groups, _ := reader.ToArray(d.resolve(ad["OCGs"]))
		for _, g := range groups {
			l := c.layer(oc, g)
			if l == nil || ignored[l.index] {
				continue
			}
			usage := d.dict(d.dict(l.ref)["Usage"])
			state, decided := true, false
			for _, cat := range cats {
				cn, _ := d.name(cat)
				switch cn {
				case "View", "Print", "Export":
					ud := d.dict(usage[cn])
					s, ok := d.name(ud[stateKey(cn)])
					if ok && (s == "ON" || s == "OFF") {
						state, decided = state && s == "ON", true
					}
				case "Zoom":
					zd := d.dict(usage["Zoom"])
					if zd == nil {
						continue
					}
					z := ocZoom{ocg: l.index, min: 0, max: math.Inf(1)}
					if f, ok := d.num(zd["min"]); ok {
						z.min = f
					}
					if f, ok := d.num(zd["max"]); ok {
						z.max = f
					}
					v.zoom = append(v.zoom, z)
					decided = true
				}
			}
			if !decided {
				continue
			}
			if !copied {
				off, copied = slices.Clone(v.off), true
			}
			if state {
				if w := l.index / 64; w < len(off) {
					off[w] &^= 1 << (l.index % 64)
				}
			} else {
				off = setBit(off, l.index)
			}
		}
	}
	if copied {
		v.off = off
	}
}

func stateKey(cat reader.Name) reader.Name {
	for _, n := range usageNames {
		if n.dict == cat {
			return n.state
		}
	}
	return ""
}

// layer returns the configuration's layer of the group o refers to.
func (c *LayerConfig) layer(oc *ocProps, o reader.Object) *Layer {
	ref, ok := o.(reader.Ref)
	if !ok {
		return nil
	}
	if i, ok := oc.index[ref]; ok {
		return c.Layers[i]
	}
	return nil
}

// order reads an /Order array: groups, each optionally followed by the
// array of its children, and arrays starting with a label.
func (c *LayerConfig) order(d *Document, oc *ocProps, a reader.Array, depth int) []LayerNode {
	if depth >= maxLayerDepth {
		return nil
	}
	var nodes []LayerNode
	for i, o := range a {
		if l := c.layer(oc, o); l != nil {
			nodes = append(nodes, LayerNode{Layer: l})
			continue
		}
		sub, ok := reader.ToArray(d.resolve(o))
		if !ok {
			continue
		}
		label, labelled := "", false
		if len(sub) > 0 {
			if s, ok := reader.ToString(d.resolve(sub[0])); ok {
				label, labelled = textString(reader.String(s)), true
				sub = sub[1:]
			}
		}
		kids := c.order(d, oc, sub, depth+1)
		if n := len(nodes); !labelled && n > 0 && i > 0 && nodes[n-1].Layer != nil && nodes[n-1].Children == nil {
			nodes[n-1].Children = kids // the children of the group before
			continue
		}
		nodes = append(nodes, LayerNode{Label: label, Children: kids})
	}
	return nodes
}

// ocIntent is a set of intents: View, Design, or All of them.
type ocIntent uint8

const (
	intentView ocIntent = 1 << iota
	intentDesign
	intentOther
	intentAll = intentView | intentDesign | intentOther
)

func ocIntents(d *Document, o reader.Object) ocIntent {
	one := func(o reader.Object) ocIntent {
		switch n, _ := d.name(o); n {
		case "View":
			return intentView
		case "Design":
			return intentDesign
		case "All":
			return intentAll
		case "":
			return 0
		}
		return intentOther
	}
	o = d.resolve(o)
	if a, ok := reader.ToArray(o); ok {
		var s ocIntent
		for _, e := range a {
			s |= one(e)
		}
		return s
	}
	if s := one(o); s != 0 {
		return s
	}
	return intentView // the default, of groups and of configurations
}

func (s ocIntent) matches(t ocIntent) bool { return s&t != 0 }

func isName(d *Document, o reader.Object, want reader.Name) bool {
	n, _ := d.name(o)
	return n == want
}

// ocExpr is a compiled membership: a group, or a function of groups.
type ocExpr struct {
	op   ocOp
	ocg  int // ocLeaf
	args []ocExpr
}

type ocOp uint8

const (
	ocTrue ocOp = iota
	ocLeaf
	ocNot
	ocAnd
	ocOr
)

// eval reports whether content of membership e is visible under v at
// zoom.
func (e *ocExpr) eval(v *Visibility, zoom float64) bool {
	switch e.op {
	case ocLeaf:
		return v.visibleAt(e.ocg, zoom)
	case ocNot:
		return !e.args[0].eval(v, zoom)
	case ocAnd:
		for i := range e.args {
			if !e.args[i].eval(v, zoom) {
				return false
			}
		}
		return true
	case ocOr:
		for i := range e.args {
			if e.args[i].eval(v, zoom) {
				return true
			}
		}
		return false
	}
	return true
}

// membership returns the compiled membership of o, an optional content
// group or membership dictionary, or nil when its content is always
// visible. bad reports a dictionary that does not read; its content is
// drawn.
func (d *Document) membership(o reader.Object) (e *ocExpr, bad bool) {
	oc := d.ocProps()
	if len(oc.ocgs) == 0 {
		return nil, false // without /OCProperties, optional content is ignored
	}
	if o == nil {
		return nil, true // a missing /Properties entry
	}
	ref, isRef := o.(reader.Ref)
	if isRef {
		oc.mu.Lock()
		e, ok := oc.exprs[ref]
		oc.mu.Unlock()
		if ok {
			return e, false
		}
	}
	x, ok := oc.compile(d, o, 0)
	if ok && x.op != ocTrue {
		e = &x
	}
	if isRef && ok {
		oc.mu.Lock()
		oc.exprs[ref] = e
		oc.mu.Unlock()
	}
	return e, !ok
}

func (oc *ocProps) compile(d *Document, o reader.Object, depth int) (ocExpr, bool) {
	if depth > maxLayerDepth {
		return ocExpr{}, false
	}
	if ref, ok := o.(reader.Ref); ok {
		if i, ok := oc.index[ref]; ok {
			return ocExpr{op: ocLeaf, ocg: i}, true
		}
	}
	md := d.dict(o)
	if md == nil {
		return ocExpr{}, false
	}
	switch t, _ := d.name(md["Type"]); {
	case t == "OCG", t != "OCMD" && md["OCGs"] == nil && md["VE"] == nil:
		return ocExpr{}, true // a group not in /OCGs: not optional
	}
	if ve, ok := reader.ToArray(d.resolve(md["VE"])); ok {
		return oc.visExpr(d, ve, depth+1)
	}
	var leaves []ocExpr
	ocgs := d.resolve(md["OCGs"])
	if a, ok := reader.ToArray(ocgs); ok {
		for _, g := range a {
			if ref, ok := g.(reader.Ref); ok {
				if i, ok := oc.index[ref]; ok {
					leaves = append(leaves, ocExpr{op: ocLeaf, ocg: i})
				}
			}
		}
	} else if ref, ok := md["OCGs"].(reader.Ref); ok {
		if i, ok := oc.index[ref]; ok {
			leaves = append(leaves, ocExpr{op: ocLeaf, ocg: i})
		}
	}
	if len(leaves) == 0 {
		return ocExpr{}, true // no groups: the dictionary has no effect
	}
	p, _ := d.name(md["P"])
	switch p {
	case "AllOn":
		return ocExpr{op: ocAnd, args: leaves}, true
	case "AnyOff":
		return ocExpr{op: ocNot, args: []ocExpr{{op: ocAnd, args: leaves}}}, true
	case "AllOff":
		return ocExpr{op: ocNot, args: []ocExpr{{op: ocOr, args: leaves}}}, true
	}
	return ocExpr{op: ocOr, args: leaves}, true // AnyOn
}

// visExpr compiles a visibility expression: [/And e …], [/Or e …] or
// [/Not e], whose operands are groups or expressions.
func (oc *ocProps) visExpr(d *Document, a reader.Array, depth int) (ocExpr, bool) {
	if depth > maxLayerDepth || len(a) < 2 {
		return ocExpr{}, false
	}
	n, _ := d.name(a[0])
	var e ocExpr
	switch n {
	case "And":
		e.op = ocAnd
	case "Or":
		e.op = ocOr
	case "Not":
		if len(a) != 2 {
			return ocExpr{}, false
		}
		e.op = ocNot
	default:
		return ocExpr{}, false
	}
	for _, o := range a[1:] {
		var x ocExpr
		var ok bool
		if ref, isRef := o.(reader.Ref); isRef {
			if i, known := oc.index[ref]; known {
				x, ok = ocExpr{op: ocLeaf, ocg: i}, true
			}
		}
		if !ok {
			sub, isArr := reader.ToArray(d.resolve(o))
			if !isArr {
				x, ok = ocExpr{}, true // a group not in /OCGs counts as on
			} else if x, ok = oc.visExpr(d, sub, depth+1); !ok {
				return ocExpr{}, false
			}
		}
		e.args = append(e.args, x)
	}
	return e, true
}

// textString decodes a PDF text string: UTF-16BE or UTF-8 with a byte
// order mark, else PDFDocEncoding.
func textString(o reader.Object) string {
	b, ok := reader.ToString(o)
	if !ok {
		return ""
	}
	switch {
	case len(b) >= 2 && b[0] == 0xfe && b[1] == 0xff:
		u := make([]uint16, 0, len(b)/2-1)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return string(utf16.Decode(u))
	case len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf && utf8.Valid(b[3:]):
		return string(b[3:])
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
		if c >= 0x80 && int(c-0x80) < len(pdfDocHigh) {
			r[i] = pdfDocHigh[c-0x80]
		}
	}
	return string(r)
}

// pdfDocHigh is PDFDocEncoding from 0x80 to 0xA0 (PDF 2.0, D.3); above
// it the encoding is Latin-1.
var pdfDocHigh = [...]rune{
	'•', '†', '‡', '…', '—', '–', 'ƒ', '⁄', '‹', '›', '−', '‰', '„', '“', '”', '‘',
	'’', '‚', '™', 'ﬁ', 'ﬂ', 'Ł', 'Œ', 'Š', 'Ÿ', 'Ž', 'ı', 'ł', 'œ', 'š', 'ž', '�',
	'€',
}
