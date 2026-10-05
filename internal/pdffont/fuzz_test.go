package pdffont

import "testing"

// FuzzToUnicode reads arbitrary ToUnicode maps: they are programs in the
// file, and a small one may claim a huge range.
func FuzzToUnicode(f *testing.F) {
	f.Add([]byte("1 beginbfchar <41> <0058> endbfchar 1 beginbfrange <0000> <FFFF> <0041> endbfrange"))
	f.Add([]byte("beginbfrange <00> <05> [<0041> <D83DDE00>] endbfrange"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if m := ReadToUnicode(b); len(m) > maxToUnicodeEntries {
			t.Fatalf("%d entries", len(m))
		}
	})
}
