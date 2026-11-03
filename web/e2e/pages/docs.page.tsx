import { check, equal, flush, q, run, typeIn } from "./lib.ts";

await run(async () => {
  const root = q(document, "#app");
  root.innerHTML = `<div id="theme"></div>
    <button class="nav-toggle" aria-expanded="false">Contents</button><div class="scrim" hidden></div>
    <label class="search"><input aria-label="Search docs"></label>
    <nav><div class="nav-group"><a href="#install">Install</a><a href="#pair">Pair</a></div><p class="nav-empty" hidden>None</p></nav>
    <main class="content"><section id="install"><h2>Install</h2><pre><code># A comment\nherdsman web --open</code></pre></section>
      <section id="pair"><h2>Pair</h2><p>Configure pairs with terminal codes.</p></section>
      <div data-tabs><div role="tablist" hidden><button role="tab" aria-selected="true" aria-controls="mac">Mac</button><button role="tab" aria-controls="linux">Linux</button></div>
        <div id="mac"><h3 class="tab-title">Mac install</h3>macOS</div><div id="linux"><h3 class="tab-title">Linux install</h3>Linux</div></div>
    </main>`;
  const copied: string[] = [];
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: {
      writeText: async (text: string) => {
        copied.push(text);
      },
    },
  });
  const { disposeDocs } = await import("../../src/docs.tsx");
  try {
    const dark = q<HTMLButtonElement>(root, '#theme [title="Dark theme"]');
    dark.click();
    equal("theme changes through the shared component", document.documentElement.dataset.theme, "dark");
    check(
      "theme buttons retain their nodes",
      q(root, '#theme [title="Dark theme"]') === dark && dark.getAttribute("aria-checked") === "true",
    );
    q<HTMLButtonElement>(root, ".copy").click();
    await flush();
    equal("copy excludes command comments", copied, ["herdsman web --open"]);
    equal("copy feedback updates", q(root, ".copy span").textContent, "Copied");
    equal("section links are native anchors", q<HTMLAnchorElement>(root, "#install .anchor").hash, "#install");
    const toggle = q<HTMLButtonElement>(root, ".nav-toggle");
    toggle.click();
    check(
      "menu signal updates visibility and ARIA",
      document.body.classList.contains("nav-open") &&
        toggle.getAttribute("aria-expanded") === "true" &&
        !q<HTMLElement>(root, ".scrim").hidden,
    );
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    check(
      "Escape closes the menu and restores focus",
      !document.body.classList.contains("nav-open") && document.activeElement === toggle,
    );
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true }));
    const search = q<HTMLInputElement>(root, ".search input");
    check(
      "slash opens and focuses contents search",
      document.body.classList.contains("nav-open") && document.activeElement === search,
    );
    typeIn(search, "terminal codes");
    const links = [...root.querySelectorAll<HTMLAnchorElement>(".nav-group a")];
    equal(
      "filter matches section text",
      links.map((link) => link.hidden),
      [true, false],
    );
    typeIn(search, "nothing matches");
    check(
      "no-match state hides the empty group",
      q<HTMLElement>(root, ".nav-group").hidden && !q<HTMLElement>(root, ".nav-empty").hidden,
    );
    search.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    check("Escape resets filtering", search.value === "" && links.every((link) => !link.hidden));
    const tabs = [...root.querySelectorAll<HTMLButtonElement>('[role="tab"]')];
    tabs[0]!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    check(
      "arrow keys select and focus the next tab",
      tabs[1]!.getAttribute("aria-selected") === "true" && document.activeElement === tabs[1],
    );
    check(
      "tabs expose only the selected panel",
      q<HTMLElement>(root, "#mac").hidden && !q<HTMLElement>(root, "#linux").hidden,
    );
    tabs[1]!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    check("tab navigation wraps", tabs[0]!.getAttribute("aria-selected") === "true");
  } finally {
    disposeDocs();
  }
  check("disposal removes component controls", root.querySelector(".copy, .anchor, .theme-switch") === null);
  q<HTMLButtonElement>(root, ".nav-toggle").click();
  check("disposal removes menu listeners", !document.body.classList.contains("nav-open"));
});
