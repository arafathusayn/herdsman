// The whole built app in headless Chrome against the mock API: the frame,
// tabs, the sidebar, the session view, the palette, dialogs, pairing and the
// docs page, plus what keyed views must keep across renders (card nodes and
// focus, the activity feed's scroll position).
import type { Browser, Page } from "puppeteer-core";

import { entry, type Mock } from "./mock.ts";

export type Check = (name: string, ok: boolean, detail?: string) => void;

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Records uncaught errors and console errors (a mock 404 is expected and left out). */
function watch(page: Page, errors: string[]): void {
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("404")) errors.push(m.text());
  });
}

export async function appChecks(browser: Browser, mock: Mock, check: Check): Promise<void> {
  const errors: string[] = [];
  try {
    await frameAndNavigation(browser, mock, check, errors);
    await keptAcrossRenders(browser, mock, check, errors);
    await dialogs(browser, mock, check, errors);
    await pairing(browser, mock, check, errors);
    await linkToNewHost(browser, mock, check, errors);
    await docs(browser, mock, check, errors);
  } catch (e) {
    check("threw", false, e instanceof Error ? (e.stack ?? e.message) : String(e));
  }
  for (const e of errors) check("page error", false, e);
}

async function open(browser: Browser, mock: Mock, errors: string[], path = "/"): Promise<Page> {
  const page = await browser.newPage();
  await page.setViewport({ width: 1300, height: 800 });
  watch(page, errors);
  await page.goto(mock.base + path, { waitUntil: "domcontentloaded" });
  return page;
}

