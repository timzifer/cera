// Bench worker for pdf.js in Node, drawing onto @napi-rs/canvas (see
// bench/protocol.md). npm install, then: node worker.mjs
import { createInterface } from "node:readline";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { createCanvas } from "@napi-rs/canvas";
import * as pdfjs from "pdfjs-dist/legacy/build/pdf.mjs";

const require = createRequire(import.meta.url);
const root = dirname(require.resolve("pdfjs-dist/package.json"));
const asset = (d) => pathToFileURL(join(root, d) + "/").href;
const version = require("pdfjs-dist/package.json").version;

let doc = null, task = null;

function ink(data) {
  let n = 0;
  for (let i = 0; i < data.length; i += 4) {
    if (data[i] !== 255) n++;
    if (data[i + 1] !== 255) n++;
    if (data[i + 2] !== 255) n++;
  }
  return data.length ? n / ((data.length / 4) * 3) : 0;
}

async function handle(f) {
  switch (f[0]) {
    case "version":
      return ["ok", `pdf.js ${version} (Node ${process.versions.node})`];
    case "open": {
      const data = new Uint8Array(await readFile(f[1]));
      const t0 = process.hrtime.bigint();
      const t = pdfjs.getDocument({
        data,
        password: f[2] || undefined,
        standardFontDataUrl: asset("standard_fonts"),
        cMapUrl: asset("cmaps"),
        cMapPacked: true,
        wasmUrl: asset("wasm"),
        verbosity: 0,
      });
      const d = await t.promise;
      const ns = process.hrtime.bigint() - t0;
      if (task) await task.destroy();
      doc = d;
      task = t;
      return ["ok", ns, d.numPages];
    }
    case "render": {
      const i = Number(f[1]), scale = Number(f[2]);
      const t0 = process.hrtime.bigint();
      const page = await doc.getPage(i + 1);
      const viewport = page.getViewport({ scale });
      const canvas = createCanvas(Math.ceil(viewport.width), Math.ceil(viewport.height));
      const ctx = canvas.getContext("2d");
      ctx.fillStyle = "#fff";
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      await page.render({ canvasContext: ctx, canvas, viewport }).promise;
      const ns = process.hrtime.bigint() - t0;
      const r = ["ok", ns, canvas.width, canvas.height];
      if (f[3] === "1") r.push(ink(ctx.getImageData(0, 0, canvas.width, canvas.height).data).toFixed(5));
      // Drop the page's operator list, so the next render parses it again.
      page.cleanup();
      return r;
    }
  }
  return ["unsupported"];
}

const rl = createInterface({ input: process.stdin, terminal: false });
for await (const line of rl) {
  const f = line.split("\t");
  if (f[0] === "quit") break;
  let r;
  try {
    r = await handle(f);
  } catch (e) {
    r = ["err", String(e?.message ?? e).replace(/[\t\n]/g, " ")];
  }
  process.stdout.write(r.join("\t") + "\n");
}
if (task) await task.destroy();
process.exit(0);
