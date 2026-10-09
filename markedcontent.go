package cera

import (
	"github.com/timzifer/cera/internal/content"
	"github.com/timzifer/cera/internal/pdf"
)

// beginMarked tells the MarkedContentDevice of the sequence a BMC or BDC
// begins. Its properties are a dictionary operand or a named entry of the
// /Properties resources.
func (in *interp) beginMarked(sc *content.Scanner, res pdf.Dict) {
	mc := &in.mcBuf
	*mc = MarkedContent{MCID: -1}
	if sc.Len() >= 1 {
		if tag := sc.Arg(0); tag.Kind == content.Name {
			mc.Tag = string(sc.Text(tag))
		}
	}
	var num func(key string) (float64, bool)
	var str func(key string) (string, bool)
	if sc.Len() >= 2 {
		switch p := sc.Arg(1); p.Kind {
		case content.Dict:
			num = func(key string) (float64, bool) {
				if v := sc.DictGet(p, key); v != nil && v.Kind == content.Number {
					return v.Num, true
				}
				return 0, false
			}
			str = func(key string) (string, bool) {
				v := sc.DictGet(p, key)
				if v == nil || (v.Kind != content.String && v.Kind != content.HexString) {
					return "", false
				}
				return decodeTextString(sc.Text(v)), true
			}
		case content.Name:
			d := in.doc.dict(in.lookupRef(res, "Properties", sc, p))
			num = func(key string) (float64, bool) { return in.doc.num(d.Get(pdf.Name(key))) }
			str = func(key string) (string, bool) {
				o := in.doc.resolve(d.Get(pdf.Name(key)))
				if _, ok := o.Str(); !ok {
					return "", false
				}
				return textString(o), true
			}
		}
	}
	if num != nil {
		if v, ok := num("MCID"); ok {
			mc.MCID = mcid(v)
		}
		mc.ActualText, mc.HasActualText = str("ActualText")
		mc.Alt, _ = str("Alt")
		mc.Lang, _ = str("Lang")
	}
	in.mcd.BeginMarkedContent(mc)
}

// mcid returns v as a marked-content identifier: a non-negative integer,
// else -1.
func mcid(v float64) int {
	if v < 0 || v > 1<<31-1 || v != float64(int(v)) {
		return -1
	}
	return int(v)
}
