// SPDX-License-Identifier: MIT
// Keep the initial console budget while accounting for the on-demand chart.
import { readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
const root = new URL("../dist/", import.meta.url);
const manifest = JSON.parse(readFileSync(new URL(".vite/manifest.json", root), "utf8"));
const initial = new Set();
function visit(key) {
  const chunk = manifest[key];
  if (!chunk || initial.has(chunk.file)) return;
  initial.add(chunk.file);
  for (const css of chunk.css ?? []) initial.add(css);
  for (const dep of chunk.imports ?? []) visit(dep);
}
for (const [key, chunk] of Object.entries(manifest)) if (chunk.isEntry) visit(key);
const all = new Set();
for (const c of Object.values(manifest)) { all.add(c.file); for (const css of c.css ?? []) all.add(css); }
let eager = 0, deferred = 0;
for (const file of all) {
  if (!/\.(js|css)$/.test(file)) continue;
  const kb = gzipSync(readFileSync(new URL(file, root))).length / 1024;
  if (initial.has(file)) eager += kb; else deferred += kb;
  console.log(`${file}\t${kb.toFixed(1)} kB gzip\t${initial.has(file) ? "initial" : "deferred"}`);
}
const initialBudget = Number(process.env.FXP_CONSOLE_BUDGET_KB ?? 160);
const deferredBudget = 120;
console.log(`initial ${eager.toFixed(1)}/${initialBudget} kB; deferred ${deferred.toFixed(1)}/${deferredBudget} kB gzip`);
if (eager > initialBudget || deferred > deferredBudget) { console.error("bundle exceeds budget"); process.exit(1); }
