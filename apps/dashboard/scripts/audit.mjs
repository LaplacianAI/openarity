#!/usr/bin/env node
/**
 * npm audit, failing on any high or critical advisory, dev dependencies
 * included, except one accepted in scripts/audit-accepted.json.
 *
 *   node scripts/audit.mjs
 *
 * An advisory is accepted only while nothing can be done about it: each entry
 * names the package's newest release when it was accepted, and the date it is
 * looked at again. A new release of that package, the date passing, or the
 * advisory no longer being reported each fails the audit, so an entry cannot
 * outlive its reason.
 */
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const SERIOUS = new Set(["high", "critical"]);
const here = dirname(fileURLToPath(import.meta.url));

/** Runs npm and returns what it printed; npm audit exits non-zero when it finds anything. */
function npm(...args) {
  const run = spawnSync("npm", args, { cwd: join(here, ".."), encoding: "utf8" });
  if (run.error) throw run.error;
  if (!run.stdout.trim()) throw new Error(`npm ${args.join(" ")}: ${run.stderr.trim()}`);
  return JSON.parse(run.stdout);
}

/** The high and critical advisories found, by GHSA id. */
function serious(report) {
  const found = new Map();
  for (const v of Object.values(report.vulnerabilities ?? {})) {
    for (const via of v.via) {
      if (typeof via === "string" || !SERIOUS.has(via.severity)) continue;
      const id = via.url.split("/").pop();
      found.set(id, { id, package: via.name, title: via.title, url: via.url });
    }
  }
  return found;
}

const accepted = JSON.parse(readFileSync(join(here, "audit-accepted.json"), "utf8"));
const found = serious(npm("audit", "--json"));
const today = new Date().toISOString().slice(0, 10);
const problems = [];

for (const a of accepted) {
  if (!found.has(a.id)) {
    problems.push(`${a.id} is no longer reported: drop it from scripts/audit-accepted.json`);
    continue;
  }
  found.delete(a.id);
  const latest = npm("view", a.package, "version", "--json");
  if (latest !== a.latest) {
    problems.push(
      `${a.id}: ${a.package} ${latest} is out since it was accepted at ${a.latest}; upgrade to it, and drop the entry if that clears it`,
    );
  }
  if (today > a.review_by) {
    problems.push(`${a.id} was to be looked at again by ${a.review_by}`);
  }
}
for (const f of found.values()) {
  problems.push(`${f.id} in ${f.package}: ${f.title} (${f.url})`);
}

if (problems.length > 0) {
  for (const p of problems) console.error(`audit: ${p}`);
  process.exit(1);
}
const held = accepted.map((a) => a.id).join(", ");
process.stdout.write(
  `audit: no high or critical advisory${held ? `, beyond the accepted ${held}` : ""}\n`,
);
