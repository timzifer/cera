// host.mjs runs ceraworker built for wasip1 under Node's WASI, on V8, as a
// browser would run it: BENCH_WASM is the module, BENCH_WASM_ROOT the
// directory mounted as the root.
import { readFile } from "node:fs/promises";
import { WASI } from "node:wasi";

const wasi = new WASI({
  version: "preview1",
  args: ["ceraworker"],
  env: {},
  preopens: { "/": process.env.BENCH_WASM_ROOT },
  returnOnExit: true,
});
const mod = await WebAssembly.compile(await readFile(process.env.BENCH_WASM));
const inst = await WebAssembly.instantiate(mod, wasi.getImportObject());
process.exitCode = wasi.start(inst);
