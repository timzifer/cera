package cera

import (
	"container/list"
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"sync"
)

// DocPos is a position in the text of a document: Pos on page Page.
type DocPos struct {
	Page int
	Pos  TextPos
}

// DocMatch is a passage found on page Page of a document.
type DocMatch struct {
	Page int
	TextMatch
}

// TextIndex holds the text of the pages of a document, laid out once and
// kept for the most recently used pages. It is safe for concurrent use.
type TextIndex struct {
	doc *Document
	opt TextOptions
	max int

	mu    sync.Mutex
	pages map[int]*indexPage
	lru   list.List // of *indexPage, the most recently used first
}

// indexPage is the text of a page, ready once ready is closed.
type indexPage struct {
	index int
	ready chan struct{}
	text  *PageText
	err   error
	elem  *list.Element

	page      *Page // set by layout
	linksOnce sync.Once
	links     []Link
}

// NewTextIndex returns an index of the text of d, laid out with opt, that
// keeps the text of at most maxPages pages; 0 or less means 64.
func NewTextIndex(d *Document, opt TextOptions, maxPages int) *TextIndex {
	if maxPages <= 0 {
		maxPages = 64
	}
	return &TextIndex{doc: d, opt: opt, max: maxPages, pages: map[int]*indexPage{}}
}

// Document returns the document of x.
func (x *TextIndex) Document() *Document { return x.doc }

// Page returns the text of page i (0-based). Several callers asking for
// the same page wait for one layout. When ctx ends before the text is
// laid out, Page returns its error and keeps nothing.
func (x *TextIndex) Page(ctx context.Context, i int) (*PageText, error) {
	for {
		x.mu.Lock()
		e := x.pages[i]
		if e != nil {
			x.lru.MoveToFront(e.elem)
			x.mu.Unlock()
		} else {
			e = &indexPage{index: i, ready: make(chan struct{})}
			e.elem = x.lru.PushFront(e)
			x.pages[i] = e
			for x.lru.Len() > x.max {
				old := x.lru.Remove(x.lru.Back()).(*indexPage)
				delete(x.pages, old.index)
			}
			x.mu.Unlock()
			x.layout(ctx, e)
		}
		select {
		case <-e.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if e.err != nil && interrupted(e.err) && ctx.Err() == nil {
			continue // another caller's context ended: lay it out again
		}
		return e.text, e.err
	}
}

// layout lays out the text of e's page and drops e again when ctx ended
// before.
func (x *TextIndex) layout(ctx context.Context, e *indexPage) {
	defer close(e.ready)
	p, err := x.doc.Page(e.index)
	e.page = p
	if err == nil {
		e.text, err = p.TextWith(ctx, x.opt)
	}
	e.err = err
	if err != nil && interrupted(err) {
		x.mu.Lock()
		if x.pages[e.index] == e {
			x.lru.Remove(e.elem)
			delete(x.pages, e.index)
		}
		x.mu.Unlock()
	}
}

// interrupted reports whether err comes from a context that ended.
func interrupted(err error) bool {
	return errors.Is(err, ErrDeadline) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// TextOf returns the text from a to b, in either order: the end of page
// a, the pages between and the start of page b, separated by an empty
// line.
func (x *TextIndex) TextOf(ctx context.Context, a, b DocPos, format TextFormat) (string, error) {
	if b.Page < a.Page || b.Page == a.Page && b.Pos < a.Pos {
		a, b = b, a
	}
	var parts []string
	for i := max(a.Page, 0); i <= min(b.Page, x.doc.NumPages()-1); i++ {
		t, err := x.Page(ctx, i)
		if err != nil {
			return strings.Join(parts, "\n\n"), err
		}
		r := t.All()
		if i == a.Page {
			r.Start = a.Pos
		}
		if i == b.Page {
			r.End = b.Pos
		}
		if s := t.TextOf(r, format); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// Find returns the places of query in the document, page by page, as
// PageText.Find finds them: forward from the matches that start at or
// after from, to the end of the document and on from its start; or, when
// backward is set, back from the matches that end at or before from. Each
// match comes once. A page whose text cannot be laid out yields its error
// with a DocMatch for that page and no Range, and the search goes on as
// long as the caller wants; when ctx ends, Find yields its error and
// stops.
func (x *TextIndex) Find(ctx context.Context, query string, opt SearchOptions, from DocPos, backward bool) iter.Seq2[DocMatch, error] {
	return func(yield func(DocMatch, error) bool) {
		n := x.doc.NumPages()
		if n == 0 {
			return
		}
		from.Page = min(max(from.Page, 0), n-1)
		// The page of from twice: first the part after from, at last the
		// part before it.
		for k := 0; k <= n; k++ {
			i := from.Page + k
			if backward {
				i = from.Page - k
			}
			i = (i%n + n) % n
			if err := ctx.Err(); err != nil {
				yield(DocMatch{Page: i}, err)
				return
			}
			t, err := x.Page(ctx, i)
			if err != nil {
				if ctx.Err() != nil {
					yield(DocMatch{Page: i}, ctx.Err())
					return
				}
				if k < n && !yield(DocMatch{Page: i}, err) {
					return // its error is yielded once
				}
				if t == nil {
					continue
				}
			}
			ms := t.Find(query, opt)
			if backward {
				slices.Reverse(ms)
			}
			for _, m := range ms {
				if i == from.Page {
					var first bool // in the part searched first
					if backward {
						first = m.Range.End <= from.Pos
					} else {
						first = m.Range.Start >= from.Pos
					}
					if first != (k == 0) {
						continue
					}
				}
				if !yield(DocMatch{Page: i, TextMatch: m}, nil) {
					return
				}
			}
		}
	}
}

// FindQuote returns the place of q as PageText.FindQuote finds it, on
// page near or else on the page nearest to it that has q.Exact; the
// pages after near come before those as far before it.
func (x *TextIndex) FindQuote(ctx context.Context, q Quote, opt SearchOptions, near int) (DocMatch, bool, error) {
	n := x.doc.NumPages()
	near = min(max(near, 0), n-1)
	for d := 0; d < n; d++ {
		for _, i := range []int{near + d, near - d} {
			if i < 0 || i >= n || d == 0 && i != near+d {
				continue
			}
			t, err := x.Page(ctx, i)
			if err != nil {
				if ctx.Err() != nil {
					return DocMatch{}, false, ctx.Err()
				}
				if t == nil {
					continue
				}
			}
			if m, ok := t.FindQuote(q, opt); ok {
				return DocMatch{Page: i, TextMatch: m}, true, nil
			}
		}
	}
	return DocMatch{}, false, nil
}
