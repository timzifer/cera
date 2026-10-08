//go:build !amd64 || purego

package cera

import (
	"image"
	"image/color"
)

// fillRegionStream is fillRegion where there are no non-temporal stores.
func fillRegionStream(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	fillRegion(dst, r.Intersect(dst.Rect), c)
}
