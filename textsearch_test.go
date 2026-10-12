package cera

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestFind(t *testing.T) {
	text := layoutPage(t, selectPage, TextOptions{}, "")
	str := func(ms []TextMatch) string {
		s := ""
		for _, m := range ms {
			s += fmt.Sprintf("%v %q;", m.Range, text.TextOf(m.Range, TextFormat{JoinHyphenated: true}))
		}
		return s
	}
	for _, c := range []struct {
		query string
		opt   SearchOptions
		want  string
	}{
		{"wonderful", SearchOptions{}, `{7 17} "wonderful";`}, // across the line, the hyphen with it
		{"WONDER", SearchOptions{}, `{7 13} "wonder";`},
		{"WONDER", SearchOptions{MatchCase: true}, ``},
		{"wonder", SearchOptions{WholeWord: true}, ``},
		{"world", SearchOptions{WholeWord: true}, `{18 23} "world";`},
		{"  world.\n other ", SearchOptions{}, `{18 29} "world.\n\nOther";`}, // across blocks
		{"o", SearchOptions{}, `{4 5} "o";{8 9} "o";{19 20} "o";{24 25} "O";{32 33} "o";`},
		{"", SearchOptions{}, ``},
		{"zebra", SearchOptions{}, ``},
	} {
		if got := str(text.Find(c.query, c.opt)); got != c.want {
			t.Errorf("Find(%q, %+v) = %s, want %s", c.query, c.opt, got, c.want)
		}
	}
	ms := text.Find("wonderful", SearchOptions{Context: 7})
	if len(ms) != 1 || ms[0].Quote != (Quote{"Hello, ", "wonderful", " world."}) {
		t.Errorf("quote %+v", ms)
	}
	ms = text.Find("world", SearchOptions{Context: -1})
	if len(ms) != 1 || ms[0].Quote != (Quote{Exact: "world"}) {
		t.Errorf("no context %+v", ms)
	}
}

