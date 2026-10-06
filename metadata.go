package cera

import (
	"strings"
	"time"

	"github.com/timzifer/cera/internal/pdf"
)

// Metadata is what a document says about itself: its document
// information dictionary (/Info), decoded, and its XMP metadata stream.
type Metadata struct {
	Title, Author, Subject, Keywords, Creator, Producer string

	// Created and Modified are zero when absent or not a date. A date
	// without a time zone is taken as UTC.
	Created, Modified time.Time

	// Trapped is "True", "False", "Unknown" or "".
	Trapped string

	// Custom holds the other entries of /Info that are strings, by key.
	Custom map[string]string

	// XMP is the catalog's /Metadata stream, decoded but not parsed; nil if
	// there is none. PDF 2.0 deprecates /Info, but for its dates, in favour
	// of XMP.
	XMP []byte
}

// Metadata returns the document's metadata. A missing or damaged /Info
// gives empty fields, never an error.
func (d *Document) Metadata() Metadata {
	var m Metadata
	if info := d.dict(d.r.Trailer().Get("Info")); !info.IsZero() {
		n := 0
		for k, v := range info.All() {
			v = d.resolve(v)
			switch k {
			case "Title":
				m.Title = infoString(v)
			case "Author":
				m.Author = infoString(v)
			case "Subject":
				m.Subject = infoString(v)
			case "Keywords":
				m.Keywords = infoString(v)
			case "Creator":
				m.Creator = infoString(v)
			case "Producer":
				m.Producer = infoString(v)
			case "CreationDate":
				m.Created = parseDate(infoString(v))
			case "ModDate":
				m.Modified = parseDate(infoString(v))
			case "Trapped":
				m.Trapped = trapped(v)
			default:
				if _, ok := v.Str(); !ok || n >= maxInfoEntries {
					continue
				}
				if m.Custom == nil {
					m.Custom = map[string]string{}
				}
				m.Custom[string(k)] = infoString(v)
				n++
			}
		}
	}
	if cat, err := d.r.Catalog(); err == nil {
		if s, ok := d.resolve(cat.Get("Metadata")).Stream(); ok {
			if data := d.r.Decode(s).Data; len(data) <= maxXMPBytes {
				m.XMP = append([]byte(nil), data...)
			}
		}
	}
	return m
}

// infoString decodes a text string of /Info, cut at maxInfoString bytes.
func infoString(o pdf.Object) string {
	s := textString(o)
	if len(s) > maxInfoString {
		s = strings.ToValidUTF8(s[:maxInfoString], "")
	}
	return s
}

// trapped reads /Trapped: a name (PDF 1.3 and later), or a boolean or
// string as some producers write it.
func trapped(o pdf.Object) string {
	if n, ok := o.Name(); ok {
		switch n {
		case "True", "False", "Unknown":
			return string(n)
		}
		return ""
	}
	if b, ok := o.Bool(); ok {
		if b {
			return "True"
		}
		return "False"
	}
	switch s := strings.TrimSpace(textString(o)); strings.ToLower(s) {
	case "true":
		return "True"
	case "false":
		return "False"
	case "unknown":
		return "Unknown"
	}
	return ""
}

// parseDate reads a PDF date, D:YYYYMMDDHHmmSSOHH'mm (PDF 2.0, 7.9.4),
// leniently: everything after the year is optional, the D: prefix may be
// missing, and fields that are not digits or out of range end the date
// there, keeping what was read before. An offset may be Z, +HH'mm or
// -HH'mm, with or without apostrophes; Z followed by digits (Z00'00') is
// UTC. Without an offset the date is taken as UTC. A string without a
// year gives the zero time.
func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "D:")
	num := func(n int) (int, bool) {
		if len(s) < n {
			return 0, false
		}
		v := 0
		for _, c := range s[:n] {
			if c < '0' || c > '9' {
				return 0, false
			}
			v = v*10 + int(c-'0')
		}
		s = s[n:]
		return v, true
	}
	year, ok := num(4)
	if !ok {
		return time.Time{}
	}
	f := [5]int{1, 1, 0, 0, 0} // month, day, hour, minute, second
	limits := [5][2]int{{1, 12}, {1, 31}, {0, 23}, {0, 59}, {0, 59}}
	for i := range f {
		save := s
		v, ok := num(2)
		if !ok || v < limits[i][0] || v > limits[i][1] {
			s = save
			break
		}
		f[i] = v
	}
	loc := time.UTC
	if len(s) > 0 {
		switch sign := s[0]; sign {
		case '+', '-':
			s = s[1:]
			hh, ok := num(2)
			if ok && hh <= 23 {
				s = strings.TrimPrefix(s, "'")
				mm, ok := num(2)
				if !ok || mm > 59 {
					mm = 0
				}
				off := hh*3600 + mm*60
				if sign == '-' {
					off = -off
				}
				if off != 0 {
					loc = time.FixedZone("", off)
				}
			}
		}
	}
	t := time.Date(year, time.Month(f[0]), f[1], f[2], f[3], f[4], 0, loc)
	if t.Day() != f[1] {
		// 31 February and the like: the day does not exist in that month.
		t = time.Date(year, time.Month(f[0]), 1, f[2], f[3], f[4], 0, loc)
	}
	return t
}
