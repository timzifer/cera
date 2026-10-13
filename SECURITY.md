# Security

cera reads files nobody vouches for. A PDF from the net is an input an
attacker wrote, and what cera does with it — in a server that makes
previews, in a browser tab, in a viewer — must stay within what the
caller asked for: the page drawn or an error, in the time and the memory
it was given.

## Reporting

Report a vulnerability in private, through GitHub:
<https://github.com/timzifer/cera/security/advisories/new>
("Report a vulnerability" under the repository's *Security* tab).

Please do not open a public issue for it, and attach the file that shows
it, or say how to make one.

Fixes go into the latest release; older tags are not patched.

## What counts

- **A panic that escapes.** Every entry point recovers panics into a
  `*PanicError`; one that reaches the caller's goroutine is a bug of this
  kind, wherever it comes from.
- **A hang, or a deadline that does not hold.** A render, a text
  extraction or a search that does not return within `Deadline` or after
  its context is cancelled, give or take the work of one operation; an
  `Open` that never returns.
- **A budget that does not hold.** The limits in
  [docs/budgets.md](docs/budgets.md) bound what one document makes cera
  allocate and how deep it recurses. Memory or stack beyond them — a
  small file that makes cera allocate without bound — counts, and so
  does a budget that is missing where one is needed.
- **Reading or writing outside what was given**: memory outside the
  file's bytes (cera and its rasteriser use `unsafe` in a few places), a
  file on disk that nobody named (`fonts/system` reads the font
  directories and nothing else).
- **A redaction that leaves what it took out.** `Editor.Redact` says
  what it removes; content it claims to have removed and that is still in
  the file is a vulnerability. What it leaves by design is listed with
  it.
- **A signature that verifies and should not**: `Signature.Verify`
  reporting an intact signature over changed bytes, a change after
  signing it does not report, a chain it accepts against roots that do
  not vouch for it.
- **Encryption that is weaker than it says**: a file `SaveWith` encrypts
  that opens without its passwords, permissions the editor does not
  hold itself to.
- **A signature or a file cera writes that is not what was asked**:
  bytes outside the signed range that change what a signed file shows.

## What does not

- A page that is drawn wrong, a feature that is missing: these are bugs,
  and the issue tracker is the place.
- Work within the budgets: a large page takes memory in proportion to
  its pixels, a file of a million objects time in proportion to them.
  The caller bounds both, with the scale, `Deadline` and the cache
  options.
- What a document asks a viewer to do: links, JavaScript, launch actions
  and form submissions are reported to the caller, who decides. cera
  runs and follows none of them.
- The trust a caller puts into a certificate: `Verify` checks the chain
  against the roots it is given and no revocation.

## How it is tested

Fuzz targets for the parser, the content interpreter, fonts, images,
colour, forms, text and the editor run in CI on every change and longer
every week, with the files of earlier findings as their seeds. The corpus
runs draw, read and rewrite some thousand files of pdf.js and other
collections, many of them written to break readers, under a
watchdog: a panic, a hang or a missed deadline fails the run.
