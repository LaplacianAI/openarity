#!/usr/bin/env node
/**
 * Screenshots of every dashboard screen, in light and dark, at 1440, 768 and
 * 390 pixels: the evidence the redesign is judged on, before and after.
 *
 *   CAPTURE_BASE_URL=https://host node scripts/capture.mjs --login
 *   CAPTURE_BASE_URL=https://host node scripts/capture.mjs before
 *
 * --login opens Chrome, waits while you sign in, and keeps the session in
 * ~/.openarity/capture-state.json (mode 600). A capture puts that session in
 * each page before the app reads it, and writes back a renewed one. Shots go
 * to .captures/<label>/, which git ignores.
 *
 * Dev only. It drives the Chrome already installed; nothing here is bundled.
 */
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { createInterface } from "node:readline/promises";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

import {
  checkLabel,
  fileFor,
  resolveBase,
  SCREENS,
  shotsOf,
  THEMES,
  WIDTHS,
} from "./capture/plan.mjs";

const SESSION_KEY = "openarity.session";
const THEME_KEY = "openarity.theme";
const STATE = join(homedir(), ".openarity", "capture-state.json");
const OUT = join(dirname(fileURLToPath(import.meta.url)), "..", ".captures");
const HEIGHT = 900;

function readSession() {
  try {
    return readFileSync(STATE, "utf8");
  } catch {
    throw new Error("no saved session: run with --login first");
  }
}

function writeSession(raw) {
  mkdirSync(dirname(STATE), { recursive: true, mode: 0o700 });
  writeFileSync(STATE, raw, { mode: 0o600 });
  chmodSync(STATE, 0o600);
}

async function login(base) {
  const browser = await chromium.launch({ channel: "chrome", headless: false });
  try {
    const page = await browser.newPage();
    await page.goto(`${base}/ui`);
    const rl = createInterface({ input: process.stdin, output: process.stdout });
    await rl.question("Sign in in the Chrome window, then press Enter here. ");
    rl.close();
    const raw = await page.evaluate((key) => window.sessionStorage.getItem(key), SESSION_KEY);
    if (!raw)
      throw new Error("no session in the page yet: finish signing in, then run --login again");
    writeSession(raw);
    console.warn(`Saved the session to ${STATE}.`);
  } finally {
    await browser.close();
  }
}

/**
 * Waits for a screen to finish loading. After a click inside the app the
 * network may already count as idle, so that wait returns at once; the
 * skeletons a screen shows while it fetches are what says it is done.
 */
async function settle(page) {
  await page.waitForLoadState("networkidle", { timeout: 15_000 }).catch(() => {});
  await page
    .waitForFunction(() => !document.querySelector('[data-slot="skeleton"]'), null, {
      timeout: 15_000,
    })
    .catch(() => {});
}

async function shoot(page, file) {
  const path = join(OUT, file);
  mkdirSync(dirname(path), { recursive: true });
  await page.screenshot({ path, fullPage: true, animations: "disabled" });
}

async function captureScreen(page, base, label, screen, theme, width, report) {
  await page.goto(`${base}${screen.path}`);
  await settle(page);

  if (screen.devOnly && (await page.getByText(/not found/i).count()) > 0) {
    report.skipped.push({ screen: screen.name, theme, width, why: "not on this server" });
    return;
  }
  if (screen.follow) {
    const links = page.locator(`a[href*="${screen.follow}"]`);
    const named = screen.followName
      ? links.filter({ has: page.getByText(screen.followName, { exact: true }) })
      : links;
    const link = ((await named.count()) > 0 ? named : links).first();
    if ((await link.count()) === 0) {
      report.skipped.push({ screen: screen.name, theme, width, why: "nothing to open" });
      return;
    }
    const from = page.url();
    await link.click();
    await page.waitForURL((url) => url.href !== from, { timeout: 15_000 });
    // The router keeps the old screen up until the new one has loaded.
    await link.waitFor({ state: "detached", timeout: 15_000 });
    await settle(page);
  }

  for (const shot of shotsOf(screen)) {
    const file = fileFor(label, screen.name, shot.variant, theme, width);
    if (shot.tab) {
      await page.getByRole("tab", { name: new RegExp(`^${shot.tab}`) }).click();
      await settle(page);
    }
    if (shot.open) {
      const button = page.getByRole("button", { name: shot.open, exact: true });
      if ((await button.count()) === 0) {
        report.skipped.push({
          screen: screen.name,
          variant: shot.variant,
          theme,
          width,
          why: "not offered",
        });
        continue;
      }
      await button.first().click();
      await page.getByRole("dialog").waitFor({ timeout: 5_000 });
    }
    await shoot(page, file);
    report.taken.push(file);
    if (shot.open) await page.keyboard.press("Escape");
  }
}

/**
 * One context per screen, seeded with the newest session. A provider that
 * rotates refresh tokens refuses the old one once a page has renewed it, so
 * every page hands back what it holds before it closes.
 */
async function capture(base, label) {
  let session = readSession();
  const report = { label, taken: [], skipped: [], failed: [] };
  const browser = await chromium.launch({ channel: "chrome", headless: true });

  try {
    for (const theme of THEMES) {
      for (const width of WIDTHS) {
        for (const screen of SCREENS) {
          const raw = screen.signedOut ? null : session;
          const context = await browser.newContext({
            viewport: { width, height: HEIGHT },
            colorScheme: theme,
            reducedMotion: "reduce",
          });
          await context.addInitScript(
            ({ sessionKey, themeKey, raw, theme }) => {
              window.localStorage.setItem(themeKey, theme);
              if (raw && !window.sessionStorage.getItem(sessionKey)) {
                window.sessionStorage.setItem(sessionKey, raw);
              }
            },
            { sessionKey: SESSION_KEY, themeKey: THEME_KEY, raw, theme },
          );
          const page = await context.newPage();
          try {
            await captureScreen(page, base, label, screen, theme, width, report);
          } catch (err) {
            const why = String(err?.message ?? err)
              .split("\n")[0]
              .replaceAll(base, "<base>");
            report.failed.push({ screen: screen.name, theme, width, why });
          }
          if (raw) {
            const held = await page
              .evaluate((key) => window.sessionStorage.getItem(key), SESSION_KEY)
              .catch(() => null);
            if (held && held !== session) {
              session = held;
              writeSession(held);
            }
          }
          await context.close();
        }
      }
    }
  } finally {
    await browser.close();
  }

  mkdirSync(join(OUT, label), { recursive: true });
  writeFileSync(join(OUT, label, "manifest.json"), `${JSON.stringify(report, null, 2)}\n`);
  console.warn(
    `${report.taken.length} shots in .captures/${label}/, ${report.skipped.length} skipped, ${report.failed.length} failed.`,
  );
  for (const f of report.failed)
    console.warn(`  failed: ${f.screen} ${f.theme} ${f.width}: ${f.why}`);
  if (report.failed.length > 0) process.exitCode = 1;
}

async function main() {
  const [arg] = process.argv.slice(2);
  const base = resolveBase(process.env);
  if (arg === "--login") return login(base);
  if (!arg) throw new Error("name the capture, such as before, or run --login first");
  return capture(base, checkLabel(arg));
}

main().catch((err) => {
  console.error(err.message);
  process.exit(1);
});
