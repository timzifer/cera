package pdfedit

import (
	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/pdfwrite"
)

// OutlineItem is an entry of the outline an editor sets.
type OutlineItem struct {
	// Entries are what the entry has of its own: its /Title, /C and /F,
	// and where it leads when that is no page of the output — an action
	// (/A) or a named destination (/Dest). They are direct values without
	// references.
	Entries []pdf.Entry
	// Page is the page of the output the entry leads to, -1 for none, and
	// View what follows the page in its destination: /Fit, or /XYZ and
	// its numbers.
	Page int
	View pdf.Array
	// From, when not zero, is the entry of the edited document's outline
	// this one stands for: it leads where that one does — its /Dest, /A
	// and /SE are written again —, and Page and what Entries say of a
	// destination are not looked at.
	From pdf.Ref
	// Lost says that the entry led to a page that is not in the output.
	Lost bool
	// Open says that its kids are shown.
	Open bool
	Kids []OutlineItem
}

// outPage returns the output number of page i of the output, 0 for none.
func (b *builder) outPage(i int) pdfwrite.Ref {
	switch {
	case i < 0:
	case b.pageRefs != nil:
		if i < len(b.pageRefs) {
			return b.pageRefs[i]
		}
	case b.doc.Base != nil:
		// An update of a document whose pages stay where they are.
		if r, ok := b.doc.Base.PageRef(i + 1); ok {
			return pdfwrite.Ref(r.Num)
		}
	}
	return 0
}

// outItems makes the entries of the output of those an editor set. With
// prune, an entry that led to a page which is not in the output is left
// out, unless it has entries below it: then it stays as their heading.
func (b *builder) outItems(list []OutlineItem, depth int, n *int) []*outItem {
	var out []*outItem
	own := b.own()
	for _, it := range list {
		if *n >= maxOutlineItems || depth > maxOutlineDepth {
			break
		}
		*n++
		item := &outItem{open: it.Open, kids: b.outItems(it.Kids, depth+1, n)}
		var leads []pdf.Entry
		dead := it.Lost
		switch {
		case it.From.Num != 0 && own != nil:
			d, _ := own.obj(it.From).Dict()
			dest := d.Get("Dest")
			if dest.IsNull() {
				if act, ok := own.d.Resolve(d.Get("A")).Dict(); ok {
					if s, _ := own.d.Resolve(act.Get("S")).Name(); s == "GoTo" {
						dest = act.Get("D")
					}
				}
			}
			if a := own.explicit(dest); len(a) > 0 {
				if r, ok := a[0].Ref(); ok && own.pageTarget(r) == 0 {
					dead = true
				}
			}
			for _, k := range [...]pdf.Name{"Dest", "A", "SE"} {
				if v := d.Get(k); !v.IsNull() {
					leads = append(leads, pdf.Entry{Key: k, Val: own.im.Map(v)})
				}
			}
		case it.Page >= 0:
			if page := b.outPage(it.Page); page != 0 {
				leads = []pdf.Entry{{Key: "Dest", Val: append(pdf.Array{page.Object()}, it.View...).Object()}}
			} else {
				dead = true
			}
		}
		if b.doc.OutlinePrune {
			switch {
			case dead && len(item.kids) == 0:
				continue
			case dead:
				leads = nil
			case len(leads) == 0 && len(it.Kids) > 0 && len(item.kids) == 0 && !it.leads():
				// A heading all of whose entries went.
				continue
			}
		}
		for _, e := range it.Entries {
			if it.From.Num != 0 && (e.Key == "Dest" || e.Key == "A") {
				continue
			}
			item.entries = append(item.entries, e)
		}
		item.entries = append(item.entries, leads...)
		out = append(out, item)
	}
	return out
}

// setOutline writes the outline an editor set, with what imported pages
// bring after it unless it stands alone, and returns its root; false for
// an outline without entries, which is none.
func (b *builder) setOutline() (pdfwrite.Ref, bool) {
	n := 0
	top := b.outItems(b.doc.Outline, 1, &n)
	if !b.doc.OutlineAlone {
		top = append(top, b.importedOutlines()...)
	}
	if len(top) == 0 {
		return 0, false
	}
	root := b.w.Alloc()
	b.writeItems(top, root, pdf.Null, pdf.Null)
	b.w.Set(root, pdf.NewDict(
		pdf.Entry{Key: "Type", Val: pdf.Name("Outlines").Object()},
		pdf.Entry{Key: "First", Val: top[0].ref.Object()},
		pdf.Entry{Key: "Last", Val: top[len(top)-1].ref.Object()},
		pdf.Entry{Key: "Count", Val: pdf.Integer(shown(top))},
	).Object())
	return root, true
}

// leads reports whether the entries of it say where it leads.
func (it *OutlineItem) leads() bool {
	for _, e := range it.Entries {
		if e.Key == "Dest" || e.Key == "A" {
			return true
		}
	}
	return false
}
