package cera

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/timzifer/cera/internal/pdf"
	"github.com/timzifer/cera/internal/testpdf"
)

// structOther is a tagged document to import from, in German: three
// pages, a Document of a heading (of the type "Heading", which the role
// map makes H1) and a paragraph on the first page, a section on the
// second — a paragraph, a link with its annotation and a figure marked in
// a form XObject — and a paragraph on the third. The second paragraph has
// an identifier and an attribute class.
func structOther() []byte {
	text := func(tag string, mcid int, y int, s string) string {
		return fmt.Sprintf("/%s <</MCID %d>> BDC BT /F 12 Tf 10 %d Td (%s) Tj ET EMC ", tag, mcid, y, s)
	}
	res := "/Resources <</Font <</F 20 0 R>> /XObject <</X 12 0 R>>>>"
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /StructTreeRoot 40 0 R /MarkInfo <</Marked true>> /Lang (de-DE)>>",
			2:  "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Contents 10 0 R /StructParents 0 " + res + ">>",
			4:  "<</Type /Page /Parent 2 0 R /Contents 11 0 R /StructParents 1 /Annots [30 0 R] " + res + ">>",
			5:  "<</Type /Page /Parent 2 0 R /Contents 13 0 R /StructParents 2 " + res + ">>",
			10: testpdf.Stream("", []byte(text("Heading", 0, 70, "Eins")+text("P", 1, 40, "Zwei"))),
			11: testpdf.Stream("", []byte(text("P", 0, 70, "Drei")+text("Link", 1, 20, "Link")+"q 1 0 0 1 150 50 cm /X Do Q")),
			12: testpdf.Stream("/Type /XObject /Subtype /Form /BBox [0 0 20 20] /StructParents 4", []byte("/Figure <</MCID 0>> BDC 0 0 10 10 re f EMC")),
			13: testpdf.Stream("", []byte(text("P", 0, 70, "Vier"))),
			20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			30: "<</Type /Annot /Subtype /Link /Rect [10 15 50 30] /StructParent 3 /A <</S /URI /URI (https://example.com)>>>>",
			40: "<</Type /StructTreeRoot /K 41 0 R /ParentTree 60 0 R /ParentTreeNextKey 5 /RoleMap <</Heading /H1>>" +
				" /ClassMap <</Cls <</O /Layout /Placement /Block>>>> /IDTree <</Names [(p2) 43 0 R]>>>>",
			41: "<</Type /StructElem /S /Document /P 40 0 R /K [42 0 R 43 0 R 44 0 R 47 0 R]>>",
			42: "<</Type /StructElem /S /Heading /P 41 0 R /Pg 3 0 R /K 0>>",
			43: "<</Type /StructElem /S /P /P 41 0 R /Pg 3 0 R /K 1 /ID (p2) /C /Cls>>",
			44: "<</Type /StructElem /S /Sect /P 41 0 R /K [45 0 R 46 0 R 48 0 R]>>",
			45: "<</Type /StructElem /S /P /P 44 0 R /Pg 4 0 R /K 0>>",
			46: "<</Type /StructElem /S /Link /P 44 0 R /Pg 4 0 R /K [1 <</Type /OBJR /Obj 30 0 R>>]>>",
			47: "<</Type /StructElem /S /P /P 41 0 R /Pg 5 0 R /K 0>>",
			48: "<</Type /StructElem /S /Figure /P 44 0 R /Pg 4 0 R /Alt (Ein Quadrat) /K <</Type /MCR /Stm 12 0 R /MCID 0>>>>",
			60: "<</Nums [0 [42 0 R 43 0 R] 1 [45 0 R 46 0 R] 2 [47 0 R] 3 46 0 R 4 [48 0 R]]>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// structBase is a tagged document in English that pages are imported
// into: one page with a paragraph of the type "Heading", which its role
// map makes P, with the identifier and the class name structOther uses,
// the class defined otherwise.
func structBase() []byte {
	return testpdf.File{
		Objs: map[int]string{
			1:  "<</Type /Catalog /Pages 2 0 R /StructTreeRoot 40 0 R /MarkInfo <</Marked true /Suspects false>> /Lang (en-US)>>",
			2:  "<</Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 200 100]>>",
			3:  "<</Type /Page /Parent 2 0 R /Contents 10 0 R /StructParents 0 /Resources <</Font <</F 20 0 R>>>>>>",
			10: testpdf.Stream("", []byte("/Heading <</MCID 0>> BDC BT /F 12 Tf 10 70 Td (Base) Tj ET EMC")),
			20: "<</Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding>>",
			40: "<</Type /StructTreeRoot /K 41 0 R /ParentTree <</Nums [0 [42 0 R]]>> /ParentTreeNextKey 1 /RoleMap <</Heading /P>>" +
				" /ClassMap <</Cls <</O /Layout /Placement /Inline>>>> /IDTree <</Names [(p2) 42 0 R]>>>>",
			41: "<</Type /StructElem /S /Document /P 40 0 R /K [42 0 R]>>",
			42: "<</Type /StructElem /S /Heading /P 41 0 R /Pg 3 0 R /K 0 /ID (p2) /C /Cls>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

// treeString writes the structure tree of d, an element a line: its
// type, its role where that differs, its identifier, its language where
// it differs from the one above, and its content as MCID@page, annotations
// as aIndex@page.
func treeString(d *Document) string {
	t := d.Structure()
	if t == nil {
		return "untagged\n"
	}
	var b strings.Builder
	var walk func(e *StructElem, depth int, lang string)
	walk = func(e *StructElem, depth int, lang string) {
		b.WriteString(strings.Repeat("  ", depth) + e.Type)
		if e.Role != e.Type {
			b.WriteString("=" + e.Role)
		}
		if e.ID != "" {
			b.WriteString(" #" + e.ID)
		}
		if e.Lang != lang {
			b.WriteString(" lang=" + e.Lang)
		}
		for _, k := range e.Kids {
			switch {
			case k.Elem != nil:
			case k.Annot >= 0:
				fmt.Fprintf(&b, " a%d@%d", k.Annot, k.Page)
			case k.MCID >= 0:
				fmt.Fprintf(&b, " %d@%d", k.MCID, k.Page)
			}
		}
		b.WriteString("\n")
		for c := range e.Children() {
			walk(c, depth+1, e.Lang)
		}
	}
	for _, e := range t.Elems {
		walk(e, 0, "")
	}
	return b.String()
}

// parentTreeErrors checks the parent tree of d against its elements: the
// entry of every page, annotation and form XObject with a key leads to
// the elements that name their content, and every entry is one's.
func parentTreeErrors(d *Document) []string {
	r := d.r
	cat, _ := r.Catalog()
	root, _ := r.GetDict(cat, "StructTreeRoot")
	nums := map[int64]pdf.Object{}
	tree, _ := r.Resolve(root.Get("ParentTree")).Dict()
	pairs, _ := r.Resolve(tree.Get("Nums")).Array()
	for i := 0; i+1 < len(pairs); i += 2 {
		k, _ := pairs[i].Int()
		nums[k] = pairs[i+1]
	}
	next, _ := r.Resolve(root.Get("ParentTreeNextKey")).Int()
	var errs []string
	used := map[int64]bool{}
	key := func(what string, o pdf.Object) (pdf.Object, bool) {
		k, ok := r.Resolve(o).Int()
		if !ok {
			return pdf.Null, false
		}
		if used[k] {
			errs = append(errs, fmt.Sprintf("%s: key %d used twice", what, k))
		}
		used[k] = true
		if k >= next {
			errs = append(errs, fmt.Sprintf("%s: key %d not below /ParentTreeNextKey %d", what, k, next))
		}
		v, ok := nums[k]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: key %d not in the parent tree", what, k))
		}
		return v, ok
	}
	// names reports whether element o has kid, an MCID or an object.
	names := func(o pdf.Object, match func(pdf.Object) bool) bool {
		e, _ := r.Resolve(o).Dict()
		k := r.Resolve(e.Get("K"))
		kids, ok := k.Array()
		if !ok {
			kids = pdf.Array{e.Get("K")}
		}
		for _, kid := range kids {
			if match(r.Resolve(kid)) {
				return true
			}
		}
		return false
	}
	for i := range r.PageCount() {
		page, _ := r.Page(i + 1)
		pref, _ := r.PageRef(i + 1)
		if v, ok := key(fmt.Sprintf("page %d", i), page.Get("StructParents")); ok {
			arr, _ := r.Resolve(v).Array()
			for mcid, el := range arr {
				if !names(el, func(k pdf.Object) bool {
					n, ok := k.Int()
					if d, isDict := k.Dict(); isDict {
						n, ok = r.Resolve(d.Get("MCID")).Int()
						if pg, has := d.Get("Pg").Ref(); has && pg != pref {
							return false
						}
					}
					return ok && n == int64(mcid)
				}) {
					errs = append(errs, fmt.Sprintf("page %d: MCID %d leads to an element without it", i, mcid))
				}
			}
		}
		annots, _ := r.Resolve(page.Get("Annots")).Array()
		for j, a := range annots {
			ad, _ := r.Resolve(a).Dict()
			if v, ok := key(fmt.Sprintf("page %d annotation %d", i, j), ad.Get("StructParent")); ok {
				ar, _ := a.Ref()
				if !names(v, func(k pdf.Object) bool {
					d, _ := k.Dict()
					o, _ := d.Get("Obj").Ref()
					return o == ar
				}) {
					errs = append(errs, fmt.Sprintf("page %d annotation %d: leads to an element without it", i, j))
				}
			}
		}
		res, _ := r.Resolve(page.Get("Resources")).Dict()
		xobjs, _ := r.Resolve(res.Get("XObject")).Dict()
		for name, xo := range xobjs.All() {
			s, ok := r.Resolve(xo).Stream()
			if !ok || used[-1-int64(s.Ref.Num)] {
				continue
			}
			used[-1-int64(s.Ref.Num)] = true
			if v, ok := key(fmt.Sprintf("form %s", name), s.Dict.Get("StructParents")); ok {
				arr, _ := r.Resolve(v).Array()
				xr, _ := xo.Ref()
				for mcid, el := range arr {
					if !names(el, func(k pdf.Object) bool {
						d, _ := k.Dict()
						stm, _ := d.Get("Stm").Ref()
						n, _ := r.Resolve(d.Get("MCID")).Int()
						return stm == xr && n == int64(mcid)
					}) {
						errs = append(errs, fmt.Sprintf("form %s: MCID %d leads to an element without it", name, mcid))
					}
				}
			}
		}
	}
	for k := range nums {
		if !used[k] {
			errs = append(errs, fmt.Sprintf("key %d of the parent tree: nothing has it", k))
		}
	}
	return errs
}

// TestImportStructure imports tagged pages with their structure: into a
// tagged document, into an untagged one, into a new one, and from two
// documents into a new one.
func TestImportStructure(t *testing.T) {
	other := editOpen(t, structOther())
	opt := ImportOptions{Structure: true}
	for _, tt := range []struct {
		name string
		base []byte // nil: a new document
		// what is imported, in order: the documents (other, or the base
		// opened anew) and their pages
		imports func(t *testing.T, e *Editor)
		want    string
		classes string
		roles   string
	}{
		{"into a tagged document", structBase(), func(t *testing.T, e *Editor) {
			if err := e.ImportPagesWith(1, other, opt, EditPage{Index: 0}, EditPage{Index: 1}, EditPage{Index: 1}); err != nil {
				t.Fatal(err)
			}
		}, `Document lang=en-US
  Heading=P #p2 0@0
  Part lang=de-DE
    H1 0@1
    P #p2_2 1@1
    Sect
      P 0@2
      Link 1@2 a0@2
      Figure
`, "Cls Cls_2", "Heading=P"},
		{"into an untagged document", mergeBase(false), func(t *testing.T, e *Editor) {
			if err := e.ImportPagesWith(2, other, opt, EditPage{Index: 1}); err != nil {
				t.Fatal(err)
			}
		}, `Document lang=de-DE
  Sect
    P 0@2
    Link 1@2 a0@2
    Figure
`, "Cls", "Heading=H1"},
		{"into a new document", nil, func(t *testing.T, e *Editor) {
			if err := e.ImportPagesWith(0, other, opt, EditPage{Index: 0}, EditPage{Index: 2}); err != nil {
				t.Fatal(err)
			}
		}, `Document lang=de-DE
  Heading=H1 0@0
  P #p2 1@0
  P 0@1
`, "Cls", "Heading=H1"},
		{"from two documents", nil, func(t *testing.T, e *Editor) {
			if err := e.ImportPagesWith(0, other, opt, EditPage{Index: 0}); err != nil {
				t.Fatal(err)
			}
			if err := e.ImportPagesWith(1, editOpen(t, structBase()), opt, EditPage{Index: 0}); err != nil {
				t.Fatal(err)
			}
		}, `Document lang=de-DE
  Heading=H1 0@0
  P #p2 1@0
  Part lang=en-US
    P #p2_2 0@1
`, "Cls Cls_2", "Heading=H1"},
	} {
		for _, mode := range []string{"save", "update"} {
			if tt.base == nil && mode == "update" {
				continue
			}
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				e := NewEditor()
				if tt.base != nil {
					e = editOpen(t, tt.base).Edit()
				}
				tt.imports(t, e)
				var out []byte
				if mode == "save" {
					out = editSave(t, e)
				} else {
					out = editUpdate(t, e, tt.base)
				}
				d := editOpen(t, out)
				if got := treeString(d); got != tt.want {
					t.Errorf("tree:\n%s\nwant:\n%s", got, tt.want)
				}
				for _, err := range parentTreeErrors(d) {
					t.Error(err)
				}
				cat, _ := d.r.Catalog()
				root, _ := d.r.GetDict(cat, "StructTreeRoot")
				cm, _ := d.r.Resolve(root.Get("ClassMap")).Dict()
				var classes []string
				for k := range cm.All() {
					classes = append(classes, string(k))
				}
				if got := strings.Join(classes, " "); got != tt.classes {
					t.Errorf("classes %q, want %q", got, tt.classes)
				}
				rm, _ := d.r.Resolve(root.Get("RoleMap")).Dict()
				var roles []string
				for k, v := range rm.All() {
					n, _ := d.r.Resolve(v).Name()
					roles = append(roles, string(k)+"="+string(n))
				}
				if got := strings.Join(roles, " "); got != tt.roles {
					t.Errorf("role map %q, want %q", got, tt.roles)
				}
				mi, _ := d.r.GetDict(cat, "MarkInfo")
				if m, _ := mi.Get("Marked").Bool(); !m {
					t.Error("/MarkInfo does not say /Marked")
				}
			})
		}
	}
}

// TestImportStructureTwice tags a page imported twice once: its second
// copy, and that copy's annotation, have no keys.
func TestImportStructureTwice(t *testing.T) {
	other := editOpen(t, structOther())
	e := NewEditor()
	if err := e.ImportPagesWith(0, other, ImportOptions{Structure: true}, EditPage{Index: 1}, EditPage{Index: 1}); err != nil {
		t.Fatal(err)
	}
	d := editOpen(t, editSave(t, e))
	for _, err := range parentTreeErrors(d) {
		t.Error(err)
	}
	page, _ := d.r.Page(2)
	if page.Has("StructParents") {
		t.Error("the second copy has /StructParents")
	}
	annots, _ := d.r.Resolve(page.Get("Annots")).Array()
	if len(annots) != 1 {
		t.Fatalf("%d annotations on the second copy", len(annots))
	}
	if ad, _ := d.r.Resolve(annots[0]).Dict(); ad.Has("StructParent") {
		t.Error("the annotation of the second copy has /StructParent")
	}
	if got, want := treeString(d), "Document lang=de-DE\n  Sect\n    P 0@0\n    Link 1@0 a0@0\n    Figure\n"; got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
}

// TestImportNoStructure leaves imported pages untagged without the
// option, as before.
func TestImportNoStructure(t *testing.T) {
	other := editOpen(t, structOther())
	e := NewEditor()
	if err := e.ImportPages(0, other, EditPage{Index: 0}); err != nil {
		t.Fatal(err)
	}
	d := editOpen(t, editSave(t, e))
	if d.Structure() != nil {
		t.Error("the result is tagged")
	}
	page, _ := d.r.Page(1)
	if page.Has("StructParents") {
		t.Error("the page has /StructParents")
	}
}

// TestImportStructureOptimised brings the structure into a file made
// smaller and linearised: the forms whose keys change are pruned too.
func TestImportStructureOptimised(t *testing.T) {
	other := editOpen(t, structOther())
	e := editOpen(t, structBase()).Edit()
	if err := e.ImportPagesWith(1, other, ImportOptions{Structure: true}, EditPage{Index: 0}, EditPage{Index: 1}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opt := SaveOptions{ObjectStreams: ObjectStreamsAlways, Linearize: true, PruneResources: true, JoinDuplicates: true, Compress: true}
	if err := e.SaveWith(&out, opt); err != nil {
		t.Fatal(err)
	}
	d := editOpen(t, out.Bytes())
	for _, err := range parentTreeErrors(d) {
		t.Error(err)
	}
	if got, want := treeString(d), "Document lang=en-US\n  Heading=P #p2 0@0\n  Part lang=de-DE\n    H1 0@1\n    P #p2_2 1@1\n    Sect\n      P 0@2\n      Link 1@2 a0@2\n      Figure\n"; got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
}
