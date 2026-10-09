package pdfedit_test

import (
	"bytes"
	"log"
	"os"

	"github.com/timzifer/cera/pdfedit"
)

// Pages 3 to 5 and page 1 of a file, the last turned to landscape.
func ExampleExtract() {
	src, err := os.ReadFile("in.pdf")
	if err != nil {
		log.Fatal(err)
	}
	var out bytes.Buffer
	err = pdfedit.Extract(&out, src, []pdfedit.Page{
		{Index: 2}, {Index: 3}, {Index: 4},
		{Index: 0, Rotate: 90},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("out.pdf", out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

// A file with a page of another file appended, its outline, form and the
// rest kept.
func ExampleDocument_ImportPages() {
	data, err := os.ReadFile("report.pdf")
	if err != nil {
		log.Fatal(err)
	}
	cover, err := os.ReadFile("cover.pdf")
	if err != nil {
		log.Fatal(err)
	}
	doc, err := pdfedit.Open(data)
	if err != nil {
		log.Fatal(err)
	}
	src, err := pdfedit.OpenSource(cover)
	if err != nil {
		log.Fatal(err)
	}
	if err := doc.ImportPages(src, []pdfedit.Page{{Index: 0}}); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create("out.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := doc.Save(f, pdfedit.SaveOptions{}); err != nil {
		log.Fatal(err)
	}
}
