package cera

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"maps"
	"math"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/timzifer/stilus"
)

// RenderOptions control Page.Render.
type RenderOptions struct {
	// Scale is pixels per point; 0 means 1 (72 dpi). 150 dpi is 150/72.
	Scale float64
	// Region restricts drawing to part of the page, in device pixels of the
	// whole page at Scale (a tile or viewport). Empty means dst.Bounds().
	Region image.Rectangle
	// Background fills the region before drawing. The zero value leaves it
	// transparent; use color.RGBA{255, 255, 255, 255} for paper.
	Background color.RGBA
	// Deadline stops interpretation; the image then holds what was drawn
	// and Render returns ErrDeadline. The zero value means no deadline.
	Deadline time.Time
	// Stats, when non-nil, receives what rendering did.
	Stats *Stats
	// Workers is the most goroutines drawing the page, each taking
	// horizontal bands of it: 0 means GOMAXPROCS, 1 draws on the calling
	// goroutine in one pass. A page or region estimated to draw quickly
	// is drawn by fewer, or in one pass, since starting and joining
	// goroutines would cost more than it saves. Antialiasing may round
	// differently by one level where bands meet.
	Workers int
	// Layers selects the optional content to draw; nil is the document's
	// default configuration for Usage (see Document.Layers). Switching
	// layers does not interpret the page again.
	Layers *Visibility
	// Usage is UsageView (the default), UsagePrint or UsageExport. It
	// selects the automatic states (/AS) of the default configuration and
	// applies only when Layers is nil.
	Usage Usage
	// Annotations selects the annotations drawn over the page content:
	// AnnotsView (the default), AnnotsPrint, or AnnotsNone.
	Annotations AnnotMode
	// SkipAnnotation, when set, is asked once per render for each
	// annotation the mode shows, by its index in /Annots; those it
	// returns true for are not drawn (a form overlay drawing its own
	// widgets). Changing it does not interpret the page again.
	SkipAnnotation func(index int) bool
	// SimulateOverprint composites objects painted with overprint (OP,
	// op) in DeviceCMYK under OPM 1, Separation or DeviceN as Multiply
	// with what is below them, approximating the mixed result of print;
	// off, they are painted over it, as most viewers do for files that
	// are not PDF/X. The page is interpreted once per setting.
	SimulateOverprint bool
	// Form, when set, supplies the values of form fields: widgets whose
	// value differs from the one the document saved are drawn with a
	// generated appearance (or, for buttons, the appearance of their
	// state). Each is recorded once per value and scale and drawn over
	// the page; the page is not interpreted again. Without it the saved
	// values are shown.
	Form *FormState
	// ImageFilter sets how magnified images that do not ask for
	// /Interpolate are sampled: ImageNearest (the default) samples the
	// nearest pixel, ImageSmooth samples bilinearly while an image is
	// magnified less than 2×, as PDFium, MuPDF and Poppler do (beyond it
	// they draw it crisp, and so does cera). Images with /Interpolate true
	// are always smoothed;
	// minified images are read from their mip levels either way. Switching
	// it does not interpret the page again. Images inside the cells of a
	// tiling pattern are drawn into the pattern's tile when the page is
	// interpreted, at the nearest pixel unless they ask for /Interpolate.
	ImageFilter ImageFilter
	// Band, when set, is called with each part of the region once its
	// pixels are final, so that an encoder can start on the first rows
	// while others are drawn. The parts are horizontal strips spanning
	// the region's width; they do not overlap and together cover the
	// region. They come in no particular order, possibly from several
	// goroutines at once, and all of them before Render returns, also
	// after a deadline or a panic. With Form values to draw over the
	// page, the region is one part.
	Band func(r image.Rectangle)
}

// Stats describes what rendering a page did.
type Stats struct {
	Ops     int // content-stream operators executed, including forms
	Fills   int
	Strokes int
	Clips   int
	Glyphs  int // glyphs filled, and Type 3 glyphs run
	Images  int // images drawn, inline images and stencil masks included
	Groups  int // transparency groups and soft masks, of forms and objects
	// Shadings counts shadings painted, by sh and as the paint of a
	// pattern.
	Shadings int
	// Unsupported counts features that were skipped or approximated, keyed
	// by feature ("shading", "font-missing", "image-filter", ...).
	Unsupported map[string]int
	// Errors counts recoverable problems: malformed operators, missing
	// resources, content that did not decode.
	Errors int
	// Reused reports that the page was not interpreted again: its display
	// list from an earlier render at the same scale was drawn. The other
	// fields are those of the render that recorded it.
	Reused bool
}

