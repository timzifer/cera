// Package cns1 supplies Noto Sans SC for the Adobe-CNS1 character collection.
//
// There is no Noto Sans TC among go-opentype's fonts: traditional
// characters are drawn with Noto Sans SC, which has most of them, in its
// simplified-Chinese glyph forms.
package cns1

import (
	"github.com/go-opentype/fonts/notosanssc"

	"github.com/timzifer/cera/fonts/cjk"
)

// Collection is Noto Sans SC for Adobe-CNS1.
var Collection = cjk.Collection{Ordering: "CNS1", Program: notosanssc.TTF}
