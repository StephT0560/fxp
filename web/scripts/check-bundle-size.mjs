// SPDX-License-Identifier: MIT
// Fails when the gzipped JS/CSS in dist/ exceeds the budget.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

const BUDGET_KB = Number(process.env.FXP_CONSOLE_BUDGET_KB ?? 160);
const dir = new URL("../dist/assets/", import.meta.url).pathname;
let total = 0;
for (const f of readdirSync(dir)) {
  if (!/\.(js|css)$/.test(f)) continue;
  const gz = gzipSync(readFileSync(join(dir, f))).length;
  total += gz;
  console.log(`${f}\t${(statSync(join(dir, f)).size / 1024).toFixed(1)} kB\t${(gz / 1024).toFixed(1)} kB gzip`);
}
console.log(`total ${(total / 1024).toFixed(1)} kB gzip (budget ${BUDGET_KB} kB)`);
if (total > BUDGET_KB * 1024) {
  console.error("bundle exceeds budget");
  process.exit(1);
}
