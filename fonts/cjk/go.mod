module github.com/timzifer/cera/fonts/cjk

go 1.27.1

require (
	github.com/go-opentype/fonts v0.12.0
	github.com/timzifer/cera v0.0.0
)

require (
	github.com/ajroetker/go-highway v0.0.12 // indirect
	github.com/andybalholm/brotli v1.2.6 // indirect
	github.com/go-images/jpeg v0.3.0 // indirect
	github.com/go-images/jpeg2000 v0.13.3 // indirect
	github.com/go-opentype/opentype v0.15.0 // indirect
	github.com/tannevaled/gobig2 v0.2.0 // indirect
	github.com/timzifer/stilus v0.9.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/timzifer/cera => ../..

replace github.com/ajroetker/go-highway => github.com/timzifer/go-highway v0.0.13-timzifer.1
