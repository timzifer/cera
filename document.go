package cera

import (
	"container/list"
	"fmt"
	"io"
	"math"
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
	r *pdf.Document

	fontProvider FontProvider // fonts the document does not embed
	naiveCMYK    bool         // OpenOptions.NaiveCMYK
	devCMYK      *colorSpace  // DeviceCMYK as the options convert it

	fontMu sync.Mutex
	fonts  map[pdf.Ref]*Font // loaded fonts, by reference

	// Decoded images by reference, least recently used last, bounded by
	// imageCacheBytes.
	imgMu    sync.Mutex
	imgs     map[pdf.Ref]*list.Element
	imgLRU   list.List
	imgBytes int
	// Decodes in progress by reference; under imgMu.
	imgFlights map[pdf.Ref]*imageFlight

	ocOnce sync.Once
	oc     *ocProps // optional content, read on first use

	formOnce sync.Once
	form     *Form     // the interactive form, read on first use
	xfa      bool      // the form is an XFA form
	ff       formFonts // fonts of generated widget appearances

	pageIdxOnce sync.Once
	pageIdx     map[pdf.Ref]int // page index by reference, for links

	structOnce  sync.Once
	structMCIDs map[int][]int // marked-content identifiers by page, in structure order

	// Colour spaces and shadings by reference.
	csMu     sync.Mutex
	spaces   map[pdf.Ref]csEntry
	shMu     sync.Mutex
	shadings map[pdf.Ref]*shadingEntry
	patterns map[pdf.Ref]*patternEntry

	// ICCBased CMYK spaces by profile object, and their profiles, at most
	// maxCMYKProfiles; under csMu.
	iccSpaces  map[pdf.Ref]csEntry
	cmykTables map[*cmyk.Table]bool

	// Transfer functions by ExtGState reference.
	trMu      sync.Mutex
	transfers map[pdf.Ref]trEntry
}

// Open parses a PDF file. Damaged cross-reference tables, wrong stream
// lengths and missing objects are repaired by the reader where possible.
func Open(data []byte) (*Document, error) {
	return OpenWith(data, OpenOptions{})
}

// OpenWithPassword parses an encrypted PDF file. It is OpenWith with a
// password.
func OpenWithPassword(data []byte, password string) (*Document, error) {
	return OpenWith(data, OpenOptions{Password: password})
}

// OpenWith parses a PDF file with options: a password, a provider of
// fonts the document does not embed, and how CMYK is converted. It fails
// on a CMYKProfile it cannot read.
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
	devCMYK := spaceCMYK
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
	d, err := read(pdf.Options{Password: opt.Password})
	if err != nil {
		return nil, err
	}
	return &Document{r: d, fontProvider: opt.Fonts, naiveCMYK: opt.NaiveCMYK, devCMYK: devCMYK}, nil
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
		return nil, err
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
type Page struct {
	doc   *Document
	index int
	dict  pdf.Dict

	// Box is the visible area in default user space: CropBox clipped to
	// MediaBox. Its units are /UserUnit points; Size and DisplayMatrix
	// apply it.
	Box Rect
	// Rotate is the clockwise display rotation: 0, 90, 180 or 270.
	Rotate int
	// unit is /UserUnit (points per user-space unit), 1 by default.
	unit float64

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
	box := media
	if crop, ok := d.rect(p.dict.Get("CropBox")); ok {
		c := Rect{max(crop.X0, media.X0), max(crop.Y0, media.Y0), min(crop.X1, media.X1), min(crop.Y1, media.Y1)}
		if c.Dx() > 0 && c.Dy() > 0 {
			box = c
		}
	}
	p.Box = box
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
