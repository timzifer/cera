package cera

import "github.com/timzifer/cera/internal/pdf"

// ViewSettings is how a document asks to be opened and printed: the
// /OpenAction, /PageMode, /PageLayout and /ViewerPreferences of its
// catalogue (PDF 2.0, 7.7.2 and 12.2), with the defaults of the
// specification where the document says nothing or says something else
// than the specification knows. They are wishes: a viewer follows those
// that suit it.
type ViewSettings struct {
	// Open is where to start: the page and view of /OpenAction, a
	// destination or an action that leads to a place in the document,
	// which [Document.Destination] or [LinkTarget.NamedPage] resolves. It
	// is nil without one, and for an action that leads elsewhere or runs
	// something — a script, an address, another file: nothing a viewer
	// should do because a file was opened.
	Open *LinkTarget
	// PageMode is what is shown with the pages: "UseNone" (nothing, the
	// default), "UseOutlines" (the outline), "UseThumbs" (thumbnails),
	// "FullScreen", "UseOC" (the layers) or "UseAttachments".
	PageMode string
	// AfterFullScreen is the PageMode on leaving full screen, for a
	// document whose PageMode is "FullScreen": "UseNone" (the default),
	// "UseOutlines", "UseThumbs" or "UseOC".
	AfterFullScreen string
	// PageLayout is how the pages are laid out: "SinglePage" (one at a
	// time, the default), "OneColumn" (one column, continuous),
	// "TwoColumnLeft" and "TwoColumnRight" (two columns, odd pages on
	// that side), "TwoPageLeft" and "TwoPageRight" (two pages at a time).
	// The layouts with odd pages on the right start with a cover page.
	PageLayout string
	// RightToLeft says that the document reads from right to left: the
	// pages of a spread run from the right.
	RightToLeft bool
	// DisplayDocTitle says that the title bar shows the title of the
	// document ([Metadata.Title]) rather than the name of its file.
	DisplayDocTitle bool
	// FitWindow asks for the window to be sized to the first page,
	// CenterWindow for it to be centred on the screen.
	FitWindow, CenterWindow bool
	// HideToolbar, HideMenubar and HideWindowUI ask for the tool bars,
	// the menu bar and the scroll bars and navigation controls to be
	// hidden.
	HideToolbar, HideMenubar, HideWindowUI bool
	// Print is what the print dialogue starts with.
	Print PrintSettings
}

// PrintSettings is what a document asks its print dialogue to start with.
type PrintSettings struct {
	// Scaling is "AppDefault" (the viewer's scaling, the default) or
	// "None" (no scaling to the paper).
	Scaling string
	// Duplex is "Simplex", "DuplexFlipShortEdge" or "DuplexFlipLongEdge",
	// empty if the document does not say.
	Duplex string
	// PickTrayByPDFSize says that the paper tray is chosen by the size of
	// the page.
	PickTrayByPDFSize bool
	// Ranges are the pages to print, each a first and a last page,
	// 0-based and inclusive; nil for all pages.
	Ranges [][2]int
	// Copies is the number of copies, 1 by default.
	Copies int
}

// ViewSettings returns how the document asks to be opened and printed.
func (d *Document) ViewSettings() (v ViewSettings) {
	defaults := ViewSettings{PageMode: "UseNone", AfterFullScreen: "UseNone", PageLayout: "SinglePage",
		Print: PrintSettings{Scaling: "AppDefault", Copies: 1}}
	defer func() {
		if recover() != nil {
			v = defaults // a broken catalogue asks for nothing
		}
	}()
	v = defaults
	cat, err := d.r.Catalog()
	if err != nil {
		return v
	}
	// one returns the name o if it is one of names, def otherwise.
	one := func(o pdf.Object, def string, names ...string) string {
		n, _ := d.name(o)
		for _, s := range names {
			if string(n) == s {
				return s
			}
		}
		return def
	}
	v.Open = d.openAction(cat.Get("OpenAction"))
	v.PageMode = one(cat.Get("PageMode"), v.PageMode, "UseOutlines", "UseThumbs", "FullScreen", "UseOC", "UseAttachments")
	v.PageLayout = one(cat.Get("PageLayout"), v.PageLayout, "OneColumn", "TwoColumnLeft", "TwoColumnRight", "TwoPageLeft", "TwoPageRight")

	vp := d.dict(cat.Get("ViewerPreferences"))
	v.AfterFullScreen = one(vp.Get("NonFullScreenPageMode"), v.AfterFullScreen, "UseOutlines", "UseThumbs", "UseOC")
	v.RightToLeft = one(vp.Get("Direction"), "", "R2L") != ""
	v.DisplayDocTitle = d.boolean(vp.Get("DisplayDocTitle"))
	v.FitWindow, v.CenterWindow = d.boolean(vp.Get("FitWindow")), d.boolean(vp.Get("CenterWindow"))
	v.HideToolbar, v.HideMenubar, v.HideWindowUI = d.boolean(vp.Get("HideToolbar")), d.boolean(vp.Get("HideMenubar")), d.boolean(vp.Get("HideWindowUI"))

	p := &v.Print
	p.Scaling = one(vp.Get("PrintScaling"), p.Scaling, "None")
	p.Duplex = one(vp.Get("Duplex"), "", "Simplex", "DuplexFlipShortEdge", "DuplexFlipLongEdge")
	p.PickTrayByPDFSize = d.boolean(vp.Get("PickTrayByPDFSize"))
	if n, ok := d.integer(vp.Get("NumCopies")); ok && n > 1 {
		p.Copies = n
	}
	// Pairs of page numbers, the first page 1. A pair that names no page
	// is left out, one that ends past the last page ends there.
	pages := d.NumPages()
	a, _ := d.resolve(vp.Get("PrintPageRange")).Array()
	for i := 0; i+1 < len(a) && len(p.Ranges) < maxPrintRanges; i += 2 {
		first, ok1 := d.integer(a[i])
		last, ok2 := d.integer(a[i+1])
		if !ok1 || !ok2 || first < 1 || last < first || first > pages {
			continue
		}
		p.Ranges = append(p.Ranges, [2]int{first - 1, min(last, pages) - 1})
	}
	return v
}

// openAction reads /OpenAction: a destination, or an action of which only
// one that leads to a place in the document is returned.
func (d *Document) openAction(o pdf.Object) *LinkTarget {
	o = d.resolve(o)
	if o.Kind() != pdf.KindDict {
		t := d.destination(o) // nil for what is no destination
		if t == nil || t.Page < 0 && t.Named == "" {
			return nil
		}
		return t
	}
	left := maxActions
	t := d.action(o, &left)
	if !t.leads() || t.External || t.URI != "" {
		return nil
	}
	return t
}
