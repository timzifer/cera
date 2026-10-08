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
