package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/cera"
	"github.com/timzifer/cera/internal/testpdf"
)

// textPDF writes a document of three pages with a line of text each, an
// outline, page labels and a form field on the first page.
func textPDF(t *testing.T, dir, name string) string {
	t.Helper()
	objs := map[int]string{
		1: "<</Type /Catalog /Pages 2 0 R /Outlines 20 0 R /PageLabels <</Nums [0 <</S /r>>]>>" +
			" /AcroForm <</Fields [30 0 R] /DA (/Helv 10 Tf 0 g) /DR <</Font <</Helv 10 0 R>>>>>>>>",
		2:  "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 300 200] /Resources <</Font <</F1 10 0 R>>>>>>",
		3:  "<</Type /Page /Parent 2 0 R /Contents 6 0 R /Annots [30 0 R]>>",
		4:  "<</Type /Page /Parent 2 0 R /Contents 7 0 R>>",
		5:  "<</Type /Page /Parent 2 0 R /Contents 8 0 R>>",
		10: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
		20: "<</Type /Outlines /First 21 0 R /Last 22 0 R /Count 2>>",
		21: "<</Title (First) /Parent 20 0 R /Next 22 0 R /Dest [3 0 R /Fit]>>",
		22: "<</Title (Third) /Parent 20 0 R /Prev 21 0 R /Dest [5 0 R /Fit]>>",
		30: "<</Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (old) /Rect [20 20 200 40] /P 3 0 R>>",
	}
	for i, s := range []string{"The quick brown fox", "jumps over the lazy dog", "and the Fox sleeps"} {
		objs[6+i] = testpdf.Stream("", []byte("BT /F1 14 Tf 20 150 Td ("+s+") Tj ET"))
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, testpdf.File{Objs: objs, Trailer: "/Root 1 0 R /Info 40 0 R"}.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// cera runs a subcommand and returns what it wrote and its exit code.
func runCera(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errs bytes.Buffer
	code, ok := runCommand(args[0], args[1:], &out, &errs)
	if !ok {
		t.Fatalf("no command %q", args[0])
	}
	return out.String(), errs.String(), code
}

func openFile(t *testing.T, name string) *cera.Document {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := cera.Open(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return doc
}

func TestCommands(t *testing.T) {
	dir := t.TempDir()
	in := textPDF(t, dir, "in.pdf")

	// text, plain and with places.
	out, _, code := runCera(t, "text", in)
	if code != 0 || !strings.Contains(out, "The quick brown fox") || strings.Count(out, "\f") != 2 || !strings.Contains(out, "lazy dog") {
		t.Errorf("text: %d %q", code, out)
	}
	out, _, _ = runCera(t, "text", "-pages", "2", in)
	if strings.TrimSpace(out) != "jumps over the lazy dog" {
		t.Errorf("text of page 2: %q", out)
	}
	out, _, code = runCera(t, "text", "-json", "-pages", "3,1", in)
	var js struct {
		Pages []struct {
			Page          int
			Width, Height float64
			Lines         []struct {
				Text string
				Quad [8]float64
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &js); err != nil || code != 0 || len(js.Pages) != 2 || js.Pages[0].Page != 3 ||
		js.Pages[1].Width != 300 || len(js.Pages[1].Lines) != 2 || js.Pages[1].Lines[0].Text != "The quick brown fox" || js.Pages[1].Lines[1].Text != "old" {
		t.Errorf("text -json: %v %+v", err, js)
	} else if q := js.Pages[1].Lines[0].Quad; q[0] < 19 || q[0] > 21 || q[1] < 30 || q[1] > 50 || q[2] <= q[0] {
		t.Errorf("the line is at %v", q)
	}

	// search.
	out, _, code = runCera(t, "search", in, "fox")
	// The context of the first goes on into what the field of the page says.
	if code != 0 || out != "1: The quick brown [fox] old\n3: and the [Fox] sleeps\n" {
		t.Errorf("search: %d %q", code, out)
	}
	out, _, _ = runCera(t, "search", "-match-case", in, "Fox")
	if out != "3: and the [Fox] sleeps\n" {
		t.Errorf("search -match-case: %q", out)
	}
	if _, errs, code := runCera(t, "search", in, "cat"); code != 1 || !strings.Contains(errs, "not in the text") {
		t.Errorf("search for what is not there: %d %q", code, errs)
	}

	// info.
	out, _, code = runCera(t, "info", in)
	for _, want := range []string{"pages: 3", "300 x 200 pt: 3 from page 1", "page labels: i, ii, iii", "outline: 2 entries", `field: name (text) = "old"`,
		"font: Helvetica (Type1), not embedded, drawn with "} {
		if !strings.Contains(out, want) {
			t.Errorf("info lacks %q:\n%s", want, out)
		}
	}
	out, _, _ = runCera(t, "info", "-json", in)
	var inf info
	if err := json.Unmarshal([]byte(out), &inf); err != nil || inf.Pages != 3 || len(inf.Fields) != 1 || inf.Outline != 2 || code != 0 ||
		len(inf.Fonts) == 0 || inf.Fonts[0].Name != "Helvetica" || inf.Fonts[0].Embedded || inf.Fonts[0].DrawnWith == "" {
		t.Errorf("info -json: %v %+v", err, inf)
	}

	// merge: the pages of both, the outline of each under its name.
	merged := filepath.Join(dir, "merged.pdf")
	if _, errs, code := runCera(t, "merge", "-o", merged, "-small", in, in); code != 0 {
		t.Fatalf("merge: %s", errs)
	}
	md := openFile(t, merged)
	if o := md.Outline(); md.NumPages() != 6 || len(o) != 2 || o[0].Title != "in" || len(o[1].Children) != 2 {
		t.Errorf("merged: %d pages, the outline %+v", md.NumPages(), o)
	}

	// split: a file a page, each with its text and its entry of the
	// outline.
	if _, errs, code := runCera(t, "split", "-o", filepath.Join(dir, "part-%d.pdf"), in); code != 0 {
		t.Fatalf("split: %s", errs)
	}
	for n, title := range []string{"First", "", "Third"} {
		pd := openFile(t, filepath.Join(dir, fmt.Sprintf("part-%d.pdf", n+1)))
		o := pd.Outline()
		if pd.NumPages() != 1 || (title == "") != (len(o) == 0) || title != "" && o[0].Title != title {
			t.Errorf("part %d: %d pages, the outline %+v", n+1, pd.NumPages(), o)
		}
	}
	if _, errs, code := runCera(t, "split", "-every", "2", "-pages", "1-3", "-o", filepath.Join(dir, "two-%d.pdf"), in); code != 0 {
		t.Fatalf("split -every: %s", errs)
	}
	if a, b := openFile(t, filepath.Join(dir, "two-1.pdf")), openFile(t, filepath.Join(dir, "two-2.pdf")); a.NumPages() != 2 || b.NumPages() != 1 {
		t.Errorf("split -every 2: %d and %d pages", a.NumPages(), b.NumPages())
	}

	// rotate and delete.
	turned := filepath.Join(dir, "turned.pdf")
	if _, errs, code := runCera(t, "rotate", "-by", "90", "-pages", "2", "-o", turned, in); code != 0 {
		t.Fatalf("rotate: %s", errs)
	}
	td := openFile(t, turned)
	p0, _ := td.Page(0)
	p1, _ := td.Page(1)
	if p0.Rotate != 0 || p1.Rotate != 90 {
		t.Errorf("turned: %d and %d", p0.Rotate, p1.Rotate)
	}
	less := filepath.Join(dir, "less.pdf")
	if _, errs, code := runCera(t, "delete", "-pages", "2-", "-o", less, in); code != 0 {
		t.Fatalf("delete: %s", errs)
	}
	if ld := openFile(t, less); ld.NumPages() != 1 || len(ld.Outline()) != 1 {
		t.Errorf("after delete: %d pages, %d entries", ld.NumPages(), len(ld.Outline()))
	}

	// fill: written, as an update, flattened.
	filled := filepath.Join(dir, "filled.pdf")
	if _, errs, code := runCera(t, "fill", "-field", "name=Ada", "-o", filled, in); code != 0 {
		t.Fatalf("fill: %s", errs)
	}
	if v := openFile(t, filled).Form().Field("name").Saved.Text(); v != "Ada" {
		t.Errorf("filled: %q", v)
	}
	if _, errs, code := runCera(t, "fill", "-field", "name=Bob", "-update", "-o", filled, in); code != 0 {
		t.Fatalf("fill -update: %s", errs)
	}
	src, _ := os.ReadFile(in)
	upd, _ := os.ReadFile(filled)
	if !bytes.HasPrefix(upd, src) || openFile(t, filled).Form().Field("name").Saved.Text() != "Bob" {
		t.Error("fill -update does not extend the file")
	}
	if _, errs, code := runCera(t, "fill", "-field", "name=Cy", "-flatten", "-o", filled, in); code != 0 {
		t.Fatalf("fill -flatten: %s", errs)
	}
	if fd := openFile(t, filled); fd.Form() != nil && len(fd.Form().Fields) != 0 {
		t.Errorf("flattened, the form has %d fields", len(fd.Form().Fields))
	}

	// What cannot be done says so and writes nothing.
	for name, args := range map[string][]string{
		"no such field":  {"fill", "-field", "x=1", "-o", filepath.Join(dir, "no.pdf"), in},
		"no such page":   {"delete", "-pages", "9", "-o", filepath.Join(dir, "no.pdf"), in},
		"no such file":   {"text", filepath.Join(dir, "none.pdf")},
		"bad rotation":   {"rotate", "-by", "45", "-o", filepath.Join(dir, "no.pdf"), in},
		"all pages gone": {"delete", "-pages", "1-", "-o", filepath.Join(dir, "no.pdf"), in},
	} {
		if _, errs, code := runCera(t, args...); code != 1 || errs == "" {
			t.Errorf("%s: exit %d, %q", name, code, errs)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "no.pdf")); err == nil {
		t.Error("a file was written for what failed")
	}
	for name, args := range map[string][]string{
		"no output":    {"merge", in},
		"no input":     {"text"},
		"no pages":     {"delete", "-o", less, in},
		"no %d":        {"split", "-o", less, in},
		"unknown flag": {"info", "-x", in},
	} {
		if _, _, code := runCera(t, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if _, ok := runCommand("in.pdf", nil, nil, nil); ok {
		t.Error("a file name is taken for a command")
	}
}

func TestParsePages(t *testing.T) {
	for spec, want := range map[string]string{
		"": "[0 1 2 3 4]", "2": "[1]", "1-3": "[0 1 2]", "4-": "[3 4]", "5,1-2": "[4 0 1]", " 2 , 3 ": "[1 2]",
	} {
		if got, err := parsePages(spec, 5); err != nil || fmt.Sprint(got) != want {
			t.Errorf("%q: %v %v, want %s", spec, got, err, want)
		}
	}
	for _, spec := range []string{"0", "6", "3-2", "a", "1-b", "-2", "1,,2"} {
		if got, err := parsePages(spec, 5); err == nil {
			t.Errorf("%q: %v", spec, got)
		}
	}
}

// The other formats are written, in colour and in grey.
func TestFormats(t *testing.T) {
	dir := t.TempDir()
	in := textPDF(t, dir, "in.pdf")
	for ext, magic := range map[string]string{"jpg": "\xff\xd8\xff", "tif": "II*\x00", "webp": "RIFF", "png": "\x89PNG"} {
		for _, gray := range []bool{false, true} {
			out := filepath.Join(dir, "p-%d."+ext)
			if err := run(in, cera.OpenOptions{}, 72, 2, out, false, time.Minute, false, 0, cera.AnnotsView, cera.BoxCrop, cera.ImageNearest, nil, "encode", 80, gray, false); err != nil {
				t.Fatalf("%s, grey %v: %v", ext, gray, err)
			}
			b, err := os.ReadFile(fmt.Sprintf(out, 2))
			if err != nil || !bytes.HasPrefix(b, []byte(magic)) || len(b) < 100 {
				t.Errorf("%s, grey %v: %d bytes, %v, starting %q", ext, gray, len(b), err, b[:min(len(b), 8)])
			}
		}
	}
	if err := run(in, cera.OpenOptions{}, 72, 1, filepath.Join(dir, "p.jpg"), true, time.Minute, false, 0, cera.AnnotsView, cera.BoxCrop, cera.ImageNearest, nil, "encode", 0, false, false); err == nil {
		t.Error("a transparent JPEG was written")
	}
	if err := run(in, cera.OpenOptions{}, 72, 1, filepath.Join(dir, "p.bmp"), false, time.Minute, false, 0, cera.AnnotsView, cera.BoxCrop, cera.ImageNearest, nil, "encode", 0, false, false); err == nil {
		t.Error("a format nobody writes was written")
	}
}

// -cmyk writes the inks into a TIFF with the profile they are for.
func TestCMYKTIFF(t *testing.T) {
	dir := t.TempDir()
	in := textPDF(t, dir, "in.pdf")
	icc, err := os.ReadFile(filepath.Join("..", "..", "internal", "cmyk", "testdata", "toy-lut16.icc"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "p.tif")
	if err := run(in, cera.OpenOptions{CMYKProfile: icc}, 72, 1, out, false, time.Minute, false, 0, cera.AnnotsView, cera.BoxCrop, cera.ImageNearest, nil, "encode", 0, false, true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// The IFD: PhotometricInterpretation 5 (separated), InkSet 1 (CMYK)
	// and the profile (34675).
	le := binary.LittleEndian
	ifd := int(le.Uint32(b[4:]))
	tags := map[uint16][]byte{}
	for i := range int(le.Uint16(b[ifd:])) {
		e := b[ifd+2+12*i:][:12]
		tags[le.Uint16(e)] = e
	}
	if e := tags[262]; e == nil || le.Uint16(e[8:]) != 5 {
		t.Error("not separated")
	}
	if e := tags[332]; e == nil || le.Uint16(e[8:]) != 1 {
		t.Error("no CMYK ink set")
	}
	if e := tags[34675]; e == nil || !bytes.Equal(b[le.Uint32(e[8:]):][:le.Uint32(e[4:])], icc) {
		t.Error("profile not embedded")
	}
	for _, c := range []struct {
		out         string
		transparent bool
		gray        bool
	}{{"p.png", false, false}, {"p.tif", true, false}, {"p.tif", false, true}} {
		if err := run(in, cera.OpenOptions{}, 72, 1, filepath.Join(dir, c.out), c.transparent, time.Minute, false, 0, cera.AnnotsView, cera.BoxCrop, cera.ImageNearest, nil, "encode", 0, c.gray, true); err == nil {
			t.Errorf("-cmyk %+v was written", c)
		}
	}
}
