module github.com/timzifer/cera/form/fyneform

go 1.26.4

require (
	fyne.io/fyne/v2 v2.8.1
	github.com/timzifer/cera v0.0.0
)

require (
	github.com/ajroetker/go-highway v0.0.12 // indirect
	github.com/anthonynsimon/bild v0.14.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.2.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/fyne-io/gl-js v0.2.1-0.20260315212741-029c47fd27e8 // indirect
	github.com/fyne-io/oksvg v0.2.0 // indirect
	github.com/go-gl/gl v0.0.0-20260331235117-4566fea9a276 // indirect
	github.com/go-images/jpeg v0.2.0 // indirect
	github.com/go-images/jpeg2000 v0.13.2 // indirect
	github.com/go-opentype/fonts v0.10.0 // indirect
	github.com/go-opentype/opentype v0.13.0 // indirect
	github.com/go-pdfkit/pdffont v0.3.1 // indirect
	github.com/go-pdfkit/reader v0.6.0 // indirect
	github.com/go-text/render v0.2.1 // indirect
	github.com/go-text/typesetting v0.3.4 // indirect
	github.com/hack-pad/go-indexeddb v0.3.2 // indirect
	github.com/hack-pad/safejs v0.1.0 // indirect
	github.com/jeandeaual/go-locale v0.0.0-20250612000132-0ef82f21eade // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-runewidth v0.0.24 // indirect
	github.com/nicksnyder/go-i18n/v2 v2.5.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/srwiley/oksvg v0.0.0-20221011165216-be6e8873101c // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/tannevaled/gobig2 v0.2.0 // indirect
	github.com/timzifer/stilus v0.7.1-0.20261004051204-eaa0881d5596 // indirect
	github.com/yuin/goldmark v1.8.2 // indirect
	golang.org/x/image v0.24.0 // indirect
	golang.org/x/net v0.35.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Until a release of cera carries the form API (ADR 0006), the adapter
// builds against the cera next to it.
replace github.com/timzifer/cera => ../..
