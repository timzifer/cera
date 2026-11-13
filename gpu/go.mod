module github.com/timzifer/cera/gpu

go 1.27.1

require (
	github.com/gogpu/wgpu v0.34.14
	github.com/timzifer/cera v0.0.0
	github.com/timzifer/stilus v0.19.0
	github.com/timzifer/stilus/gpu v0.8.1
)

require (
	github.com/ajroetker/go-highway v0.0.12 // indirect
	github.com/andybalholm/brotli v1.2.6 // indirect
	github.com/go-images/jpeg v0.3.0 // indirect
	github.com/go-images/jpeg2000 v0.13.3 // indirect
	github.com/go-opentype/fonts v0.12.0 // indirect
	github.com/go-opentype/opentype v0.15.0 // indirect
	github.com/go-webgpu/goffi v0.6.8 // indirect
	github.com/go-webgpu/webgpu v0.5.5 // indirect
	github.com/gogpu/gpucontext v0.32.0 // indirect
	github.com/gogpu/gputypes v0.8.2 // indirect
	github.com/gogpu/naga v0.21.10 // indirect
	github.com/tannevaled/gobig2 v0.2.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

// The GPU surface builds against the cera next to it (ADR 0011).
replace github.com/timzifer/cera => ../

replace github.com/ajroetker/go-highway => github.com/timzifer/go-highway v0.0.13-timzifer.1
