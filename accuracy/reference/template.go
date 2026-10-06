package main

// reportHTML is the report page: self-contained, light and dark.
const reportHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>cera reference report</title>
<style>
:root {
  color-scheme: light;
  --surface-1: #fcfcfb; --surface-2: #f3f2ef; --rule: #e4e3df;
  --text-primary: #0b0b0b; --text-secondary: #52514e; --text-muted: #7a7974;
  --series-1: #2a78d6; --series-2: #eb6834; --series-3: #1baf7a; --series-4: #eda100; --series-5: #e87ba4;
  --q0: #f3f2ef; --q1: #cde2fb; --q2: #9ec5f4; --q3: #6da7ec; --q4: #3987e5; --q5: #256abf; --q6: #184f95;
  --q-dark-text: #ffffff; --critical: #d03b3b; --contested: #aaaaaa;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    color-scheme: dark;
    --surface-1: #1a1a19; --surface-2: #242423; --rule: #383835;
    --text-primary: #ffffff; --text-secondary: #c3c2b7; --text-muted: #9a998f;
    --series-1: #3987e5; --series-2: #d95926; --series-3: #199e70; --series-4: #c98500; --series-5: #d55181;
    --q0: #242423; --q1: #0d366b; --q2: #104281; --q3: #184f95; --q4: #1c5cab; --q5: #2a78d6; --q6: #5598e7;
  }
}
:root[data-theme="dark"] {
  color-scheme: dark;
  --surface-1: #1a1a19; --surface-2: #242423; --rule: #383835;
  --text-primary: #ffffff; --text-secondary: #c3c2b7; --text-muted: #9a998f;
  --series-1: #3987e5; --series-2: #d95926; --series-3: #199e70; --series-4: #c98500; --series-5: #d55181;
  --q0: #242423; --q1: #0d366b; --q2: #104281; --q3: #184f95; --q4: #1c5cab; --q5: #2a78d6; --q6: #5598e7;
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--surface-1); color: var(--text-primary);
  font: 14px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { max-width: 1120px; margin: 0 auto; padding: 32px 16px 64px; }