// set copies src into s, reusing s's map.
func (s *Stats) set(src *Stats) {
	um := s.Unsupported
	*s = *src
	clear(um)
	if len(src.Unsupported) > 0 {
		if um == nil {
			um = make(map[string]int, len(src.Unsupported))
		}
		maps.Copy(um, src.Unsupported)
	}
	s.Unsupported = um
}

// UnsupportedKeys returns the keys of s.Unsupported in sorted order.
func (s *Stats) UnsupportedKeys() []string {
	keys := make([]string, 0, len(s.Unsupported))
	for k := range s.Unsupported {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *Stats) unsupported(feature string) {
	if s.Unsupported == nil {
		s.Unsupported = map[string]int{}
	}
	s.Unsupported[feature]++
}

// ErrDeadline is returned when rendering stops at RenderOptions.Deadline or
// because the context was cancelled.
var ErrDeadline = errors.New("cera: render deadline exceeded")

// PanicError reports a panic recovered while opening or rendering. It always
// indicates a bug in cera or one of its dependencies.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("cera: panic: %v", e.Value) }

// recoverPanic turns a panic into a *PanicError in *err. Use it deferred.
func recoverPanic(err *error) {
	if v := recover(); v != nil {
		*err = &PanicError{Value: v, Stack: debug.Stack()}
	}
}

// Bounds returns the device-pixel size of the page at scale (pixels per
// point), rounded up.
func (p *Page) Bounds(scale float64) image.Rectangle {
	scale = normScale(scale)
	w, h := p.Size()
	return image.Rect(0, 0, max(1, int(math.Ceil(w*scale))), max(1, int(math.Ceil(h*scale))))
}

func normScale(s float64) float64 {
	if !(s > 0) || math.IsInf(s, 0) {
		return 1
	}
	return s
}

// limit is the deadline of one render, shared by its goroutines.
type limit struct {
	ctx      context.Context
	deadline time.Time
	hit      atomic.Bool
}

func (l *limit) expired() bool {
	if l.hit.Load() {
		return true
	}
	if l.ctx.Err() != nil || (!l.deadline.IsZero() && time.Now().After(l.deadline)) {
		l.hit.Store(true)
		return true
	}
	return false
}

// painter draws display-list bands; painters are pooled, so steady-state
// rendering reuses their canvases.
type painter struct {
	canvas *stilus.Canvas
	dev    RasterDevice
	ds     drawState
	page   image.RGBA // a transparent band, for a page that blends
}

// noImage is what idle painters point at.
var noImage = image.NewRGBA(image.Rectangle{})

var painters = sync.Pool{New: func() any { return new(painter) }}

// recorders are pooled interpreters.
var recorders = sync.Pool{New: func() any { return new(interp) }}

// Render draws the page into dst, whose pixel coordinates are device pixels
// of the whole page at opt.Scale (see Bounds). dst belongs to the caller and
// can be reused across calls, so rendering does not allocate a page image.
//
// The page is interpreted into a display list once per scale; later renders
// at the same scale (other tiles, a scrolled viewport) only draw. Release
// frees the list. Bands of the page are drawn by opt.Workers goroutines.
//
// Broken content does not stop rendering. A non-nil error means the page
// was drawn partially: ErrDeadline, a rasterizer budget, or a *PanicError.
func (p *Page) Render(ctx context.Context, dst *image.RGBA, opt RenderOptions) (err error) {
	region := dst.Bounds()
	if !opt.Region.Empty() {
		region = region.Intersect(opt.Region)
	}
	// The region is reported whole at the end unless the display list
	// reports its parts as it draws them.
	report := opt.Band != nil && !region.Empty()
	defer func() {
		if report {
			opt.Band(region)
		}
	}()
	defer recoverPanic(&err)
	// The display list fills the background band by band, each just
	// before it is drawn; until it takes over, an early return or a panic
	// leaves the region filled here.
	fill := true
	defer func() {
		if fill {
			fillRegion(dst, region, opt.Background)
		}
	}()
	st := opt.Stats
	if st == nil {
		st = new(Stats)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lim := &limit{ctx: ctx, deadline: opt.Deadline}
	if lim.expired() {
		st.set(&Stats{})
		return ErrDeadline
	}
	scale := normScale(opt.Scale)

	dl, reused := p.list(scale, opt.SimulateOverprint, lim)
	defer p.done(dl)
	st.set(&dl.stats)
	st.Reused = reused
	switch {
	case dl.panic != nil:
		err = dl.panic
	case !dl.complete:
		err = ErrDeadline
	}
	if region.Empty() {
		return err
	}
	vis := opt.Layers
	if vis == nil && len(dl.ocTags) > 1 {
		vis = p.doc.defaultVisibility(opt.Usage)
	}
	af := annotFilter{mode: opt.Annotations, skip: opt.SkipAnnotation}
	// Widgets showing values of opt.Form are drawn from lists of their
	// own, over the page; the page's list leaves them out.
	var overlays []*displayList
	pageAF := af
	if vals := opt.Form.snapshot(); len(vals) > 0 && opt.Form.form == p.doc.Form() {
		var own map[int]bool
		overlays, own = p.widgetLists(vals, scale, opt.SimulateOverprint, lim)
		if len(own) > 0 {
			pageAF.skip = func(i int) bool { return own[i] || af.skip != nil && af.skip(i) }
		}
	}
	iso := dl.blends && opt.Background.A != 0
	fill = false
	// Overlays draw over the page's bands after them.
	var band func(image.Rectangle)
	if report && len(overlays) == 0 {
		band, report = opt.Band, false
	}
	if derr := dl.render(dst, region, opt.Background, true, band, opt.Workers, vis, &pageAF, lim, iso, opt.ImageFilter); derr != nil && err == nil {
		err = derr
	}
	if len(overlays) > 0 {
		if vis = opt.Layers; vis == nil {
			vis = p.doc.defaultVisibility(opt.Usage)
		}
	}
	for _, ol := range overlays {
		if derr := ol.render(dst, region, color.RGBA{}, false, nil, opt.Workers, vis, &af, lim, false, opt.ImageFilter); derr != nil && err == nil {
			err = derr
		}
	}
	return err
}

// widgetList is the display list of one widget showing a value other than
// the saved one.
type widgetList struct {
	scale     float64
	overprint bool
	val       Value
	dl        *displayList
}

// widgetLists returns the lists of the page's widgets whose fields vals
// gives values, recording those not cached, and the annotation indices
// they replace in the page's list.
func (p *Page) widgetLists(vals map[*Field]Value, scale float64, overprint bool, lim *limit) ([]*displayList, map[int]bool) {
	var lists []*displayList
	var own map[int]bool
	for _, w := range p.doc.Form().PageWidgets(p.index) {
		v, ok := vals[w.Field]
		if !ok {
			continue
		}
		p.mu.Lock()
		wl := p.wl[w.Annotation]
		p.mu.Unlock()
		if wl == nil || wl.scale != scale || wl.overprint != overprint || !wl.val.Equal(v) {
			dl := p.recordWidget(w, vals, scale, overprint, lim)
			if dl == nil {
				continue
			}
			wl = &widgetList{scale: scale, overprint: overprint, val: v, dl: dl}
			if dl.complete {
				p.mu.Lock()
				if p.wl == nil {
					p.wl = map[int]*widgetList{}
				}
				p.wl[w.Annotation] = wl
				p.mu.Unlock()
			}
		}
		if own == nil {
			own = map[int]bool{}
		}
		own[w.Annotation] = true
		lists = append(lists, wl.dl)
	}
	return lists, own
}

// recordWidget records the annotation of w alone, with the values vals.
// Its list is not pooled: renders may still draw it when it is replaced.
func (p *Page) recordWidget(w *Widget, vals map[*Field]Value, scale float64, overprint bool, lim *limit) (dl *displayList) {
	annots := p.Annotations()
	i, ok := slices.BinarySearchFunc(annots, w.Annotation, func(a Annotation, idx int) int { return a.Index - idx })
	if !ok {
		return nil
	}
	dl = new(displayList)
	dl.reset(p.Bounds(scale))
	dl.scale, dl.overprint = scale, overprint
	base := p.deviceMatrix(scale)
	dl.ClipRect(p.Box, base)
	in := recorders.Get().(*interp)
	func() {
		defer func() {
			if v := recover(); v != nil {
				in = nil // possibly inconsistent; not pooled again
				dl.complete = false
			}
		}()
		in.reset(p.doc, dl, &dl.stats, lim)
		in.overprint = overprint
		in.devBox = p.Bounds(scale)
		in.formVals = vals
		if a := &annots[i]; a.Flags&AnnotHidden == 0 && !in.expired() {
			in.annotation(p, a, base, scale)
		}
		dl.complete = in.err == nil
	}()
	if in != nil {
		in.release()
		recorders.Put(in)
	}
	dl.finish()
	return dl
}

// list returns the display list of the page at scale, with overprint
// simulated or not, recording it unless the cached one fits. Renders that
// want a list being recorded wait for it rather than record it again,
// unless their limit ends first. The caller must pass it to done.
func (p *Page) list(scale float64, overprint bool, lim *limit) (dl *displayList, reused bool) {
	key := listKey{scale, overprint}
	for {
		p.mu.Lock()
		if dl := p.dl; dl != nil && dl.scale == scale && dl.overprint == overprint {
			dl.refs++
			p.mu.Unlock()
			return dl, true
		}
		f, busy := p.flights[key]
		if !busy {
			// Recording stands in the map as nil: the flight is made only
			// when a render comes to wait for it.
			if p.flights == nil {
				p.flights = map[listKey]*listFlight{}
			}
			p.flights[key] = nil
			p.mu.Unlock()
			return p.recordList(key, lim, true), false
		}
		if f == nil {
			f = &listFlight{done: make(chan struct{})}
			p.flights[key] = f
		}
		f.waiters++
		p.mu.Unlock()
		if !p.wait(key, f, lim) {
			// The limit ended: recording stops at once, with an
			// incomplete list of this render's own.
			return p.recordList(key, lim, false), false
		}
		if f.dl != nil && f.dl.complete {
			return f.dl, true
		}
		// The recording stopped at its own limit or panicked: try again.
		if f.dl != nil {
			p.done(f.dl)
		}
	}
}

// listKey is what a display list of a page is recorded for.
type listKey struct {
	scale     float64
	overprint bool
}

// listFlight is a recording in progress that renders wait for.
type listFlight struct {
	done chan struct{} // closed when the recording ends
	dl   *displayList  // the list recorded, nil if recording panicked
	// waiters are the renders waiting, for which dl holds a reference;
	// under Page.mu.
	waiters int
}

// wait waits for f to end and reports whether it did, or whether lim
// ended first and the render gave up waiting.
func (p *Page) wait(key listKey, f *listFlight, lim *limit) bool {
	var timeout <-chan time.Time
	if !lim.deadline.IsZero() {
		t := time.NewTimer(time.Until(lim.deadline))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-f.done:
		return true
	case <-lim.ctx.Done():
	case <-timeout:
	}
	p.mu.Lock()
	if p.flights[key] == f {
		f.waiters--
		p.mu.Unlock()
		lim.hit.Store(true)
		return false
	}
	p.mu.Unlock()
	<-f.done // it ended meanwhile, with a reference for this render
	return true
}

// recordList records the list for key and, when it is complete, caches
// it. If it is the recording of p.flights[key], the renders waiting for it
// get the list too.
func (p *Page) recordList(key listKey, lim *limit, flight bool) (dl *displayList) {
	ended := false
	if flight {
		defer func() {
			if !ended { // recording panicked outside the interpreter
				p.mu.Lock()
				f := p.flights[key]
				delete(p.flights, key)
				p.mu.Unlock()
				if f != nil {
					close(f.done)
				}
			}
		}()
	}
	scale, overprint := key.scale, key.overprint
	dl = getList()
	dl.reset(p.Bounds(scale))
	dl.scale, dl.overprint = scale, overprint
	in := recorders.Get().(*interp)
	func() {
		defer func() {
			if v := recover(); v != nil {
				// An interpreter that panicked may hold inconsistent state;
				// its list is incomplete and not cached.
				in = nil
				dl.complete = false
				dl.panic = &PanicError{Value: v, Stack: debug.Stack()}
			}
		}()
		dl.complete = p.record(in, dl, &dl.stats, scale, overprint, nil, nil, nil, lim)
	}()
	if in != nil {
		in.release()
		recorders.Put(in)
	}
	dl.finish()

	p.mu.Lock()
	dl.refs++
	var f *listFlight
	if flight {
		if f = p.flights[key]; f != nil {
			dl.refs += f.waiters
			f.dl = dl
		}
		delete(p.flights, key)
	}
	if dl.complete {
		if old := p.dl; old != nil && old.refs == 0 {
			putList(old)
		}
		p.dl = dl
	}
	p.mu.Unlock()
	ended = true
	if f != nil {
		close(f.done)
	}
	return dl
}

// record interprets the page into dev at scale and reports whether it got
// to the end. A display list records all optional content and all
// annotations that can be drawn; any other device sees what vis and af
// show. Widgets show the field values vals, and the saved ones for fields
// it leaves out.
func (p *Page) record(in *interp, dev Device, st *Stats, scale float64, overprint bool, vis *Visibility, af *annotFilter, vals map[*Field]Value, lim *limit) bool {
	base := p.deviceMatrix(scale)
	dev.ClipRect(p.Box, base)
	in.reset(p.doc, dev, st, lim)
	in.overprint = overprint
	in.devBox = p.Bounds(scale)
	in.formVals = vals
	if vis != nil {
		in.ocVis, in.ocZoom = vis, scale
	}
	// The interpreter checks every checkEvery operators: a short page
	// would otherwise be recorded whole after the deadline.
	if in.expired() {
		return false
	}
	res := p.doc.dict(p.dict.Get("Resources"))
	dec, derr := p.doc.r.PageContents(p.index + 1)
	switch {
	case derr != nil:
		st.Errors++
	default:
		if dec.Recovered {
			st.Errors++
		}
		in.run(dec.Data, res, base, 0)
	}
	if in.err == nil {
		in.drawAnnots(p, base, scale, af)
	}
	return in.err == nil
}

// Run interprets the page into dev, without a display list: device space
// is the page at scale pixels per point, as for Render. It is how devices
// other than the raster one (text extraction, hit testing) see a page. The
// page's box is the outermost clip. Optional content is drawn as the
// document's default configuration shows it on screen. st, when non-nil,
// receives the statistics. The error is ErrDeadline when ctx ends first,
// or a *PanicError.
func (p *Page) Run(ctx context.Context, dev Device, scale float64, st *Stats) error {
	return p.RunWith(ctx, dev, RunOptions{Scale: scale, Stats: st})
}

// RunOptions control Page.RunWith.
type RunOptions struct {
	// Scale is pixels per point, as for RenderOptions.
	Scale float64
	// Stats, when non-nil, receives what interpreting did.
	Stats *Stats
	// Layers and Usage select the optional content dev sees, as for
	// RenderOptions; hidden content reaches dev only as its clips, and
	// a TextDevice does not see its text.
	Layers *Visibility
	Usage  Usage
	// Annotations and SkipAnnotation select the annotations, as for
	// RenderOptions. A TextDevice sees the text of their appearances.
	Annotations    AnnotMode
	SkipAnnotation func(index int) bool
	// SimulateOverprint is as for RenderOptions.
	SimulateOverprint bool
	// Form supplies field values, as for RenderOptions; a TextDevice
	// sees the text of the generated appearances.
	Form *FormState
}

// RunWith is Run with options.
func (p *Page) RunWith(ctx context.Context, dev Device, opt RunOptions) (err error) {
	defer recoverPanic(&err)
	if ctx == nil {
		ctx = context.Background()
	}
	st := opt.Stats
	if st == nil {
		st = new(Stats)
	} else {
		st.set(&Stats{})
	}
	vis := opt.Layers
	if vis == nil {
		vis = p.doc.defaultVisibility(opt.Usage)
	}
	lim := &limit{ctx: ctx}
	in := recorders.Get().(*interp)
	af := annotFilter{mode: opt.Annotations, skip: opt.SkipAnnotation}
	var vals map[*Field]Value
	if opt.Form != nil && opt.Form.form == p.doc.Form() {
		vals = opt.Form.snapshot()
	}
	ok := p.record(in, dev, st, normScale(opt.Scale), opt.SimulateOverprint, vis, &af, vals, lim)
	dev.PopClip()
	in.release()
	recorders.Put(in)
	if !ok {
		return ErrDeadline
	}
	return nil
}

// done ends a render's use of dl.
func (p *Page) done(dl *displayList) {
	p.mu.Lock()
	dl.refs--
	if dl.refs == 0 && p.dl != dl {
		putList(dl)
	}
	p.mu.Unlock()
}

// Release frees the display list cached by Render. The page stays usable.
func (p *Page) Release() {
	p.mu.Lock()
	if dl := p.dl; dl != nil {
		p.dl = nil
		if dl.refs == 0 {
			putList(dl)
		}
	}
	p.wl = nil
	p.mu.Unlock()
}

// render draws the part of the list inside region into dst, with the
// optional content vis and the annotations af show (vis nil: all layers).
// One worker draws the region in one pass; several share the bands that
// touch it. With iso set, each band is drawn onto a transparent image
// first and then composited onto dst, which holds the background. Images
// are magnified with filter.
func (l *displayList) render(dst *image.RGBA, region image.Rectangle, bg color.RGBA, fill bool, band func(image.Rectangle), workers int, vis *Visibility, af *annotFilter, lim *limit, iso bool, filter ImageFilter) error {
	b0, b1 := l.bandRange(region)
	// The periodic checks come after hundreds of items; a short list
	// would otherwise be drawn whole after the deadline.
	if b0 >= b1 || lim.expired() {
		if fill {
			fillRegion(dst, region, bg)
		}
		if band != nil {
			band(region)
		}
		if b0 >= b1 {
			return nil
		}
		return ErrDeadline
	}
	if fill {
		// The bands cover the page; the rest of region is filled now.
		fillOutside(dst, region, l.bounds, bg)
	}
	in := region.Intersect(l.bounds)
	if band != nil {
		// The rows above and below the page are final.
		if in.Min.Y > region.Min.Y {
			band(rows(region, region.Min.Y, in.Min.Y))
		}
		if region.Max.Y > in.Max.Y {
			band(rows(region, in.Max.Y, region.Max.Y))
		}
	}
	workers, whole := l.plan(region, b0, b1, workers)
	j := jobs.Get().(*job)
	if vis == nil {
		vis = &noLayers
	}
	// A cheap page drawn in one pass touches few of its pixels after the
	// fill; a large one is filled past the caches. A list drawn over
	// another (a widget showing a form value) fills nothing.
	stream := fill && whole && 4*region.Dx()*region.Dy() >= streamFill && l.workersFor(region) == 1
	*j = job{l: l, dst: dst, region: region, bg: bg, fill: fill, stream: stream, band: band, lim: lim, b0: b0, b1: b1, iso: iso, filter: filter, vis: l.visibleTags(j.vis, vis, af), buf: j.buf, told: j.told}
	if whole {
		j.idx, j.buf = l.regionItems(j.buf, b0, b1)
		j.work(true)
		if band != nil {
			band(rows(region, in.Min.Y, in.Max.Y))
		}
	} else {
		if band != nil {
			j.told = append(j.told[:0], make([]bool, b1-b0)...)
		}
		// The caller draws bands too.
		j.next.Store(int32(b0))
		j.wg.Add(workers - 1)
		for range workers - 1 {
			go j.helper()
		}
		j.work(false)
		j.wg.Wait()
		if fill {
			// Bands left after a deadline or a panic get the background.
			for b := int(j.next.Load()); b < b1; b++ {
				fillRegion(dst, l.band(b).Intersect(region), bg)
			}
		}
		if band != nil {
			// Bands no worker finished: left, or whose drawing panicked.
			for b := b0; b < b1; b++ {
				if !j.told[b-b0] {
					band(j.bandRows(b))
				}
			}
		}
	}
	err := j.err
	if err == nil && lim.hit.Load() {
		err = ErrDeadline
	}
	*j = job{vis: j.vis[:0], buf: j.buf[:0], told: j.told[:0]}
	jobs.Put(j)
	return err
}

// Drawing is shared between workers only when it is estimated to take
// at least serialBelow on one core, and then by about one worker per
// perWorker of it. Starting goroutines, waking cores for them and joining
// them takes tens of microseconds; on a page that draws faster several
// workers were slower than one (#63).
const (
	serialBelow = 150e3 // ns
	perWorker   = 100e3 // ns
)

// adaptWorkers is off in tests that hold the drawing of bands by several
// workers against one pass, on pages that would not be shared.
var adaptWorkers = true

// workersFor returns how many workers drawing region is worth, from the
// list's estimated cost scaled to the part of the page region covers.
func (l *displayList) workersFor(region image.Rectangle) int {
	r := region.Intersect(l.bounds)
	page := float64(l.bounds.Dx()) * float64(l.bounds.Dy())
	if r.Empty() || page == 0 {
		return 1
	}
	est := l.cost * float64(r.Dx()) * float64(r.Dy()) / page
	if est < serialBelow {
		return 1
	}
	return max(2, int(est/perWorker))
}

// plan returns how many workers draw region, bands [b0, b1), given at
// most workers (0: GOMAXPROCS), and whether they draw it in one pass.
//
// A page that is not cheap is drawn band by band even by one worker if
// few items span bands: a band filled with the background just before it
// is drawn is still in the cache, the whole page filled first is not, and
// pages drawn at once wait on memory (#63). Items spanning bands are
// replayed in each, which costs more than that on pages of large images,
// clips and shadings.
func (l *displayList) plan(region image.Rectangle, b0, b1, workers int) (int, bool) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = min(workers, b1-b0)
	if !adaptWorkers {
		return workers, workers == 1
	}
	w := l.workersFor(region)
	if w == 1 {
		return 1, true
	}
	workers = min(workers, w)
	replays := int(l.bandStart[b1] - l.bandStart[b0])
	return workers, workers == 1 && replays > 2*len(l.allItems)
}

// streamFill is the size in bytes from which a region drawn in one pass
// is filled past the caches: larger than a core's L2 cache.
const streamFill = 2 << 20

// job is one render of a display list, shared by its workers.
type job struct {
	l      *displayList
	dst    *image.RGBA
	region image.Rectangle
	bg     color.RGBA // with fill, set in each part before it is drawn
	fill   bool
	stream bool // fill with fillRegionStream
	band   func(image.Rectangle)
	told   []bool // per band from b0: passed to band
	lim    *limit
	b0, b1 int
	vis    []bool  // per tag of the list: drawn
	idx    []int32 // the items a single pass draws
	buf    []int32 // storage of idx when it is not the list's
	iso    bool
	filter ImageFilter
	next   atomic.Int32 // next band to draw
	wg     sync.WaitGroup
	mu     sync.Mutex
	err    error
}

var jobs = sync.Pool{New: func() any { return new(job) }}

func (j *job) report(err error) {
	j.mu.Lock()
	if j.err == nil {
		j.err = err
	}
	j.mu.Unlock()
}

// helper draws bands beside the caller.
func (j *job) helper() {
	defer j.wg.Done()
	j.work(false)
}

// work draws the whole region in one pass, or bands until none are left.
func (j *job) work(whole bool) {
	pt := painters.Get().(*painter)
	defer func() {
		if v := recover(); v != nil {
			// A painter that panicked may hold inconsistent state.
			j.report(&PanicError{Value: v, Stack: debug.Stack()})
			j.lim.hit.Store(true) // stop the other workers
		} else {
			pt.dev.Reset(noImage, noImage.Rect) // do not keep dst alive
			putGlyphCache(pt.dev.glyphs)
			pt.dev.glyphs = nil
			painters.Put(pt)
		}
	}()
	if pt.canvas == nil {
		pt.canvas = stilus.NewCanvas(noImage)
		pt.dev.C = pt.canvas
	}
	pt.dev.glyphs = getGlyphCache()
	if whole {
		j.paint(pt, -1, j.region)
		return
	}
	for !j.lim.hit.Load() {
		b := int(j.next.Add(1)) - 1
		if b >= j.b1 {
			return
		}
		j.paint(pt, b, j.l.band(b).Intersect(j.region))
		if j.band != nil {
			j.told[b-j.b0] = true
			j.band(j.bandRows(b))
		}
	}
}

// bandRows returns the rows of band b in the region, across its width.
func (j *job) bandRows(b int) image.Rectangle {
	r := j.l.band(b).Intersect(j.region)
	return rows(j.region, r.Min.Y, r.Max.Y)
}

// rows returns rows y0 to y1 of r.
func rows(r image.Rectangle, y0, y1 int) image.Rectangle {
	return image.Rect(r.Min.X, y0, r.Max.X, y1)
}

// paint draws r, band b of the list or, if b < 0, the items j.idx.
func (j *job) paint(pt *painter, b int, r image.Rectangle) {
	if j.stream {
		fillRegionStream(j.dst, r, j.bg)
	} else if j.fill {
		fillRegion(j.dst, r, j.bg)
	}
	dst := j.dst
	if j.iso {
		n := 4 * r.Dx() * r.Dy()
		if cap(pt.page.Pix) < n {
			pt.page.Pix = make([]uint8, n)
		}
		pt.page = image.RGBA{Pix: pt.page.Pix[:n], Stride: 4 * r.Dx(), Rect: r}
		clear(pt.page.Pix)
		dst = &pt.page
		defer compositeOver(j.dst, &pt.page)
	}
	pt.dev.Reset(dst, r)
	pt.dev.ImageFilter = j.filter
	var ok bool
	if b < 0 {
		ok = j.l.drawItems(&pt.dev, &pt.ds, j.idx, r, j.vis, j.lim)
	} else {
		ok = j.l.drawBand(&pt.dev, &pt.ds, b, r, j.vis, j.lim)
	}
	if err := pt.dev.Err(); err != nil {
		j.report(err)
	} else if !ok {
		j.report(ErrDeadline)
	}
}

// compositeOver composites src over dst within src's rectangle.
func compositeOver(dst, src *image.RGBA) {
	r := src.Rect
	n := 4 * r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		s := src.Pix[src.PixOffset(r.Min.X, y):][:n:n]
		d := dst.Pix[dst.PixOffset(r.Min.X, y):][:n:n]
		for i := 0; i < n; i += 4 {
			switch a := uint32(s[i+3]); a {
			case 0:
			case 255:
				copy(d[i:i+4], s[i:i+4])
			default:
				k := 255 - a
				for c := i; c < i+4; c++ {
					d[c] = s[c] + mulByte(d[c], k)
				}
			}
		}
	}
}

