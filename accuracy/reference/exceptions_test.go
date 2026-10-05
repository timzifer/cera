package main

import "testing"

func TestExcepted(t *testing.T) {
	for _, c := range []struct {
		rel    string
		page   int
		annots bool
		want   bool
	}{
		{"checkbox_no_appearance.pdf", 1, true, true},
		{"test/pdfs/checkbox_no_appearance.pdf", 1, true, true},
		{"checkbox_no_appearance.pdf", 1, false, false}, // page content only: nothing to judge
		{"checkbox_no_appearance.pdf", 2, true, false},
		{"xcheckbox_no_appearance.pdf", 1, true, false},
	} {
		if got := excepted(c.rel, c.page, c.annots) != ""; got != c.want {
			t.Errorf("excepted(%q, %d, %v) = %v, want %v", c.rel, c.page, c.annots, got, c.want)
		}
	}
	for _, e := range exceptions {
		if e.file == "" || e.reason == "" {
			t.Errorf("exception %+v: needs a file and a reason", e)
		}
	}
}