async function frameAndNavigation(browser: Browser, mock: Mock, check: Check, errors: string[]): Promise<void> {
  const page = await open(browser, mock, errors);
  await page.waitForSelector(".route-shell", { timeout: 5000 });
  const frame = await page.evaluate(() => [...document.getElementById("app")!.children].map((e) => e.className));
  check("frame: topbar and body in #app", JSON.stringify(frame) === '["topbar","body"]', JSON.stringify(frame));
  const body = await page.evaluate(() => [...document.querySelector(".body")!.children].map((e) => e.className));
  check("body: sidebar, handle, scrim, main, panel host", body.length === 5, JSON.stringify(body));
  check("crumb", (await page.$eval(".crumb", (e) => e.textContent)) === "auth");
  const navItems = await page.$$eval(".sidebar .nav-item", (es) => es.map((e) => e.textContent));
  check("sidebar links", navItems.length === 4, JSON.stringify(navItems));
  const tabs = await page.$$eval(".tabs .tab", (es) => es.map((e) => e.textContent));
  check("tabs", tabs.length === 7, JSON.stringify(tabs));

  // Tabs switch the body; the tab row stays the same element.
  const row = await page.$(".tabs");
  const pings = mock.activity;
  await page.click('.tabs .tab[title="The route\'s checklist lines"]');
  await page.waitForSelector(".tab-body ul.checklist", { timeout: 2000 });
  check("checklist tab", (await page.$$eval(".checklist li", (es) => es.length)) === 3);
  check("tab row kept", await page.evaluate((r) => r === document.querySelector(".tabs"), row));
  await page.click('.tabs .tab[title="The orchestrator\'s run log, newest first"]');
  await page.waitForSelector(".tab-body ol.timeline", { timeout: 2000 });
  check("run state tab", true);
  check("url follows tab", page.url().includes("tab=log"), page.url());
  // A33: the user's own clicks say the user is here, at most once in 10 s.
  await sleep(100);
  check(
    "two clicks tell the server once that the user is here",
    mock.activity === pings + 1,
    `${pings} -> ${mock.activity}`,
  );

  // A24: the log shows the newest line and pages older ones in place.
  const lines = () => page.$$eval(".tab-body ol.timeline li p", (es) => es.map((e) => e.textContent));
  check("log: the newest line", JSON.stringify(await lines()) === '["started"]', JSON.stringify(await lines()));
  const more = await page.$(".tab-body button.btn.ghost");
  check(
    "log: a button for the earlier lines",
    (await more?.evaluate((e) => e.textContent?.trim())) === "Show 3 earlier lines",
  );
  const list = await page.$(".tab-body ol.timeline");
  await more?.click();
  await page.waitForFunction(() => document.querySelectorAll(".tab-body ol.timeline li").length === 4, {
    timeout: 2000,
  });
  check(
    "log: the earlier lines follow, newest first",
    JSON.stringify(await lines()) === '["started","older 2","older 1","older 0"]',
    JSON.stringify(await lines()),
  );
  check("log: the button is gone at the start", (await page.$(".tab-body button.btn.ghost")) === null);
  check(
    "log: the list stays the same element",
    await page.evaluate((l) => l === document.querySelector(".tab-body ol.timeline"), list),
  );
  // A state push describes the view again: the loaded lines stay.
  mock.setStatus("p1", "idle");
  await sleep(150);
  check("log: loaded lines stay across an update", (await lines()).length === 4, JSON.stringify(await lines()));
  mock.setStatus("p1", "working");
  await sleep(100);

  // Sidebar toggle.
  await page.click(".nav-toggle");
  await sleep(100);
  check("sidebar collapses", await page.evaluate(() => document.body.classList.contains("nav-collapsed")));
  check("toggle aria-expanded", (await page.$eval(".nav-toggle", (e) => e.getAttribute("aria-expanded"))) === "false");
  await page.click(".nav-toggle");
  await sleep(100);

  // The session view.
  await page.click(".sidebar .nav-item"); // All agents
  await page.waitForSelector(".board .group", { timeout: 2000 });
  check("session cards", (await page.$$eval(".board .cards .card", (es) => es.length)) === 2);
  check("session crumb", (await page.$eval(".crumb", (e) => e.textContent)) === "All agents");
  await page.click(".shells summary");
  await page.click(".workspace-row summary");
  await sleep(50);
  check("idle workspace opens", await page.$eval(".workspace-row", (e) => (e as HTMLDetailsElement).open));
  // A route in the sidebar goes back to the route.
  await page.click('.sidebar a[href="?r=auth"]');
  await page.waitForSelector(".route-shell", { timeout: 2000 });
  check("route link", page.url().includes("r=auth"), page.url());

  // The palette.
  await page.click(".palette-trigger");
  await page.waitForSelector("dialog.palette[open]", { timeout: 2000 });
  const focused = await page.evaluate(() => document.activeElement?.getAttribute("aria-label"));
  check("palette focuses its input", focused === "Command palette", String(focused));
  await page.keyboard.type(">togg");
  await sleep(50);
  const rows = await page.$$eval(".palette-row", (es) => es.map((e) => e.textContent));
  check("palette filters", rows.length > 0 && rows.every((r) => /togg/i.test(r ?? "")), JSON.stringify(rows));
  check("palette marks", (await page.$$eval(".palette-row mark", (es) => es.length)) > 0);
  await page.keyboard.press("Escape");
  await sleep(50);
  check("palette closes", (await page.$("dialog.palette")) === null);
  // The app's shortcut is ⌘K on a Mac and Ctrl+K elsewhere (CI runs Linux).
  const mod = (await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)))
    ? "Meta"
    : "Control";
  await page.keyboard.down(mod);
  await page.keyboard.press("k");
  await page.keyboard.up(mod);
  await page.waitForSelector("dialog.palette[open]", { timeout: 2000 });
  await page.keyboard.press("ArrowDown");
  await sleep(30);
  check("arrow moves", (await page.$eval(".palette-row.active", (e) => e.id)) === "pal-1");
  await page.mouse.click(5, 5); // the backdrop
  await sleep(50);
  check("backdrop closes", (await page.$("dialog.palette")) === null);

  // Theme switch in the sidebar foot.
  await page.click(".sidebar .theme-switch button:nth-child(3)");
  await sleep(50);
  check("theme dark", (await page.evaluate(() => document.documentElement.dataset["theme"])) === "dark");
  check(
    "theme button active",
    (await page.$eval('.sidebar .theme-switch button[aria-checked="true"]', (e) => e.className)) === "active",
  );
  await page.close();
}

