package main

import "strings"

// exception is a page left out of the comparison because the references
// cannot judge it: cera draws it as intended and the references do not.
// Each entry names one file (and page) and says why; a whole class of
// pages is no exception but a mode of the tool (as -annots is).
type exception struct {
	file   string // ending of the path, at a path element
	page   int    // 1-based; 0 is every page
	annots bool   // applies only when annotations are drawn
	reason string
}

var exceptions = []exception{
	{
		file: "checkbox_no_appearance.pdf", page: 1, annots: true,
		reason: "check boxes without /AP under /NeedAppearances: cera generates the check mark of the checked box (ADR 0006, section 5); " +
			"PDFium, MuPDF, Poppler and Ghostscript generate no appearance and draw nothing",
	},
}

// excepted returns why page (1-based) of the file at rel is left out,
// or "" when it is compared.
func excepted(rel string, page int, annots bool) string {
	for _, e := range exceptions {
		if e.annots && !annots || e.page != 0 && e.page != page {
			continue
		}
		if rel == e.file || strings.HasSuffix(rel, "/"+e.file) {
			return e.reason
		}
	}
	return ""
}
