package cera

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera/internal/pdf"
)

func openWithInfo(t *testing.T, info string, catalogExtra string, objs ...string) *Document {
	t.Helper()
	doc, err := Open(buildPDFTrailer(info, catalogExtra, []string{""}, "", objs...))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestMetadata(t *testing.T) {
	xmp := `<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`
	doc := openWithInfo(t, "/Info 100 0 R", "/Metadata 101 0 R",
		`<< /Title (Plain \(title\)) /Author <FEFF00C40064006100>
		   /Subject <EFBBBF5374C3A46474> /Keywords (a, b) /Creator (Writer) /Producer (cera test)
		   /CreationDate (D:20240229133005+01'30') /ModDate (D:2025)
		   /Trapped /True /Company (Acme) /Pages 3 /Empty () >>`,
		fmt.Sprintf("<< /Type /Metadata /Subtype /XML /Length %d >>\nstream\n%s\nendstream", len(xmp), xmp))
	m := doc.Metadata()
	want := Metadata{
		Title: "Plain (title)", Author: "Äda", Subject: "Städt", Keywords: "a, b", Creator: "Writer", Producer: "cera test",
		Created:  time.Date(2024, 2, 29, 13, 30, 5, 0, time.FixedZone("", 90*60)),
		Modified: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Trapped:  "True",
		Custom:   map[string]string{"Company": "Acme", "Empty": ""},
		XMP:      []byte(xmp),
	}
	if m.Title != want.Title || m.Author != want.Author || m.Subject != want.Subject || m.Keywords != want.Keywords ||
		m.Creator != want.Creator || m.Producer != want.Producer || m.Trapped != want.Trapped {
		t.Errorf("strings: %+v", m)
	}
	if !m.Created.Equal(want.Created) || !m.Modified.Equal(want.Modified) {
		t.Errorf("dates: %v, %v; want %v, %v", m.Created, m.Modified, want.Created, want.Modified)
	}
	if fmt.Sprint(m.Custom) != fmt.Sprint(want.Custom) {
		t.Errorf("custom: %v, want %v", m.Custom, want.Custom)
	}
	if string(m.XMP) != xmp {
		t.Errorf("XMP %q", m.XMP)
	}
}

// TestMetadataBroken checks that a missing, damaged or odd /Info gives
// empty fields and no panic.
func TestMetadataBroken(t *testing.T) {
	for _, info := range []string{"", "/Info 100 0 R", "/Info 5", "/Info (string)", "/Info 999 0 R"} {
		doc := openWithInfo(t, info, "", "[1 2 3]")
		if m := doc.Metadata(); m.Title != "" || m.Custom != nil || m.XMP != nil || !m.Created.IsZero() {
			t.Errorf("%q: %+v", info, m)
		}
	}
	// Custom entries are bounded, and long strings are cut on a character.
	var b strings.Builder
	b.WriteString("<< /Title <FEFF")
	for range maxInfoString/2 + 10 {
		b.WriteString("00E4") // ä: two bytes in UTF-8
	}
	b.WriteString(">")
	for i := range maxInfoEntries + 50 {
		fmt.Fprintf(&b, " /K%d (v)", i)
	}
	b.WriteString(" >>")
	m := openWithInfo(t, "/Info 100 0 R", "", b.String()).Metadata()
	if len(m.Custom) != maxInfoEntries {
		t.Errorf("%d custom entries kept", len(m.Custom))
	}
	if len(m.Title) > maxInfoString || !strings.HasPrefix(m.Title, "ää") || strings.ContainsRune(m.Title, '�') {
		t.Errorf("title of %d bytes, cut badly", len(m.Title))
	}
}

func TestParseDate(t *testing.T) {
	utc := time.UTC
	east := func(h, m int) *time.Location { return time.FixedZone("", h*3600+m*60) }
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"D:20240101120000Z", time.Date(2024, 1, 1, 12, 0, 0, 0, utc)},
		{"D:20240101120000Z00'00'", time.Date(2024, 1, 1, 12, 0, 0, 0, utc)},
		{"D:20240101120000+02'00'", time.Date(2024, 1, 1, 12, 0, 0, 0, east(2, 0))},
		{"D:20240101120000+0200", time.Date(2024, 1, 1, 12, 0, 0, 0, east(2, 0))},
		{"D:20240101120000-05'30", time.Date(2024, 1, 1, 12, 0, 0, 0, east(-5, -30))},
		{"20240101", time.Date(2024, 1, 1, 0, 0, 0, 0, utc)}, // no D:
		{"D:2024", time.Date(2024, 1, 1, 0, 0, 0, 0, utc)},
		{"D:202406", time.Date(2024, 6, 1, 0, 0, 0, 0, utc)},
		{"D:20241399", time.Date(2024, 1, 1, 0, 0, 0, 0, utc)},       // month 13: the rest is dropped
		{"D:20240231", time.Date(2024, 2, 1, 0, 0, 0, 0, utc)},       // no 31 February
		{"D:20240101256000", time.Date(2024, 1, 1, 0, 0, 0, 0, utc)}, // hour 25
		{"  D:19991231235959  ", time.Date(1999, 12, 31, 23, 59, 59, 0, utc)},
		{"", time.Time{}},
		{"D:", time.Time{}},
		{"D:99", time.Time{}},
		{"yesterday", time.Time{}},
	} {
		if got := parseDate(c.in); !got.Equal(c.want) {
			t.Errorf("parseDate(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTextString(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f", "˘ˇˆ˙˝˛˚˜"},
		{"\x80\x92\xa0", "•™€"},
		{"a\x7fb\xadc\x9fd", "a�b�c�d"},
		{"\xe9t\xe9", "été"},
		{"\xfe\xff\x00H\x00i", "Hi"},
		{"\xfe\xff\x00\x1b\x00e\x00n\x00\x1b\x00H\x00i", "Hi"},                     // a language escape
		{"\xfe\xff\x00\x1b\x00d\x00e\x00D\x00E\x00\x1b\x00H\x00i\x00\x1b", "Hi"},   // with a country, and an unpaired ESC
		{"\xef\xbb\xbf\x1ben\x1bHi", "Hi"},
	} {
		if got := textString(pdf.String([]byte(c.in))); got != c.want {
			t.Errorf("textString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
