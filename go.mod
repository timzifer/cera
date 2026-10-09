module github.com/timzifer/cera

go 1.27.1

require (
	github.com/andybalholm/brotli v1.2.6
	github.com/go-images/jpeg v0.3.0
	github.com/go-images/jpeg2000 v0.13.3
	github.com/go-opentype/fonts v0.12.0
	github.com/go-opentype/opentype v0.15.0
	github.com/tannevaled/gobig2 v0.2.0
	github.com/timzifer/stilus v0.9.0
	golang.org/x/text v0.42.0
)

require (
	github.com/ajroetker/go-highway v0.0.12 // indirect
	golang.org/x/sys v0.44.0 // indirect
)

replace github.com/ajroetker/go-highway => github.com/timzifer/go-highway v0.0.13-timzifer.1
