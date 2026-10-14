package cera

import (
	"fmt"
	"io"
	"math"
	"runtime"
	"sync"

	"github.com/timzifer/cera/internal/pdf"

	"github.com/timzifer/cera/internal/cmyk"
)

// Document is an open PDF file. It is safe for concurrent use: pages of one
// Document may be rendered from many goroutines at once, and what they read
// from the file (objects, decoded streams, fonts, images, colour spaces) is
// loaded once and shared. Once a page has been rendered at a scale, further
// renders of it at that scale only draw its display list.
type Document struct {
	*docShared
	cmykView
}

// docShared is what a document has once for all its views (cmykView).
type docShared struct {
	r *pdf.Document

	fontProvider FontProvider // fonts the document does not embed
	naiveCMYK    bool         // OpenOptions.NaiveCMYK
	ignoreIntent bool         // OpenOptions.IgnoreOutputIntent
	intentOnce   sync.Once
	intent       *colorSpace // DeviceCMYK through the output intent, nil without one
	intentApprox string      // what an output intent cera cannot use is counted as

	fontMu sync.Mutex
	fonts  map[pdf.Ref]*Font // loaded fonts, by reference

	// Decoded images: the document's own cache or the one it shares
	// (OpenOptions.Cache), which knows its images by the id of a view.
	imgs        *imageCache
	sharedCache bool // imgs is an OpenOptions.Cache

	ocOnce sync.Once
	oc     *ocProps // optional content, read on first use

	formOnce sync.Once
	form     *Form     // the interactive form, read on first use
	xfa      bool      // the form is an XFA form
	ff       formFonts // fonts of generated widget appearances

	pageIdxOnce sync.Once
	pageIdx     map[pdf.Ref]int // page index by reference, for links

	outlineOnce sync.Once
	outline     []OutlineItem // the document outline, read on first use

	sigOnce sync.Once
	sigs    []Signature // the signatures, read on first use

	attachOnce  sync.Once
	attachments []*Attachment // the embedded files, read on first use

	labelOnce    sync.Once
	labels       []labelRange // the page labels, read on first use
	labelIdxOnce sync.Once
	labelIdx     map[string]int // the first page of each label

	structOnce  sync.Once
	structMCIDs map[int][]int // marked-content identifiers by page, in structure order

	structTreeOnce sync.Once
	structTree     *StructTree // the structure tree, read on first use

	// CMYK profiles by profile object — a nil space for one that is not
	// used, with the key it is counted under — and their tables, at most
	// maxCMYKProfiles; under csMu.
	csMu       sync.Mutex
	iccSpaces  map[pdf.Ref]csEntry
	iccTables  int // grey and RGB profiles tabulated
	cmykTables map[*cmyk.Table]bool

	// Transfer functions by ExtGState reference.
	trMu      sync.Mutex
	transfers map[pdf.Ref]trEntry

	// The views of pages with an output intent of their own (Page.cmykDoc):
	// by DeviceCMYK, and by page.
	viewMu    sync.Mutex
	views     map[csEntry]*Document
	pageViews map[pdf.Ref]*Document
}

// cmykView is DeviceCMYK as a document converts it, and what is made of
// it: colour spaces, shadings and patterns by reference, decoded images.
// The pages of a document convert it alike but those with an output
// intent of their own (PDF 2.0, 14.11.5), which are drawn with a view of
// the document of their own (Page.cmykDoc).
type cmykView struct {
	devCMYK   *colorSpace // DeviceCMYK as the options or the page convert it; nil: as the document says, see deviceCMYK
	devApprox string      // what devCMYK is counted as

	id uint64 // of the view's images in imgs
	// Decodes in progress by reference.
	imgMu      sync.Mutex
	imgFlights map[pdf.Ref]*imageFlight

	// Colour spaces and shadings by reference.
	spMu     sync.Mutex
	spaces   map[pdf.Ref]csEntry
	shMu     sync.Mutex
	shadings map[pdf.Ref]*shadingEntry
	patterns map[pdf.Ref]*patternEntry
}

// newDocument returns a document of r, converting DeviceCMYK as it says.
func newDocument(r *pdf.Document) *Document {
	return &Document{docShared: &docShared{r: r}, cmykView: cmykView{id: docIDs.Add(1)}}
}

