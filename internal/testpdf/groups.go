package testpdf

import (
	"fmt"
	"strings"
)

// Page returns a PDF of one 200 × 100 page drawing content with the page
// resources res (the inside of the dictionary); objs are numbered from 100.
func Page(content, res string, objs ...string) []byte {
	f := File{Objs: map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R /Resources << " + res + " >> >>",
		4: Stream("", []byte(content)),
	}, Trailer: "/Root 1 0 R"}
	for i, o := range objs {
		f.Objs[100+i] = o
	}
	return f.Bytes()
}

// Form returns a form XObject with a 200 × 100 BBox, the dictionary
// entries extra and content.
func Form(extra, content string) string {
	return fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 200 100] %s /Length %d >>\nstream\n%s\nendstream",
		extra, len(content), content)
}

// Groups returns pages with every kind of transparency group and soft
// mask, by name.
func Groups() map[string][]byte {
	multiply := "/Resources << /ExtGState << /M << /BM /Multiply >> >> >>"
	var c strings.Builder
	c.WriteString("0.9 0.6 0.1 rg 0 0 200 100 re f\n")
	c.WriteString("q /M gs 0.2 0.3 0.9 rg 20 10 m 180 30 l 100 95 l f Q\n")
	c.WriteString("q 30 0 m 170 0 l 100 100 l h W n /A gs /K Do Q\n")
	c.WriteString("q /S gs 0 0.8 0.2 rg 10 w 0 50 m 200 60 l S Q\n")
	c.WriteString("q /D gs 0 1 1 rg 70 20 60 60 re f Q\n")
	return map[string][]byte{
		// Blend modes, a knockout group in a clip at 60%, a luminosity
		// soft mask.
		"mixed": Page(c.String(),
			"/ExtGState << /M << /BM /Multiply >> /A << /ca 0.6 >> /D << /BM /Difference >>"+
				" /S << /SMask << /S /Luminosity /G 101 0 R >> >> >> /XObject << /K 100 0 R >>",
			Form("/Group << /S /Transparency /K true /I true >> /Resources << /ExtGState << /H << /ca 0.5 >> >> >>",
				"/H gs 1 0 0 rg 10 10 120 60 re f 0 0 1 rg 60 30 120 60 re f"),
			Form("/Group << /S /Transparency >>", "1 g 0 0 m 200 0 l 200 100 l h f")),
		// An isolated group with a blend mode inside, nested in a clip in
		// an isolated group composited at 70% with Screen.
		"isolated-nested": Page("1 1 0 rg 0 0 120 100 re f /A gs /G Do",
			"/ExtGState << /A << /ca 0.7 /BM /Screen >> >> /XObject << /G 100 0 R >>",
			Form("/Group << /S /Transparency /I true >> /Resources << /XObject << /F 101 0 R >> >>",
				"0 1 0 rg 30 30 100 50 re f 15 10 m 160 20 l 90 90 l h W n /F Do"),
			Form("/Group << /S /Transparency /I true >> "+multiply,
				"/M gs 0 0 1 rg 50 0 100 100 re f 1 0 0 rg 80 40 100 40 re f")),
		// A non-isolated group with Multiply inside, at 50%, in a clip.
		"non-isolated": Page("1 0 0 rg 0 0 100 100 re f /A gs 0 0 m 200 0 l 100 100 l h W n /F Do",
			"/ExtGState << /A << /ca 0.5 >> >> /XObject << /F 100 0 R >>",
			Form("/Group << /S /Transparency >> "+multiply,
				"/M gs 0 0 1 rg 50 0 100 100 re f 0 1 0 rg 160 0 30 100 re f")),
		// The same composited with Screen: the group is drawn alone
		// first, and the page blends.
		"non-isolated-blended": Page("1 1 0 rg 0 0 100 100 re f /S gs /F Do",
			"/ExtGState << /S << /BM /Screen >> >> /XObject << /F 100 0 R >>",
			Form("/Group << /S /Transparency >> "+multiply, "/M gs 0 0 1 rg 50 0 100 100 re f")),
		// A knockout group that is not isolated.
		"knockout-non-isolated": Page("0 0 1 rg 0 0 70 100 re f 0 1 1 rg 70 0 130 100 re f /F Do",
			"/XObject << /F 100 0 R >>",
			Form("/Group << /S /Transparency /K true >> /Resources << /ExtGState << /H << /ca 0.5 /BM /Multiply >> >> >>",
				"/H gs 1 0 0 rg 10 10 120 60 re f 1 1 0 rg 60 30 120 60 re f")),
		// An alpha soft mask on an isolated group with a blend mode.
		"masked-group": Page("0.2 0.4 0.8 rg 0 0 200 100 re f /S gs /F Do",
			"/ExtGState << /S << /SMask << /S /Alpha /G 101 0 R >> /BM /Overlay >> >> /XObject << /F 100 0 R >>",
			Form("/Group << /S /Transparency /I true >>", "1 0.5 0 rg 20 10 160 80 re f"),
			Form("/Group << /S /Transparency >> /Resources << /ExtGState << /H << /ca 0.4 >> >> >>",
				"0 g 0 0 100 100 re f /H gs 100 0 100 100 re f")),
		// Text, a gradient and an image inside an isolated group at 80%.
		"paints": Page("q /A gs /F Do Q",
			"/ExtGState << /A << /ca 0.8 >> >> /XObject << /F 100 0 R >>",
			Form("/Group << /S /Transparency /I true >> /Resources << /Font << /H 101 0 R >> /Shading << /G 102 0 R >> /XObject << /I 103 0 R >> >>",
				"q 0 0 120 100 re W n /G sh Q q 60 0 0 40 120 30 cm /I Do Q BT /H 24 Tf 0 0 0 rg 10 40 Td (Surface) Tj ET"),
			"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			"<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 120 100] /Function << /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >> >>",
			Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceRGB /BitsPerComponent 8",
				[]byte("\xff\x00\x00\x00\xff\x00\x00\x00\xff\xff\xff\x00"))),
	}
}
