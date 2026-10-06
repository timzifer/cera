package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// reasons are the differences ADR 0012 expects between the readers; an
// allowlist entry must give one of them.
var reasons = map[string]string{
	"hybrid-xref":        "a free table entry no longer hides a compressed /XRefStm entry",
	"adler-ignored":      "Flate output is kept when only the Adler-32 checksum is wrong",
	"stream-cap":         "decoded output is capped at MaxStreamBytes",
	"nesting-limit":      "objects nested deeper than MaxNesting are cut",
	"predictor-limit":    "predictor rows above MaxPredictorRow are refused",
	"tiff-subbyte":       "the TIFF predictor is applied at 1, 2 and 4 bits per component",
	"macroman-encoding":  "MacRomanEncoding is its own table, not StandardEncoding",
	"macexpert-encoding": "MacExpertEncoding is its own table, not StandardEncoding",
	"saslprep":           "revision 5 and 6 passwords are prepared with SASLprep (RFC 4013)",
	"encrypt-unreadable": "a file whose /Encrypt dictionary does not parse is read unencrypted, as PDFium does",
	"brotli":             "/BrotliDecode (RFC 7932) is decoded, as pdf.js does",
	"literal-eol":        "a literal string keeps its raw end-of-line bytes, as pdf.js, MuPDF and PDFium do",
}

// An allowlist names differences that are expected, one per line:
//
//	<file glob> <key prefix> <reason>
//
// The glob matches the file's name or its slash-separated path, the prefix the
// snapshot key (for example "decoded/12 0" or "obj/"; "*" for every key).
// Blank lines and lines starting with # are ignored.
type allowlist []allowEntry

type allowEntry struct{ glob, prefix, reason string }

func readAllowlist(p string) (allowlist, error) {
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var l allowlist
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) != 3 {
			return nil, fmt.Errorf("%s:%d: want <glob> <key prefix> <reason>", p, n)
		}
		if _, ok := reasons[fs[2]]; !ok {
			return nil, fmt.Errorf("%s:%d: unknown reason %q", p, n, fs[2])
		}
		if _, err := path.Match(fs[0], ""); err != nil {
			return nil, fmt.Errorf("%s:%d: %v", p, n, err)
		}
		l = append(l, allowEntry{fs[0], fs[1], fs[2]})
	}
	return l, sc.Err()
}

// reason returns why the difference at key in file is allowed, or "".
func (l allowlist) reason(file, key string) string {
	file = filepath.ToSlash(file)
	for _, e := range l {
		okName, _ := path.Match(e.glob, path.Base(file))
		okPath, _ := path.Match(e.glob, file)
		if (okName || okPath) && (e.prefix == "*" || strings.HasPrefix(key, e.prefix)) {
			return e.reason
		}
	}
	return ""
}