// Open parses a PDF file. Damaged cross-reference tables, wrong stream
// lengths and missing objects are repaired by the reader where possible.
// It returns [ErrPassword] for an encrypted file the empty password does
// not open, and [ErrUnsupportedEncryption] for one cera cannot decrypt.
func Open(data []byte) (*Document, error) {
	return OpenWith(data, OpenOptions{})
}

// OpenWithPassword parses an encrypted PDF file. It is OpenWith with a
// password.
func OpenWithPassword(data []byte, password string) (*Document, error) {
	return OpenWith(data, OpenOptions{Password: password})
}

// OpenWith parses a PDF file with options: a password, a provider of
// fonts the document does not embed, how CMYK is converted, and how much
// the document keeps decoded. It fails on a CMYKProfile it cannot read.
func OpenWith(data []byte, opt OpenOptions) (doc *Document, err error) {
	return open(func(o pdf.Options) (*pdf.Document, error) { return pdf.OpenWith(data, o) }, opt)
}

// OpenReaderAt is OpenWith for a file of size bytes read through r as it
// is needed, rather than held in memory: a large file, or one in a browser
// read through the File API. r must not change while the Document is used,
// and must be safe for concurrent use if pages are rendered concurrently,
// as an *os.File is. A file with damaged cross-reference tables is read in
// full once to rebuild them.
func OpenReaderAt(r io.ReaderAt, size int64, opt OpenOptions) (doc *Document, err error) {
	return open(func(o pdf.Options) (*pdf.Document, error) { return pdf.OpenReaderAt(r, size, o) }, opt)
}

func open(read func(pdf.Options) (*pdf.Document, error), opt OpenOptions) (doc *Document, err error) {
	defer recoverPanic(&err)
	var devCMYK *colorSpace
	if opt.CMYKProfile != nil {
		t, err := cmyk.Load(opt.CMYKProfile)
		if err != nil {
			return nil, fmt.Errorf("cera: CMYKProfile: %w", err)
		}
		devCMYK = &colorSpace{kind: csCMYK, n: 4, cmyk: t}
	}
	if opt.NaiveCMYK {
		devCMYK = spaceCMYKNaive
	}
	po := pdf.Options{Password: opt.Password}
	var imgs *imageCache
	if c := opt.Cache; c != nil {
		po.StreamCache, imgs = c.streams, c.images
	} else {
		// The reader takes 0 for its default and a negative number for none.
		po.StreamCacheBytes = int(max(min(opt.StreamCacheBytes, math.MaxInt), -1))
		imgs = newImageCache(opt.ImageCacheBytes)
	}
	d, err := read(po)
	if err != nil {
		return nil, openError(err)
	}
	doc = newDocument(d)
	doc.fontProvider, doc.naiveCMYK, doc.ignoreIntent = opt.Fonts, opt.NaiveCMYK, opt.IgnoreOutputIntent
	doc.imgs, doc.sharedCache = imgs, opt.Cache != nil
	doc.devCMYK = devCMYK
	doc.dropWithCache()
	return doc, nil
}

// dropWithCache drops, when d is collected, what a shared cache has of
// it: a shared cache outlives the document.
func (d *Document) dropWithCache() {
	if d.sharedCache {
		imgs := d.imgs
		runtime.AddCleanup(d, func(id uint64) { imgs.drop(id) }, d.id)
	}
}

// NumPages returns the number of pages.
func (d *Document) NumPages() int { return d.r.PageCount() }

// Page returns page i (0-based). Pages are lightweight; nothing is
// interpreted until the page is rendered.
func (d *Document) Page(i int) (p *Page, err error) {
	defer recoverPanic(&err)
	if i < 0 || i >= d.NumPages() {
		return nil, fmt.Errorf("cera: page %d out of range [0, %d)", i, d.NumPages())
	}
	pd, err := d.r.Page(i + 1)
	if err != nil {
		return nil, fmt.Errorf("cera: %w", err)
	}
	p = &Page{doc: d, index: i, dict: pd}
	p.geometry()
	return p, nil
}

// Rect is an axis-aligned rectangle.
type Rect struct{ X0, Y0, X1, Y1 float64 }

// Dx returns the width of r.
func (r Rect) Dx() float64 { return r.X1 - r.X0 }

// Dy returns the height of r.
func (r Rect) Dy() float64 { return r.Y1 - r.Y0 }