h1 { font-size: 24px; margin: 0 0 4px; }
h2 { font-size: 18px; margin: 40px 0 4px; }
p.lead, .muted, figcaption .muted { color: var(--text-secondary); }
p.note { color: var(--text-secondary); max-width: 760px; margin: 4px 0 16px; }
.meta { color: var(--text-muted); font-size: 13px; }
.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 12px; margin: 24px 0 8px; }
.tile { background: var(--surface-2); border-radius: 8px; padding: 14px 16px; }
.tile .v { font-size: 28px; font-weight: 600; font-variant-numeric: tabular-nums; }
.tile .l { color: var(--text-secondary); font-size: 13px; }
.facets { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 8px 24px; }
figure { margin: 0; }
figcaption { font-weight: 600; font-size: 13px; margin: 8px 0 2px; }
svg { width: 100%; height: auto; display: block; overflow: visible; }
svg text { font: 11px system-ui, sans-serif; fill: var(--text-secondary); }
svg .lab { fill: var(--text-primary); font-size: 12px; }
svg .val { fill: var(--text-primary); font-variant-numeric: tabular-nums; }
svg .tick { fill: var(--text-muted); font-size: 10px; }
svg .grid { stroke: var(--rule); stroke-width: 1; }
svg .ref { stroke: var(--text-secondary); stroke-width: 1.5; }
svg .hit { fill: transparent; }
svg .dot { stroke: var(--surface-1); stroke-width: 2; }
.s1 { fill: var(--series-1); } .s2 { fill: var(--series-2); } .s3 { fill: var(--series-3); }
.s4 { fill: var(--series-4); } .s5 { fill: var(--series-5); }
.heat { max-width: 470px; }
.heat .diag { fill: var(--surface-2); }
.q0 { fill: var(--q0); } .q1 { fill: var(--q1); } .q2 { fill: var(--q2); } .q3 { fill: var(--q3); }
.q4 { fill: var(--q4); } .q5 { fill: var(--q5); } .q6 { fill: var(--q6); }
svg .cellv { fill: var(--text-primary); font-variant-numeric: tabular-nums; }
svg .q4t, svg .q5t, svg .q6t { fill: var(--q-dark-text); }
.toggle { display: inline-flex; gap: 2px; background: var(--surface-2); border-radius: 8px; padding: 3px; margin: 24px 0 0; }
.toggle button { font: inherit; font-size: 13px; border: 0; border-radius: 6px; padding: 6px 14px; background: transparent; color: var(--text-secondary); cursor: pointer; }
.toggle button[aria-selected="true"] { background: var(--surface-1); color: var(--text-primary); font-weight: 600; }
.legend { list-style: none; display: flex; flex-wrap: wrap; gap: 4px 16px; padding: 0; margin: 8px 0; }
.legend li { display: flex; align-items: center; gap: 6px; color: var(--text-secondary); font-size: 13px; }
.legend .key { width: 12px; height: 12px; border-radius: 3px; display: inline-block; }
.legend .key.s1 { background: var(--series-1); } .legend .key.s2 { background: var(--series-2); }
.legend .key.s3 { background: var(--series-3); } .legend .key.s4 { background: var(--series-4); }
.legend .key.s5 { background: var(--series-5); }
.scroll { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; font-size: 13px; font-variant-numeric: tabular-nums; }
th, td { padding: 6px 10px; border-bottom: 1px solid var(--rule); text-align: right; white-space: nowrap; }
th:first-child, td:first-child, td.text { text-align: left; }
td.text { white-space: normal; color: var(--text-secondary); }
th { color: var(--text-secondary); font-weight: 600; cursor: pointer; user-select: none; }
th[aria-sort="ascending"]::after { content: " ↑"; } th[aria-sort="descending"]::after { content: " ↓"; }
details { margin: 8px 0; } summary { cursor: pointer; color: var(--text-secondary); }
.gallery { display: grid; gap: 24px; }
.shot h3 { font-size: 13px; margin: 0 0 6px; }
.shot .imgs { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.shot img { width: 100%; border: 1px solid var(--rule); border-radius: 4px; background: #fff; }
.shot .cap { font-size: 12px; color: var(--text-muted); text-align: center; }
.swatch { display: inline-block; width: 10px; height: 10px; border-radius: 2px; vertical-align: -1px; }
.fig { margin: 8px 0; }
.fig svg { width: 100%; height: auto; display: block; }
.fig-dark { display: none; }
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) .fig-light { display: none; }
  :root:not([data-theme="light"]) .fig-dark { display: block; }
}
:root[data-theme="dark"] .fig-light { display: none; }
:root[data-theme="dark"] .fig-dark { display: block; }
#tip { position: fixed; pointer-events: none; background: var(--text-primary); color: var(--surface-1);
  padding: 6px 8px; border-radius: 6px; font-size: 12px; max-width: 320px; display: none; z-index: 10; }
@media (max-width: 640px) { .shot .imgs { grid-template-columns: 1fr; } }
</style>
</head>
<body>
<main>
<h1>cera against the references</h1>
<div class="meta">{{.R.Generated}} · {{.R.DPI}} dpi · {{.R.Files}} files · {{.R.Pages}} pages ({{.R.Compared}} compared) · {{if .R.Annotations}}with annotations (MuPDF draws none, PDFium no form widgets){{else}}page content only, no annotations{{end}}{{if .R.Overprint}} · cera simulates overprint{{end}} · cera's magnified images {{.R.ImageFilter}}{{if .R.CMYKProfile}} · cera's DeviceCMYK through {{.R.CMYKProfile}}{{end}} · engines: {{join .R.Engines ", "}}{{if .R.Sample}} · random batch of {{.R.Sample}}, seed {{.R.Seed}}{{end}} · {{.R.Dirs}}</div>
{{if .R.Missing}}<p class="note">Not available: {{join .R.Missing "; "}}.</p>{{end}}

<p class="lead">No renderer is ground truth. Every engine is held against the median of all
the others, on the pixels where all the others agree (within 16 levels per channel); where
they disagree among themselves, the page says nothing about who is right and is counted
as contested. Shares are of the <em>inked area</em>, the pixels that are not paper white in
any rendering. The synthetic drawings are also compared with an exact rendering.</p>

