package main

import (
	"github.com/timzifer/cera"
	"github.com/timzifer/cera/fonts/cjk"
	"github.com/timzifer/cera/fonts/cjk/cns1"
	"github.com/timzifer/cera/fonts/cjk/gb1"
	"github.com/timzifer/cera/fonts/cjk/japan1"
	"github.com/timzifer/cera/fonts/cjk/korea1"
)

// openOptions opens every document cera renders or measures.
var openOptions cera.OpenOptions

// simulateOverprint renders cera with RenderOptions.SimulateOverprint (-overprint).
var simulateOverprint bool

// cjkFonts are the fonts -cjk gives cera for CJK text a document does not
// embed. They are off by default: of the references, only Ghostscript
// draws such text (with the system's fonts); PDFium, MuPDF and Poppler
// leave it out or draw Latin stand-ins, so with these fonts cera is the
// outlier on those pages (pdf.js and borb pages with font-missing-*: 1.3 %
// of the inked boxes without, 6.5 % with). -cjk is for references that
// have CJK fonts.
var cjkFonts = cjk.Provider{japan1.Collection, gb1.Collection, cns1.Collection, korea1.Collection}