// letter is used when a page has no usable MediaBox.
var letter = Rect{0, 0, 612, 792}

// Page is one page of a Document. It caches the display list of its last
// render; Release frees it.
//
// A viewer that turns its view uses [Page.Rotated]: the page as it is
// shown is then a Page of its own, and everything given in points of the
// displayed page — Bounds, Render, the text, links, annotations — is in
// its coordinates.
type Page struct {
	doc   *Document
	index int
	dict  pdf.Dict

	// Box is the visible area in default user space: CropBox clipped to
	// MediaBox, or for a page made by InBox the box it shows. Its units
	// are /UserUnit points; Size and DisplayMatrix apply it.
	Box Rect
	// Rotate is the clockwise display rotation: 0, 90, 180 or 270, the
	// /Rotate of the page and what Rotated added to it. It is for
	// reading: a page is rendered, measured and cached as it was made, so
	// setting Rotate after its first use draws one rotation in the size
	// of another. Use Rotated for another rotation.
	Rotate int
	// unit is /UserUnit (points per user-space unit), 1 by default.
	unit float64
	// boxes are the boxes of the page; see Boxes.
	boxes PageBoxes

	mu sync.Mutex
	dl *displayList // cached by Render; see Release
	// flights are the lists being recorded; see list.
	flights map[listKey]*listFlight
	// wl are the lists of widgets showing values of a FormState, by
	// annotation index; see Render.
	wl map[int]*widgetList

	annOnce sync.Once
	annots  []Annotation
	annBad  int // unreadable entries of /Annots

	// root is the page Rotated or InBox was called on, nil for that page
	// itself, turn what was added to its rotation and box the box shown.
	// The root keeps its views, so that a view asked for again has its
	// display list still.
	root  *Page
	turn  int
	box   PageBox
	views [numBoxes][4]*Page // by box and quarter turn, but for the page itself; under mu
}

// PageBox names one of the boxes of a page.
type PageBox int

const (
	BoxCrop  PageBox = iota // what a viewer shows and a printer prints; the default
	BoxMedia                // the medium the page is printed on, with all its marks
	BoxBleed                // what is printed and then cut off around the trimmed page
	BoxTrim                 // the finished page, after trimming
	BoxArt                  // the content its author means, as for placing the page in another
	numBoxes
)

// PageBoxes are the boxes of a page in default user space, as Box is,
// each clipped to the MediaBox, with the defaults of the specification
// for one the page does not have or has no area of within the MediaBox:
// Crop is then Media, and Bleed, Trim and Art are Crop.
type PageBoxes struct{ Media, Crop, Bleed, Trim, Art Rect }

// Box returns the box b names, and Crop for none of them.
func (b PageBoxes) Box(box PageBox) Rect {
	switch box {
	case BoxMedia:
		return b.Media
	case BoxBleed:
		return b.Bleed
	case BoxTrim:
		return b.Trim
	case BoxArt:
		return b.Art
	}
	return b.Crop
}

// Boxes returns the boxes of the page. They are in default user space
// whatever the page shows; [Page.DisplayMatrix] maps them to points of the
// displayed page, where a viewer of print files outlines them.
func (p *Page) Boxes() PageBoxes { return p.boxes }

// InBox returns the page showing another of its boxes than the CropBox:
// the MediaBox with the marks around a page for print, or the TrimBox for
// a thumbnail of the finished page. It is a view of the page as
// [Page.Rotated] makes one, with Box, Size, Bounds and DisplayMatrix of
// that box; the two combine, in either order. Render draws what is inside
// the box, and "points of the displayed page" have their origin at its
// corner. InBox(BoxCrop) is the page as the document shows it.
//
// APIs that take a page number ask for the box as they do for the
// rotation: [TextIndex.SetBox], [NewAnnotation.Box], [Highlight.Box].
func (p *Page) InBox(box PageBox) *Page {
	root, turn, _ := p.origin()
	if box < 0 || box >= numBoxes {
		box = BoxCrop
	}
	return root.view(box, turn)
}