<div class="tiles">
  <div class="tile"><div class="v">{{pct (index .R.Coarse.Outlier "cera")}}</div><div class="l">content: cera differs where all other engines agree ({{.Box}}×{{.Box}} px boxes)</div></div>
  <div class="tile"><div class="v">{{.PlaceCoarse}} of {{len .R.Engines}}</div><div class="l">cera's place on content (1 = fewest outliers); on edges {{.PlaceFine}} of {{len .R.Engines}}</div></div>
  <div class="tile"><div class="v">{{pct (index .R.Fine.Outlier "cera")}}</div><div class="l">edges: the same per pixel</div></div>
  <div class="tile"><div class="v">{{pct .R.Coarse.Contested}}</div><div class="l">contested content: the references disagree among themselves ({{pct .R.Fine.Contested}} per pixel)</div></div>
  <div class="tile"><div class="v">{{.InkMedian}}</div><div class="l">cera's ink against the exact drawings (median; 1× is exact)</div></div>
</div>

<div class="toggle" role="tablist">{{range $i, $v := .Views}}<button role="tab" data-view="{{$v.ID}}" aria-selected="{{if eq $i 0}}true{{else}}false{{end}}">{{$v.Name}}</button>{{end}}</div>
{{range $i, $v := .Views}}<section class="view" id="view-{{$v.ID}}"{{if ne $i 0}} hidden{{end}}>
<p class="note"><strong>{{$v.Name}}</strong> compares {{$v.What}}.</p>
<h2>Where all the others agree, who differs?</h2>
<p class="note">Share of the inked area on which an engine differs from the median of the other engines while those agree. Lower is better; one scale for all panels.</p>
{{$v.Bars}}
<details><summary>Table</summary><div class="scroll"><table class="sortable">
<thead><tr><th>category</th><th>pages</th>{{range $v.Engines}}<th>{{.}}</th>{{end}}<th>contested</th></tr></thead>
<tbody><tr><td>all</td><td>{{$v.V.Compared}}</td>{{range $v.Engines}}<td>{{get $v.V.Outlier .}}</td>{{end}}<td>{{pct $v.V.Contested}}</td></tr>
{{range $c := $v.V.Categories}}<tr><td>{{$c.Category}}</td><td>{{$c.Pages}}</td>{{range $v.Engines}}<td>{{get $c.Outlier .}}</td>{{end}}<td>{{pct $c.Contested}}</td></tr>
{{end}}</tbody></table></div></details>

<h2>How often two engines differ</h2>
<p class="note">Share of the inked area on which a pair differs by more than 16 levels. Engines that agree with each other but not with cera point at cera; a reference that differs from all points at that reference.</p>
{{$v.Heat}}

<h2>Files</h2>
<p class="note">Sorted by cera's outlier share; click a heading to sort. The columns per engine are the share on which it differs from cera.</p>
<div class="scroll"><table class="sortable">
<thead><tr><th>file</th><th>category</th><th>pages</th><th>cera outliers</th><th>contested</th>{{range notCera $v.Engines}}<th>vs {{.}}</th>{{end}}<th>unsupported</th></tr></thead>
<tbody>{{range $f := $v.V.FileRows}}<tr><td>{{$f.File}}</td><td class="text">{{$f.Category}}</td><td>{{$f.Pages}}</td><td>{{pct $f.CeraOutlier}}</td><td>{{pct $f.Contested}}</td>{{range notCera $v.Engines}}<td>{{get $f.VsCera .}}</td>{{end}}<td class="text">{{join $f.Unsupported " "}}{{if $f.Failed}} <strong>failed: {{join $f.Failed ", "}}</strong>{{end}}</td></tr>
{{end}}</tbody></table></div>

{{if $v.V.Keys}}
<h2>Unsupported features</h2>
<p class="note">cera's <code>Stats.Unsupported</code> keys and cera's outlier share on the pages that use them.</p>
<div class="scroll"><table class="sortable"><thead><tr><th>key</th><th>pages</th><th>cera outliers</th></tr></thead>
<tbody>{{range $v.V.Keys}}<tr><td>{{.Key}}</td><td>{{.Pages}}</td><td>{{pct .CeraOutlier}}</td></tr>{{end}}</tbody></table></div>
{{end}}
</section>
{{end}}

