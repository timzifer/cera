module github.com/timzifer/cera/readerdiff

go 1.26.4

require (
	github.com/go-pdfkit/reader v0.6.0
	github.com/timzifer/cera v0.0.0
)

require github.com/go-pdfkit/pdffont v0.3.1

require golang.org/x/text v0.34.0 // indirect

replace github.com/timzifer/cera => ../
