"""Bench worker for MuPDF (PyMuPDF) and PDFium (pypdfium2); see
bench/protocol.md. Both bindings ship the native libraries:

    pip install -r requirements.txt   # pymupdf, pypdfium2

Usage: python pyworker.py mupdf|pdfium
"""

import os
import sys
import time


def ink(buf, n):
    """Share of colour bytes below white, over the first n bytes of buf
    (the parent asks for it untimed, as a check that a page drew)."""
    view = bytes(buf[:n])
    return (n - view.count(b"\xff")) / n if n else 0.0


class MuPDF:
    def __init__(self):
        import pymupdf

        self.m = pymupdf
        self.m.TOOLS.mupdf_display_errors(False)
        self.doc = None

    def version(self):
        return "mupdf %s (PyMuPDF %s)" % (self.m.mupdf_version, self.m.pymupdf_version)

    def open(self, data, password):
        doc = self.m.open(stream=data, filetype="pdf")
        if doc.needs_pass:
            doc.authenticate(password)
        self.doc = doc
        return doc.page_count

    def render(self, i, scale, want_ink):
        t0 = time.perf_counter_ns()
        page = self.doc.load_page(i)
        pix = page.get_pixmap(matrix=self.m.Matrix(scale, scale), alpha=False, annots=True)
        ns = time.perf_counter_ns() - t0
        r = [ns, pix.width, pix.height]
        if want_ink:
            r.append(ink(pix.samples_mv, len(pix.samples_mv)))
        return r


class PDFium:
    def __init__(self):
        import pypdfium2

        self.m = pypdfium2
        self.doc = None

    def version(self):
        v = getattr(self.m, "PDFIUM_INFO", None) or getattr(self.m, "V_PDFIUM", "?")
        return "pdfium %s (pypdfium2 %s)" % (v, getattr(self.m, "PYPDFIUM_INFO", getattr(self.m, "V_PYPDFIUM2", "?")))

    def open(self, data, password):
        self.doc = self.m.PdfDocument(data, password=password or None)
        return len(self.doc)

    def render(self, i, scale, want_ink):
        t0 = time.perf_counter_ns()
        page = self.doc[i]
        bm = page.render(scale=scale, draw_annots=True, may_draw_forms=True)
        ns = time.perf_counter_ns() - t0
        r = [ns, bm.width, bm.height]
        if want_ink:
            buf = bm.buffer
            r.append(ink(buf, bm.stride * bm.height))
        bm.close()
        page.close()
        return r


def main():
    engine = {"mupdf": MuPDF, "pdfium": PDFium}[sys.argv[1]]()
    out = sys.stdout
    for line in sys.stdin:
        f = line.rstrip("\n").split("\t")
        cmd = f[0]
        try:
            if cmd == "quit":
                return
            elif cmd == "pid":
                r = ["ok", os.getpid()]
            elif cmd == "version":
                r = ["ok", engine.version()]
            elif cmd == "open":
                with open(f[1], "rb") as fh:
                    data = fh.read()
                t0 = time.perf_counter_ns()
                n = engine.open(data, f[2] if len(f) > 2 else "")
                r = ["ok", time.perf_counter_ns() - t0, n]
            elif cmd == "render":
                r = ["ok"] + engine.render(int(f[1]), float(f[2]), len(f) > 3 and f[3] == "1")
            else:
                r = ["unsupported"]
        except Exception as e:  # noqa: BLE001 - every failure is a reply
            r = ["err", str(e).replace("\t", " ").replace("\n", " ") or type(e).__name__]
        out.write("\t".join(str(v) if not isinstance(v, float) else "%.5f" % v for v in r) + "\n")
        out.flush()


if __name__ == "__main__":
    main()