// DisplayMatrix maps default user space to points of the displayed page:
// y down, origin at the top-left corner of Box, rotated clockwise by
// /Rotate and scaled by /UserUnit. Text, links and the boxes of the text
// API are in these points; a viewer scales them by its zoom. Its inverse
// maps them back to user space.
func (p *Page) DisplayMatrix() Matrix { return p.deviceMatrix(1) }

// deviceMatrix maps default user space to device pixels: y down, origin at
// the top-left corner of the visible box, rotated clockwise by /Rotate.
func (p *Page) deviceMatrix(scale float64) Matrix {
	s := scale * p.unit
	b := p.Box
	switch p.Rotate {
	case 90:
		return Matrix{0, s, s, 0, -b.Y0 * s, -b.X0 * s}
	case 180:
		return Matrix{-s, 0, 0, s, b.X1 * s, -b.Y0 * s}
	case 270:
		return Matrix{0, -s, -s, 0, b.Y1 * s, b.X1 * s}
	}
	return Matrix{s, 0, 0, -s, -b.X0 * s, b.Y1 * s}
}

// fillOutside fills the parts of r outside in with c.
func fillOutside(dst *image.RGBA, r, in image.Rectangle, c color.RGBA) {
	in = in.Intersect(r)
	if in.Empty() {
		fillRegion(dst, r, c)
		return
	}
	fillRegion(dst, image.Rect(r.Min.X, r.Min.Y, r.Max.X, in.Min.Y), c)
	fillRegion(dst, image.Rect(r.Min.X, in.Max.Y, r.Max.X, r.Max.Y), c)
	fillRegion(dst, image.Rect(r.Min.X, in.Min.Y, in.Min.X, in.Max.Y), c)
	fillRegion(dst, image.Rect(in.Max.X, in.Min.Y, r.Max.X, in.Max.Y), c)
}

// fillRegion sets every pixel of r in dst to c by doubling copies.
func fillRegion(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	if r.Empty() {
		return
	}
	row := dst.Pix[dst.PixOffset(r.Min.X, r.Min.Y):][:4*r.Dx()]
	row[0], row[1], row[2], row[3] = c.R, c.G, c.B, c.A
	for n := 4; n < len(row); n *= 2 {
		copy(row[n:], row[:n])
	}
	for y := r.Min.Y + 1; y < r.Max.Y; y++ {
		copy(dst.Pix[dst.PixOffset(r.Min.X, y):][:len(row)], row)
	}
}

// premul converts straight-alpha components in [0, 1] to premultiplied
// RGBA8.
func premul(r, g, b, a float64) color.RGBA {
	a = clamp01(a)
	return color.RGBA{
		R: uint8(clamp01(r)*a*255 + 0.5),
		G: uint8(clamp01(g)*a*255 + 0.5),
		B: uint8(clamp01(b)*a*255 + 0.5),
		A: uint8(a*255 + 0.5),
	}
}

func clamp01(v float64) float64 {
	if !(v > 0) { // also NaN
		return 0
	}
	return min(v, 1)
}