async function keptAcrossRenders(browser: Browser, mock: Mock, check: Check, errors: string[]): Promise<void> {
  // The Team board: a status change patches the card in place, focus included.
  const page = await open(browser, mock, errors, "/?r=auth");
  const card = '.kanban .card[data-pane="p2"]';
  await page.waitForSelector(card, { timeout: 5000 });
  await page.evaluate((sel) => {
    const el = document.querySelector<HTMLElement>(sel)!;
    (window as unknown as { kept: unknown }).kept = el;
    el.focus();
  }, card);
  mock.setStatus("p2", "idle");
  await page.waitForSelector(`${card}.s-idle`, { timeout: 3000 });
  const after = await page.evaluate((sel) => {
    const el = document.querySelector(sel);
    return { same: el === (window as unknown as { kept: unknown }).kept, focused: document.activeElement === el };
  }, card);
  check("team: a status change keeps the card's node", after.same);
  check("team: a status change keeps the card's focus", after.focused);
  mock.setStatus("p2", "working");

  // j and k move between cards; focus stays on the card through the render the move causes.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  for (const [key, pane] of [
    ["j", "p1"],
    ["j", "p2"],
    ["k", "p1"],
  ] as const) {
    await page.keyboard.press(key);
    await page.waitForSelector(`.kanban .card.selected[data-pane="${pane}"]`, { timeout: 2000 });
    await sleep(50);
    const at = await page.evaluate(() => {
      const a = document.activeElement as HTMLElement | null;
      return a?.classList.contains("card") ? (a.dataset["pane"] ?? "") : (a?.tagName ?? "none");
    });
    check(`team: ${key} focuses the ${pane} card and keeps it focused`, at === pane, at);
  }
  await page.keyboard.press("Escape"); // closes the panel
  await page.waitForFunction(() => !document.body.classList.contains("has-panel"), { timeout: 2000 });

  // The activity feed: a new event keeps what the reader is looking at in place.
  await page.click('.tabs .tab[title="Agent and web events since this page opened"]');
  await page.waitForSelector(".timeline.activity li", { timeout: 2000 });
  const before = await page.evaluate(() => {
    const list = document.querySelector(".timeline.activity")!;
    let s: HTMLElement | null = list.parentElement;
    while (s && !(s.scrollHeight > s.clientHeight && /auto|scroll/.test(getComputedStyle(s).overflowY)))
      s = s.parentElement;
    if (!s) return null;
    s.scrollTop = 800;
    const top = s.getBoundingClientRect().top;
    const anchor = [...list.children].find((li) => li.getBoundingClientRect().top >= top)!;
    (window as unknown as { anchor: unknown }).anchor = anchor;
    return { y: anchor.getBoundingClientRect().top, n: list.children.length, scrolled: s.scrollTop };
  });
  check("activity: the feed scrolls", before !== null && before.scrolled > 0, JSON.stringify(before));
  if (before) {
    mock.push(entry(500, "a new event"));
    await page.waitForFunction((n) => document.querySelector(".timeline.activity")!.children.length > n, {}, before.n);
    const now = await page.evaluate(() => {
      const anchor = (window as unknown as { anchor: Element }).anchor;
      const first = document.querySelector(".timeline.activity li")!;
      return { y: anchor.getBoundingClientRect().top, connected: anchor.isConnected, first: first.textContent ?? "" };
    });
    check("activity: the new event is first", now.first.includes("a new event"), now.first);
    check("activity: a new event keeps the rows' nodes", now.connected);
    check("activity: a new event keeps the scroll position", Math.abs(now.y - before.y) < 1, `${before.y} -> ${now.y}`);
  }
  await page.close();
}