{{if .R.Exact}}
<h2>Against the exact drawings</h2>
<p class="note">The synthetic drawings rendered exactly: each pixel's area covered by the geometry (sampled at 64 rows per pixel, exact along them). The dot is an engine's ink relative to the exact ink: right of 1× draws heavier lines, left lighter; within a row the engines are set slightly apart so that equal values stay visible. Hairlines are one device pixel wide in the exact rendering; engines differ there by design.</p>
{{.Ink}}
<details><summary>Table</summary><div class="scroll"><table class="sortable">
<thead><tr><th>drawing</th><th>engine</th><th>ink</th><th>&gt;16</th><th>mean error (1/255)</th><th>p99</th></tr></thead>
<tbody>{{range .R.Exact}}<tr><td>{{.Scene}}</td><td class="text"><span class="swatch s{{slot .Engine}}" style="background:var(--series-{{slot .Engine}})"></span> {{.Engine}}</td><td>{{ratio .InkRatio}}</td><td>{{pct .Over}}</td><td>{{num .MAE}}</td><td>{{.P99}}</td></tr>
{{end}}</tbody></table></div></details>
{{end}}

{{if .Shots}}
<h2>Pages where cera stands out most</h2>
<p class="note">By content outliers. cera, the median of the references, and the map: <span class="swatch" style="background:var(--critical)"></span> cera differs where the others agree, <span class="swatch" style="background:var(--contested)"></span> the references disagree.</p>
<div class="gallery">{{range .Shots}}<div class="shot"><h3>{{.Title}} · {{.Share}}</h3><div class="imgs">
<div><img src="{{.Cera}}" alt="cera"><div class="cap">cera</div></div>
<div><img src="{{.Cons}}" alt="median of the references"><div class="cap">references (median)</div></div>
<div><img src="{{.Outly}}" alt="outlier map"><div class="cap">outliers</div></div></div></div>{{end}}</div>
{{end}}

{{if .R.Excepted}}<h2>Left out</h2><p class="note">Pages the references cannot judge (exceptions.go).</p><ul>{{range .R.Excepted}}<li>{{.}}</li>{{end}}</ul>{{end}}
{{if .R.Skipped}}<h2>Skipped</h2><ul>{{range .R.Skipped}}<li>{{.}}</li>{{end}}</ul>{{end}}
</main>
<div id="tip" role="tooltip"></div>
<script>
(() => {
  const tip = document.getElementById("tip");
  document.addEventListener("pointermove", e => {
    const t = e.target.closest && e.target.closest("[data-tip]");
    if (!t) { tip.style.display = "none"; return; }
    tip.textContent = t.getAttribute("data-tip");
    tip.style.display = "block";
    const x = Math.min(e.clientX + 14, window.innerWidth - tip.offsetWidth - 8);
    tip.style.left = x + "px";
    tip.style.top = (e.clientY + 14) + "px";
  });
  for (const b of document.querySelectorAll(".toggle button")) b.addEventListener("click", () => {
    document.querySelectorAll(".toggle button").forEach(x => x.setAttribute("aria-selected", x === b));
    document.querySelectorAll("section.view").forEach(v => v.hidden = v.id !== "view-" + b.dataset.view);
  });
  const num = s => {
    if (s === "–") return -Infinity;
    if (s.startsWith("<")) return 0.00005;
    const v = parseFloat(s.replace(/[^0-9.\-]/g, ""));
    return isNaN(v) ? null : v;
  };
  for (const table of document.querySelectorAll("table.sortable")) {
    table.querySelectorAll("th").forEach((th, i) => th.addEventListener("click", () => {
      const asc = th.getAttribute("aria-sort") !== "ascending";
      table.querySelectorAll("th").forEach(h => h.removeAttribute("aria-sort"));
      th.setAttribute("aria-sort", asc ? "ascending" : "descending");
      const body = table.tBodies[0];
      const rows = [...body.rows];
      rows.sort((a, b) => {
        const x = a.cells[i].textContent.trim(), y = b.cells[i].textContent.trim();
        const nx = num(x), ny = num(y);
        const c = nx !== null && ny !== null ? nx - ny : x.localeCompare(y);
        return asc ? c : -c;
      });
      rows.forEach(r => body.appendChild(r));
    }));
  }
})();
</script>
</body>
</html>
`
