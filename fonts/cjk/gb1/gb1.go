// Package gb1 supplies Noto Sans SC for the Adobe-GB1 character collection.
package gb1

import (
	"github.com/go-opentype/fonts/notosanssc"

	"github.com/timzifer/cera/fonts/cjk"
)

// Collection is Noto Sans SC for Adobe-GB1.
var Collection = cjk.Collection{Ordering: "GB1", Program: notosanssc.TTF}
