// Package korea1 supplies Noto Sans KR for the Adobe-Korea1 character collection.
package korea1

import (
	"github.com/go-opentype/fonts/notosanskr"

	"github.com/timzifer/cera/fonts/cjk"
)

// Collection is Noto Sans KR for Adobe-Korea1.
var Collection = cjk.Collection{Ordering: "Korea1", Program: notosanskr.TTF}