// Rotated returns the page viewed turned by degrees more, clockwise: a
// multiple of 90, others are rounded to one. It is the page itself for a
// multiple of 360, and else a view of it — a Page with Rotate turned, its
// own bounds, display list and text, and the same content. Asked for
// again, the same view is returned, so what it cached is still there;
// Release on the page frees the views' lists too.
//
// "Points of the displayed page" are those of the view everywhere: of
// what Render draws, of [PageText], of links, [Annotation.Markup] and
// [Page.EditAnnotation], whose result [Editor.SetAnnotation] takes back
// with its view. APIs that take a page number ask for the view where it
// matters: [Page.Destination], [TextIndex.SetRotation],
// [NewAnnotation.View], [Highlight.View].
func (p *Page) Rotated(degrees int) *Page {
	root, turn, box := p.origin()
	q := int(math.Round(float64(degrees) / 90))
	return root.view(box, ((turn+q*90)%360+360)%360)
}

// origin returns the page p is a view of, p itself if it is none, and how
// p views it.
func (p *Page) origin() (root *Page, turn int, box PageBox) {
	if p.root != nil {
		return p.root, p.turn, p.box
	}
	return p, 0, BoxCrop
}

// view returns the view of p, a page that is no view, that shows box
// turned by turn, one of 0, 90, 180 and 270.
func (p *Page) view(box PageBox, turn int) *Page {
	if box == BoxCrop && turn == 0 {
		return p
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	v := &p.views[box][turn/90]
	if *v == nil {
		shown := p.Box
		if box != BoxCrop {
			shown = p.boxes.Box(box)
		}
		*v = &Page{doc: p.doc, index: p.index, dict: p.dict, Box: shown, boxes: p.boxes, unit: p.unit,
			Rotate: (p.Rotate + turn) % 360, root: p, turn: turn, box: box}
	}
	return *v
}

// Index returns the 0-based page number.
func (p *Page) Index() int { return p.index }

// Size returns the displayed size in points, after /Rotate and /UserUnit.
func (p *Page) Size() (w, h float64) {
	w, h = p.Box.Dx()*p.unit, p.Box.Dy()*p.unit
	if p.Rotate == 90 || p.Rotate == 270 {
		return h, w
	}
	return w, h
}

func (p *Page) geometry() {
	d := p.doc
	media, ok := d.rect(p.dict.Get("MediaBox"))
	if !ok {
		media = letter
	}
	// box returns the box of the page under key within the MediaBox, and
	// def for none with an area there.
	box := func(key pdf.Name, def Rect) Rect {
		if r, ok := d.rect(p.dict.Get(key)); ok {
			c := Rect{max(r.X0, media.X0), max(r.Y0, media.Y0), min(r.X1, media.X1), min(r.Y1, media.Y1)}
			if c.Dx() > 0 && c.Dy() > 0 {
				return c
			}
		}
		return def
	}
	crop := box("CropBox", media)
	p.boxes = PageBoxes{Media: media, Crop: crop, Bleed: box("BleedBox", crop), Trim: box("TrimBox", crop), Art: box("ArtBox", crop)}
	p.Box = crop
	if v, ok := d.num(p.dict.Get("Rotate")); ok {
		r := int(math.Mod(math.Round(v/90), 4))
		p.Rotate = (r + 4) % 4 * 90
	}
	p.unit = 1
	if v, ok := d.num(p.dict.Get("UserUnit")); ok && v > 0 && v < 1000 {
		p.unit = v
	}
}

// resolve follows references; broken ones become null.
func (d *Document) resolve(o pdf.Object) pdf.Object { return d.r.Resolve(o) }

func (d *Document) dict(o pdf.Object) pdf.Dict {
	v, _ := d.resolve(o).Dict()
	return v
}

func (d *Document) num(o pdf.Object) (float64, bool) {
	f, ok := d.resolve(o).Float()
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func (d *Document) name(o pdf.Object) (pdf.Name, bool) {
	return d.resolve(o).Name()
}

func (d *Document) rect(o pdf.Object) (Rect, bool) {
	a, ok := d.resolve(o).Array()
	if !ok || len(a) != 4 {
		return Rect{}, false
	}
	var v [4]float64
	for i, e := range a {
		if v[i], ok = d.num(e); !ok {
			return Rect{}, false
		}
	}
	r := Rect{min(v[0], v[2]), min(v[1], v[3]), max(v[0], v[2]), max(v[1], v[3])}
	if r.Dx() <= 0 || r.Dy() <= 0 {
		return Rect{}, false
	}
	return r, true
}
