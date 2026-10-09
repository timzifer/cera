package cera_test

import (
	"log"
	"os"

	"github.com/timzifer/cera"
)

// A report with a cover page from another file, its last page deleted;
// its outline, form and the rest kept.
func ExampleDocument_Edit() {
	report, err := os.ReadFile("report.pdf")
	if err != nil {
		log.Fatal(err)
	}
	cover, err := os.ReadFile("cover.pdf")
	if err != nil {
		log.Fatal(err)
	}
	doc, err := cera.Open(report)
	if err != nil {
		log.Fatal(err)
	}
	src, err := cera.Open(cover)
	if err != nil {
		log.Fatal(err)
	}
	e := doc.Edit()
	if err := e.DeletePages(e.NumPages() - 1); err != nil {
		log.Fatal(err)
	}
	if err := e.ImportPages(0, src, cera.EditPage{Index: 0}); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create("out.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := e.Save(f); err != nil {
		log.Fatal(err)
	}
}

// Pages 3 to 5 and page 1 of a file in a new one, the last turned to
// landscape.
func ExampleNewEditor() {
	data, err := os.ReadFile("in.pdf")
	if err != nil {
		log.Fatal(err)
	}
	doc, err := cera.Open(data)
	if err != nil {
		log.Fatal(err)
	}
	e := cera.NewEditor()
	err = e.ImportPages(0, doc,
		cera.EditPage{Index: 2}, cera.EditPage{Index: 3}, cera.EditPage{Index: 4},
		cera.EditPage{Index: 0, Rotate: 90})
	if err != nil {
		log.Fatal(err)
	}
	f, err := os.Create("out.pdf")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := e.Save(f); err != nil {
		log.Fatal(err)
	}
}