// Two places in the text of one /ActualText are one place on the page:
// its glyphs are found whole (#138).
func TestFindInActualText(t *testing.T) {
	var chars []TextChar
	for i, s := range []string{"o", "ffe", "", "", "r"} {
		x := float64(10 + 6*i)
		q := Quad{[4][2]float64{{x, 10}, {x + 6, 10}, {x, 22}, {x + 6, 22}}}
		c := TextChar{Text: s, Origin: [2]float64{x, 20}, Size: 12, Advance: 6, Dir: [2]float64{1, 0}, Quad: q, Box: q.Bounds(), MCID: -1}
		if i >= 1 && i <= 3 {
			c.group = 1
		}
		chars = append(chars, c)
	}
	text := layoutText(chars, nil, false)
	for _, c := range []struct {
		query string
		want  []TextRange
	}{
		{"f", []TextRange{{1, 4}}},
		{"ffe", []TextRange{{1, 4}}},
		{"of", []TextRange{{0, 4}}},
		{"er", []TextRange{{1, 5}}},
		{"r", []TextRange{{4, 5}}},
	} {
		var got []TextRange
		for _, m := range text.Find(c.query, SearchOptions{}) {
			got = append(got, m.Range)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("Find(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for _, c := range []struct {
		s           string
		fold, strip bool
		want        string
	}{
		{"Ré\u00adsu-\tmé²  X", false, false, "Résu- mé² X"},
		{"Ré\u00adsu-\tmé²  X", true, false, "Résu- mé² X"},
		{"Ré\u00adsu-\tmé²  X", true, true, "Resu- me2 X"},
		{"ΣΑΣ σας", true, false, "ΣΑΣ ΣΑΣ"},
	} {
		n := normalize(c.s, c.fold, c.strip)
		want := c.want
		if c.fold {
			want = normalize(c.want, true, false).s // folded to upper case
		}
		if n.s != want || len(n.lo) != len(n.s) || len(n.hi) != len(n.s) {
			t.Errorf("normalize(%q, %v, %v) = %q, want %q", c.s, c.fold, c.strip, n.s, want)
		}
	}
	// Each byte maps back to the rune it comes from.
	n := normalize("aé b", true, true)
	if n.s != "AE B" || n.lo[1] != 1 || n.hi[1] != 3 || n.lo[3] != 4 {
		t.Errorf("%q %v %v", n.s, n.lo, n.hi)
	}
	n = normalize("résumé, Resume", true, true)
	if ms := n.find(n.query("resume"), true); len(ms) != 2 {
		t.Errorf("without diacritics %v", ms)
	}
}

func TestFindQuote(t *testing.T) {
	text := layoutPage(t, "BT /F1 10 Tf 20 360 Td (the cat. the dog. the cat sat.) Tj ET", TextOptions{}, "")
	for _, c := range []struct {
		q    Quote
		want TextRange
	}{
		{Quote{Exact: "the cat"}, TextRange{0, 7}},
		{Quote{Prefix: "dog. ", Exact: "the cat", Suffix: " sat"}, TextRange{18, 25}},
		{Quote{Prefix: "DOG.", Exact: "THE  CAT"}, TextRange{18, 25}},
		{Quote{Exact: "the", Suffix: " dog"}, TextRange{9, 12}},
	} {
		m, ok := text.FindQuote(c.q, SearchOptions{})
		if !ok || m.Range != c.want {
			t.Errorf("FindQuote(%+v) = %v %v, want %v", c.q, m.Range, ok, c.want)
		}
	}
	if _, ok := text.FindQuote(Quote{Exact: "the bird"}, SearchOptions{}); ok {
		t.Error("found a quote not in the text")
	}
	// A quote taken from a range finds that range again.
	for _, r := range []TextRange{{18, 25}, {0, 3}, {26, 29}} {
		q := text.QuoteOf(r, 8)
		if m, ok := text.FindQuote(q, SearchOptions{MatchCase: true}); !ok || m.Range != r {
			t.Errorf("QuoteOf(%v) = %+v, found at %v", r, q, m.Range)
		}
	}
	if q := text.QuoteOf(TextRange{4, 4}, 0); q != (Quote{}) {
		t.Errorf("quote of nothing %+v", q)
	}
}

// indexDoc is a document of three pages.
func indexDoc(t *testing.T) *Document {
	t.Helper()
	page := func(s string) string { return "BT /F1 10 Tf 10 50 Td (" + s + ") Tj ET" }
	doc, err := Open(buildPDF([]string{page("alpha beta"), page("beta gamma"), page("gamma beta")}, "/Resources << /Font << /F1 100 0 R >> >>", helvetica))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestTextIndexFind(t *testing.T) {
	x := NewTextIndex(indexDoc(t), TextOptions{}, 2)
	ctx := context.Background()
	find := func(from DocPos, backward bool) string {
		s := ""
		for m, err := range x.Find(ctx, "beta", SearchOptions{}, from, backward) {
			if err != nil {
				t.Fatal(err)
			}
			s += fmt.Sprintf("%d:%d ", m.Page, m.Range.Start)
		}
		return s
	}
	for _, c := range []struct {
		from     DocPos
		backward bool
		want     string
	}{
		{DocPos{1, 0}, false, "1:0 2:6 0:6 "},
		{DocPos{1, 1}, false, "2:6 0:6 1:0 "},
		{DocPos{1, 0}, true, "0:6 2:6 1:0 "},
		{DocPos{1, 4}, true, "1:0 0:6 2:6 "},
		{DocPos{9, 0}, false, "2:6 0:6 1:0 "},
	} {
		if got := find(c.from, c.backward); got != c.want {
			t.Errorf("Find from %v backward %v: %s, want %s", c.from, c.backward, got, c.want)
		}
	}
	// Stopping early.
	for m := range x.Find(ctx, "gamma", SearchOptions{}, DocPos{}, false) {
		if m.Page != 1 || m.Quote.Exact != "gamma" || m.Quote.Prefix != "beta " {
			t.Errorf("first gamma %+v", m)
		}
		break
	}
	if len(x.pages) != 2 || x.lru.Len() != 2 {
		t.Errorf("%d pages kept", len(x.pages))
	}

	done, cancel := context.WithCancel(ctx)
	cancel()
	n := 0
	for _, err := range x.Find(done, "beta", SearchOptions{}, DocPos{}, false) {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled: %v", err)
		}
		n++
	}
	if n != 1 {
		t.Errorf("%d results after cancel", n)
	}
}

func TestTextIndexText(t *testing.T) {
	x := NewTextIndex(indexDoc(t), TextOptions{}, 0)
	ctx := context.Background()
	s, err := x.TextOf(ctx, DocPos{2, 5}, DocPos{0, 6}, TextFormat{})
	if err != nil || s != "beta\n\nbeta gamma\n\ngamma" {
		t.Errorf("TextOf %q %v", s, err)
	}
	// The page nearest to near with the quote wins, although page 0 has
	// the prefix.
	m, ok, err := x.FindQuote(ctx, Quote{Prefix: "alpha ", Exact: "beta"}, SearchOptions{}, 2)
	if err != nil || !ok || m.Page != 2 || m.Range.Start != 6 {
		t.Errorf("FindQuote %+v %v %v", m, ok, err)
	}
	if m, ok, _ := x.FindQuote(ctx, Quote{Exact: "alpha"}, SearchOptions{}, 2); !ok || m.Page != 0 {
		t.Errorf("FindQuote far %+v", m)
	}
	if _, ok, _ := x.FindQuote(ctx, Quote{Exact: "delta"}, SearchOptions{}, 1); ok {
		t.Error("found delta")
	}
	// Callers of one page share its text.
	got := make(chan *PageText)
	for range 4 {
		go func() {
			p, _ := x.Page(ctx, 2)
			got <- p
		}()
	}
	first := <-got
	for range 3 {
		if p := <-got; p != first || p == nil {
			t.Error("pages differ")
		}
	}
	if _, err := x.Page(ctx, 7); err == nil {
		t.Error("page out of range")
	}
}
