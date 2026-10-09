package cera

import "github.com/timzifer/cera/internal/pdf"

// maxStructNodes bounds the structure tree walked for the reading order.
const maxStructNodes = 1 << 21

// structRanks returns the place of each marked-content identifier of page
// i in the structure tree (PDF 2.0, 14.7): the order in which a depth-first
// walk meets them. It is nil when the document has no structure tree, or
// says the tree may be wrong (/MarkInfo /Suspects).
func (d *Document) structRanks(i int) map[int]int {
	d.structOnce.Do(func() {
		defer func() { _ = recover() }() // a broken tree gives no order
		d.structMCIDs = d.readStructTree()
	})
	ids := d.structMCIDs[i]
	if len(ids) == 0 {
		return nil
	}
	ranks := make(map[int]int, len(ids))
	for r, id := range ids {
		if _, ok := ranks[id]; !ok {
			ranks[id] = r
		}
	}
	return ranks
}

// readStructTree walks the structure tree and returns the marked-content
// identifiers of each page in the order of the tree.
func (d *Document) readStructTree() map[int][]int {
	cat, err := d.r.Catalog()
	if err != nil {
		return nil
	}
	if suspects, _ := d.resolve(d.dict(cat.Get("MarkInfo")).Get("Suspects")).Bool(); suspects {
		return nil
	}
	root := d.dict(cat.Get("StructTreeRoot"))
	if root.IsZero() {
		return nil
	}
	type item struct {
		o    pdf.Object
		page int // the page of the nearest /Pg, -1 for none
	}
	out := map[int][]int{}
	seen := map[pdf.Ref]bool{}
	stack := []item{{root.Get("K"), -1}}
	for n := 0; len(stack) > 0 && n < maxStructNodes; n++ {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		o := it.o
		if r, ok := o.Ref(); ok {
			if seen[r] {
				continue
			}
			seen[r] = true
			o = d.resolve(o)
		}
		if v, ok := o.Int(); ok {
			if it.page >= 0 && v >= 0 && v <= 1<<31-1 {
				out[it.page] = append(out[it.page], int(v))
			}
			continue
		}
		if a, ok := o.Array(); ok {
			for k := len(a) - 1; k >= 0; k-- {
				stack = append(stack, item{a[k], it.page})
			}
			continue
		}
		e, ok := o.Dict()
		if !ok {
			continue
		}
		page := it.page
		if r, ok := e.Get("Pg").Ref(); ok {
			page = d.pageIndex(r)
		}
		switch t, _ := d.name(e.Get("Type")); t {
		case "MCR":
			// Marked content in a form XObject (/Stm) is numbered in the
			// form's own content, not the page's.
			if v, ok := d.num(e.Get("MCID")); ok && page >= 0 && !e.Has("Stm") {
				if id := mcid(v); id >= 0 {
					out[page] = append(out[page], id)
				}
			}
		case "OBJR":
		default:
			stack = append(stack, item{e.Get("K"), page})
		}
	}
	return out
}
