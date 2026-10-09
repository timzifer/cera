# 0013. Editing documents: `Document.Edit`

- Status: accepted
- Date: 2026-10-09
- Milestone: M9

## Context

cera reads PDFs to render them and keeps form values a user enters in a
`FormState`, but writes nothing back. The first writing feature,
`pdfedit.Extract` (v0.6.0), copies pages of one file into a new one: a
closed operation, bytes in, file out. Saving filled forms (#89), merging
files, deleting and reordering pages and, later, generating pages need
more, and a first attempt at a document type in `pdfedit` (#98, PR #99)
showed where a separate package leads:

- **Three types for one file.** `cera.Document` renders and reads the
  form, `pdfedit.Source` is a file pages are imported from and
  `pdfedit.Document` the file being edited. The same bytes are parsed up to
  three times, and a `FormState` of the one would have to be matched to the
  fields of the other before it could be saved.
- **Behaviour the API does not show.** A page kept its outline entries,
  form fields and structure when its document was opened for editing and
  lost them when it was imported, with nothing in the calls to tell.
- **No page model.** An opened document could only grow; pages could not
  be deleted, moved or turned.
- **A save mode that rarely fits.** An incremental update as an option of
  `Save` only makes sense for a document based on a file that did not need
  a repair.
- **The appearance generator and the form model are unexported in the
  root package.** A separate package needs a bridge to reach them, or they
  move out of the renderer for a reason that has nothing to do with
  rendering.

## Decision

Editing lives in the root package, on the document type there is:

```go
e := d.Edit()             // d *cera.Document: everything d has stays
e := cera.NewEditor()     // an empty document

e.ImportPages(at, src, cera.EditPage{Index: 2, Rotate: 90}) // src *cera.Document
e.DeletePages(3, 4)
e.MovePage(from, to)
e.RotatePage(i, 90)
e.SetFields(state)        // state from d.Form().NewState() (#92)

e.Save(w)                 // the whole file, new
e.Update(w)               // d's bytes and the changes appended (#91)
```

- **One rule for what a page brings.** Everything at the level of the
  document (outline, names, structure tree, form, layers, page labels,
  metadata) belongs to the document being edited. Pages imported from
  another document bring only what is on the page: content, resources,
  annotations, links to other pages imported from the same document; their
  widgets stay as plain annotations, unless the page is imported with its
  fields (`EditPage.Fields`): then the part of its field tree that leads
  to its widgets comes too, renamed at the root where the result has the
  name. Importing a page of the edited
  document itself gives such a copy too.
- **Deleting a page** removes what hangs on it alone: its annotations, and
  form fields whose widgets were all on deleted pages. Outline entries,
  links and named destinations that pointed at it lose their target.
- **`Save` and `Update` are two calls**, not a mode. `Update` appends the
  changed and new objects to the original bytes, so signatures stay valid;
  it fails for an editor made with `NewEditor` and for a document that
  needed a repair.
- **The root package keeps a thin API.** The work is done in internal
  packages: `internal/pdfwrite` (objects, serialisation, cross-reference
  sections) and `internal/pdfedit` (what a saved document is made of: page
  tree, catalogue, imported pages, links, layers, patches). They work on
  `*pdf.Document` and know nothing of rendering. Appearance streams of
  saved form values are made by the generator the renderer uses
  (`Document.widgetContent`), which stays in the root package: it draws
  with the document's fonts, and moving it out would take the font layer
  with it (#93).
- **An editor records what is to be written**; `Save` builds the file and
  may be called again. The document an editor is based on, and documents
  pages are imported from, stay usable for rendering while it is edited.
- **`pdfedit`** keeps `Extract` as a deprecated short form for
  `NewEditor`, `ImportPages` and `Save`.

## Consequences

- A file is opened once, rendered, filled in and saved through the same
  `*cera.Document`. `SetFields` needs no matching: the state's fields are
  the document's.
- The root package gains writing. The renderer itself does not change;
  what rendering and writing share (the form model, appearance
  generation) is used by both, so what is shown and what is saved cannot
  drift apart.
- Generating pages later (`AddPage` with a canvas, embedded fonts) is
  another editor method, not a new package.

## Alternatives considered

- **A package of its own (`cera/edit`, `cera/compose`, `pdfedit`).** Keeps
  the root package a renderer, but needs a bridge to the reader and the
  form model behind `cera.Document`, and leaves two document types in the
  hands of callers.
- **Incremental saving as a mode of `Save`.** Rejected: the mode is
  invalid in most states of an editor.
