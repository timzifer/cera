// Package cjk supplies fonts for Chinese, Japanese and Korean text that a
// PDF names but does not embed — composite fonts of the Adobe character
// collections Japan1, GB1, CNS1 and Korea1 — to cera through its
// FontProvider:
//
//	doc, err := cera.OpenWith(data, cera.OpenOptions{
//		Fonts: cjk.Provider{japan1.Collection, gb1.Collection},
//	})
//
// The fonts are Noto Sans JP, SC and KR (SIL Open Font License), from
// github.com/go-opentype/fonts. Each collection lives in a package of its
// own (japan1, gb1, cns1, korea1), so a binary links only the fonts it
// imports; they are large (10 to 18 MB each). This module is separate from
// cera's so that no binary gets them without asking.
package cjk

import (
	"strings"

	"github.com/timzifer/cera"
)

// Collection is a font for one character collection.
type Collection struct {
	// Ordering is the collection: "Japan1", "GB1", "CNS1" or "Korea1".
	Ordering string
	// Program is a font program keyed by Unicode (TrueType or OpenType).
	Program []byte
}

// Provider supplies its collections for composite fonts. A font of a
// collection the provider has gets that collection's font; one that names
// no CJK collection (Adobe-Identity, mostly) gets the collection its name
// suggests ("MS-Mincho": Japan1, "SimSun": GB1, "Batang": Korea1), or else
// the first one. Simple fonts get nothing.
type Provider []Collection

// Font implements cera.FontProvider.
func (p Provider) Font(req cera.FontRequest) ([]byte, bool) {
	if req.Ordering == "" || len(p) == 0 {
		return nil, false
	}
	want := collection(req.Ordering)
	if want == "" {
		want = guess(req.Name)
	}
	if want != "" {
		for _, c := range p {
			if strings.EqualFold(c.Ordering, want) {
				return c.Program, true
			}
		}
		// Traditional and simplified Chinese share most characters.
		for _, c := range p {
			if (want == "CNS1" && c.Ordering == "GB1") || (want == "GB1" && c.Ordering == "CNS1") {
				return c.Program, true
			}
		}
		if collection(req.Ordering) != "" {
			return nil, false
		}
	}
	return p[0].Program, true
}

func collection(ordering string) string {
	switch strings.ToLower(ordering) {
	case "japan1", "japan2":
		return "Japan1"
	case "gb1":
		return "GB1"
	case "cns1", "cns2":
		return "CNS1"
	case "korea1", "kr":
		return "Korea1"
	}
	return ""
}

// guesses map words in font names to collections, checked in order.
var guesses = []struct{ word, ordering string }{
	{"mincho", "Japan1"}, {"gothic", "Japan1"}, {"meiryo", "Japan1"},
	{"heisei", "Japan1"}, {"kozmin", "Japan1"}, {"kozgo", "Japan1"},
	{"ryumin", "Japan1"}, {"yugoth", "Japan1"}, {"yumin", "Japan1"},
	{"hiragino", "Japan1"}, {"osaka", "Japan1"}, {"msgothic", "Japan1"},
	{"batang", "Korea1"}, {"gulim", "Korea1"}, {"dotum", "Korea1"},
	{"gungsuh", "Korea1"}, {"myeongjo", "Korea1"}, {"malgun", "Korea1"},
	{"hygothic", "Korea1"}, {"hysmyeongjo", "Korea1"}, {"nanum", "Korea1"},
	{"mingliu", "CNS1"}, {"pmingliu", "CNS1"}, {"msung", "CNS1"},
	{"mhei", "CNS1"}, {"dfkai", "CNS1"}, {"jhenghei", "CNS1"},
	{"simsun", "GB1"}, {"simhei", "GB1"}, {"simkai", "GB1"},
	{"fangsong", "GB1"}, {"stsong", "GB1"}, {"stheiti", "GB1"},
	{"stkaiti", "GB1"}, {"yahei", "GB1"}, {"nsimsun", "GB1"}, {"kaiti", "GB1"},
}

func guess(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		if c >= 'a' && c <= 'z' {
			b.WriteRune(c)
		}
	}
	k := b.String()
	for _, g := range guesses {
		if strings.Contains(k, g.word) {
			return g.ordering
		}
	}
	return ""
}
