// Finds a Chrome or Chromium for the benchmark and the SVG check: $CHROME,
// else the usual install places on macOS and Linux, else the first of the
// usual names on PATH. Puppeteer starts its own process with a fresh
// profile, never the user's open browser.
import { existsSync } from "node:fs";

const places = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/Applications/Chromium.app/Contents/MacOS/Chromium",
  "/usr/bin/google-chrome",
  "/usr/bin/google-chrome-stable",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
];
const names = ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"];

/** The browser to launch; the arguments let tests stand in for the machine. */
export function findChrome(
  env: Record<string, string | undefined> = process.env,
  exists: (path: string) => boolean = existsSync,
  which: (name: string) => string | null = Bun.which,
): string {
  const set = env["CHROME"];
  if (set) return set;
  for (const p of places) if (exists(p)) return p;
  for (const n of names) {
    const p = which(n);
    if (p) return p;
  }
  throw new Error("No Chrome found: install Chrome or Chromium, or set CHROME to its executable.");
}
