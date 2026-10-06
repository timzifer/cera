# Worker protocol

Every engine is driven by a worker process: `ceraworker` (natively and as
WebAssembly), `workers/pyworker.py` (MuPDF through PyMuPDF, PDFium through
pypdfium2), `workers/hayro` (Rust), `workers/pdfjs/worker.mjs` (Node) and
the bench binary itself for PDFium compiled to WebAssembly. The parent
writes one request per line and reads one reply per line; fields are
separated by tabs.

| request | reply | |
|---|---|---|
| `pid` | `ok <process id>` | optional: the process to measure memory of, if not the one started (a Python venv's launcher) |
| `version` | `ok <name and version>` | |
| `open <path> [password]` | `ok <ns> <pages>` | the file is read untimed, opening it is timed |
| `render <page> <scale> [1]` | `ok <ns> <width> <height> [ink]` | page 0-based; a new bitmap on white; with `1`, the share of colour values below white (untimed) |
| `all <scale> <threads>` | `ok <ns>` | every page on that many threads; `unsupported` if the engine has no threads of its own |
| `again <page> <scale>` | `ok <ns first> <ns again>` | cera only: render, then render again from the display list |
| `allocs <page> <scale>` | `ok <allocs> <bytes>` | cera only, the bitmap not counted |
| `quit` | — | |

An error is `err <message>`, a request the engine does not know
`unsupported`. Times are measured by the worker, around the engine's own
calls only, and never leave the bench tool: it turns them into ratios
straight away.

`render` loads the page afresh each time, as a viewer showing it for the
first time does; what an engine caches per document (fonts, images) stays,
as it would in a viewer.
