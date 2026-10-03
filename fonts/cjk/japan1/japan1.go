// Package japan1 supplies Noto Sans JP for the Adobe-Japan1 character collection.
package japan1

import (
	"github.com/go-opentype/fonts/notosansjp"

	"github.com/timzifer/cera/fonts/cjk"
)

// Collection is Noto Sans JP for Adobe-Japan1.
var Collection = cjk.Collection{Ordering: "Japan1", Program: notosansjp.TTF}