async function dialogs(browser: Browser, mock: Mock, check: Check, errors: string[]): Promise<void> {
  const page = await open(browser, mock, errors, "/?r=auth");
  await page.waitForSelector(".route-shell", { timeout: 5000 });

  // New route: "Use the recommended setup?" comes first, with the setup
  // in one summary (A32).
  mock.catalog = false;
  await page.click(".nav-new");
  await page.waitForSelector("dialog.new-route[open] [name=use-recommended]", { timeout: 2000 });
  const first = await page.$eval("dialog.new-route form", (f) => ({
    question: f.querySelector("p.muted")?.textContent,
    summary: f.querySelectorAll(".nr-summary dt").length,
    answers: [...f.querySelectorAll(".dialog-actions button")].map((b) => b.getAttribute("name")),
  }));
  check(
    "new route: the recommended setup is asked first",
    first.question === "Use the recommended setup?" &&
      first.summary > 0 &&
      JSON.stringify(first.answers) === '["custom-setup","use-recommended"]',
    JSON.stringify(first),
  );
  // Without a catalog the dialog says so and closes.
  await page.click("dialog.new-route [name=use-recommended]");
  await page.waitForSelector("dialog.new-route[open] .error", { timeout: 2000 });
  check("new route failure", true);
  await page.click("dialog.new-route .btn.ghost");
  await sleep(50);
  check("new route closes", (await page.$("dialog.new-route")) === null);

  // With one, the form opens with focus on its first empty field.
  mock.catalog = true;
  await page.click(".nav-new");
  await page.waitForSelector("dialog.new-route[open] [name=use-recommended]", { timeout: 2000 });
  await page.click("dialog.new-route [name=use-recommended]");
  await page.waitForSelector("dialog.new-route[open] [name=start]", { timeout: 2000 });
  await sleep(50);
  const focus = await page.evaluate(() => {
    const dlg = document.querySelector("dialog.new-route")!;
    const fields = [...dlg.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input[name], textarea")].filter(
      (f) => f.type !== "radio" && f.type !== "checkbox" && f.type !== "hidden",
    );
    const first = fields.find((f) => !f.value);
    const a = document.activeElement as HTMLInputElement | null;
    return { first: first?.name ?? "", active: a?.name ?? a?.tagName ?? "none", inside: dlg.contains(a) };
  });
  check(
    "new route: focus lands on the first empty field",
    focus.inside && focus.first !== "" && focus.active === focus.first,
    JSON.stringify(focus),
  );
  check(
    "new route: the recommended summary shows in the form",
    await page.$eval("dialog.new-route [data-field=recommended-summary]", (e) => e.checkVisibility()),
  );
  // A32: a folder is probed for the GitHub account, and Start waits for the pick.
  await page.$eval("dialog.new-route [name=folder]", (e) => {
    const input = e as HTMLInputElement;
    input.value = "/r/repo";
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await page.waitForSelector("dialog.new-route select[name=ghAccount]", { timeout: 2000 });
  const waiting = await page.$eval("dialog.new-route [name=start]", (b) => [
    (b as HTMLButtonElement).disabled,
    b.getAttribute("title"),
  ]);
  check(
    "new route: Start waits for the GitHub account",
    JSON.stringify(waiting) === '[true,"Confirm the GitHub account."]',
    JSON.stringify(waiting),
  );
  await page.select("dialog.new-route select[name=ghAccount]", "user-b");
  check(
    "new route: a picked account opens Start",
    await page.$eval("dialog.new-route [name=start]", (b) => !(b as HTMLButtonElement).disabled),
  );
  await page.keyboard.press("Escape");
  await page.waitForFunction(() => !document.querySelector("dialog.new-route"), { timeout: 2000 });
  check("new route: Escape closes the form", true);

  // A30: with saved picks, "Use your previous choice?" follows, and Yes
  // opens the form from them.
  mock.picks = [
    { role: "orchestrator", runner: "herdr", harness: "claude", model: "fable", effort: "high", route: "", at: "" },
    { role: "adjutant", runner: "herdr", harness: "claude", model: "fable", effort: "low", route: "", at: "" },
  ];
  await page.click(".nav-new");
  await page.waitForSelector("dialog.new-route[open] [name=use-recommended]", { timeout: 2000 });
  await page.click("dialog.new-route [name=use-recommended]");
  await page.waitForSelector("dialog.new-route[open] [name=use-previous]", { timeout: 2000 });
  const picks = await page.$$eval("dialog.new-route .nr-previous-list li", (es) => es.map((e) => e.textContent));
  check(
    "new route: the previous choice is offered",
    JSON.stringify(picks) ===
      '["orchestrator (herdr): claude fable, effort high","adjutant (herdr): claude fable, effort low"]',
    JSON.stringify(picks),
  );
  await page.click("dialog.new-route [name=use-previous]");
  await page.waitForSelector("dialog.new-route[open] [name=start]", { timeout: 2000 });
  const efforts = await page.$$eval("dialog.new-route select[name$=-effort]", (es) =>
    es.map((e) => (e as HTMLSelectElement).value),
  );
  check(
    "new route: the form has each role's pick",
    JSON.stringify(efforts) === '["low","high"]',
    JSON.stringify(efforts),
  );
  check(
    "new route: the form offers the previous choice",
    ((await page.$eval("dialog.new-route .nr-previous", (e) => e.textContent)) ?? "").includes("Previous choice"),
  );
  await page.keyboard.press("Escape");
  await page.waitForFunction(() => !document.querySelector("dialog.new-route"), { timeout: 2000 });
  mock.picks = [];
  mock.catalog = false;

  // Appearance: the theme switch inside the dialog picks a theme and leaves it open.
  await page.click('.sidebar button[aria-label="Appearance"]');
  await page.waitForSelector("dialog.appearance[open]", { timeout: 2000 });
  await page.click("dialog.appearance .theme-switch button:nth-child(2)");
  await sleep(100);
  const app = await page.evaluate(() => ({
    open: document.querySelector<HTMLDialogElement>("dialog.appearance")?.open ?? false,
    theme: document.documentElement.dataset["theme"] ?? "",
    checked:
      document.querySelector("dialog.appearance .theme-switch button:nth-child(2)")?.getAttribute("aria-checked") ?? "",
  }));
  check("appearance: the theme switch keeps the dialog open", app.open, JSON.stringify(app));
  check(
    "appearance: the theme switch picks the theme",
    app.theme === "light" && app.checked === "true",
    JSON.stringify(app),
  );
  await page.click("dialog.appearance .dialog-actions .btn");
  await sleep(50);
  check("appearance: Done closes the dialog", (await page.$("dialog.appearance")) === null);
  await page.close();
}

async function pairing(browser: Browser, mock: Mock, check: Check, errors: string[]): Promise<void> {
  mock.mode = "pairing";
  const page = await open(browser, mock, errors);
  await page.waitForSelector("main.pair .pair-code", { timeout: 5000 });
  check("pair focuses code", (await page.evaluate(() => document.activeElement?.className)) === "pair-code");
  await page.keyboard.type("12a3");
  check("pair formats", (await page.$eval(".pair-code", (e) => (e as HTMLInputElement).value)) === "123");
  check("pair submit disabled", await page.$eval(".pair-submit", (e) => (e as HTMLButtonElement).disabled));
  await page.keyboard.type("456");
  await page
    .waitForFunction(() => document.querySelector("p[role=status]")?.textContent?.includes("tries left"), {
      timeout: 3000,
    })
    .catch(() => {});
  check("pair wrong code", (await page.$eval("p[role=status]", (e) => e.className)) === "pair-message error");
  check("pair cleared", (await page.$eval(".pair-code", (e) => (e as HTMLInputElement).value)) === "");
  mock.mode = "paired";
  await page.close();
}

// A link that names a host this browser does not know asks first. Once
// added, a host that already knows the browser answers "paired", and the
// app shows its frame again in place of the question.
async function linkToNewHost(browser: Browser, mock: Mock, check: Check, errors: string[]): Promise<void> {
  const context = await browser.createBrowserContext(); // a fresh host list
  const page = await context.newPage();
  await page.setViewport({ width: 1300, height: 800 });
  watch(page, errors);
  const token = "0123456789abcdef0123456789abcdef";
  await page.goto(`${mock.base}/#host=${encodeURIComponent(mock.other)}&t=${token}`, {
    waitUntil: "domcontentloaded",
  });
  await page.waitForSelector(".connect-card .host-url", { timeout: 5000 });
  check(
    "link asks before adding a host",
    (await page.$eval(".host-url", (e) => e.textContent ?? "")).startsWith(mock.other),
  );
  await page.click(".connect-card .btn.primary");
  const shown = await page
    .waitForSelector(".route-shell", { timeout: 5000 })
    .then(() => true)
    .catch(() => false);
  const frame = await page.evaluate(() => [...document.getElementById("app")!.children].map((e) => e.className));
  check("added host shows the frame", shown && JSON.stringify(frame) === '["topbar","body"]', JSON.stringify(frame));
  await context.close();
}

async function docs(browser: Browser, mock: Mock, check: Check, errors: string[]): Promise<void> {
  const page = await browser.newPage();
  watch(page, errors);
  await page.goto(mock.base + "/docs/", { waitUntil: "load" });
  const copies = await page.$$("button.copy");
  check("docs copy buttons", copies.length > 0, String(copies.length));
  if (copies[0]) {
    await copies[0].click();
    await sleep(100);
    const label = await page.evaluate((b) => b.textContent, copies[0]);
    check("docs copy label changes", label === "Copied" || label === "Press ⌘C", String(label));
  }
  check("docs theme switch", (await page.$$(".theme-switch button")).length === 3);
  await page.close();
}
