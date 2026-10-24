// The docs page at /docs/: the page itself is static HTML; this adds the
// theme switch (shared with the app), copy buttons, the contents filter, the
// current section in the contents, the install tabs and the narrow-screen menu.
import { icon, icons, mount } from "./dom.ts";
import { applyTheme, loadTheme, themeSwitch } from "./ui.ts";

function setupTheme(): void {
  applyTheme(loadTheme());
  const slot = document.getElementById("theme");
  if (!slot) return;
  const draw = (): void => {
    mount(
      slot,
      themeSwitch(loadTheme(), (t) => {
        applyTheme(t);
        draw();
      }),
    );
  };
  draw();
}

/** Copies text, with a fallback for plain-HTTP pages where the clipboard API is missing. */
async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.append(area);
    area.select();
    const ok = document.execCommand("copy");
    area.remove();
    return ok;
  }
}

function setupCopy(): void {
  for (const pre of document.querySelectorAll<HTMLPreElement>(".content pre")) {
    if (pre.closest(".hero-demo")) continue;
    const code = pre.querySelector("code");
    if (!code) continue;
    const label = document.createElement("span");
    label.textContent = "Copy";
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "copy";
    btn.setAttribute("aria-label", "Copy the commands");
    btn.append(icon(icons.copy), label);
    let timer = 0;
    btn.addEventListener("click", async () => {
      // Comment lines are for reading; the copy holds only the commands.
      const text = (code.textContent ?? "")
        .split("\n")
        .filter((l) => !l.trimStart().startsWith("# "))
        .join("\n")
        .replace(/\n{3,}/g, "\n\n")
        .trim();
      const ok = await copyText(text);
      label.textContent = ok ? "Copied" : "Press ⌘C";
      btn.classList.toggle("done", ok);
      clearTimeout(timer);
      timer = window.setTimeout(() => {
        label.textContent = "Copy";
        btn.classList.remove("done");
      }, 1600);
    });
    pre.classList.add("has-copy");
    pre.append(btn);
  }
}

function setupAnchors(): void {
  for (const hd of document.querySelectorAll<HTMLElement>(".content h2, .content h3")) {
    const id = hd.id || hd.closest("section")?.id;
    if (!id || hd.closest(".role")) continue;
    const a = document.createElement("a");
    a.className = "anchor";
    a.href = `#${id}`;
    a.textContent = "#";
    a.setAttribute("aria-label", `Link to ${hd.textContent ?? "this section"}`);
    hd.append(a);
  }
}

function setupMenu(): { close: () => void } {
  const toggle = document.querySelector<HTMLButtonElement>(".nav-toggle");
  const scrim = document.querySelector<HTMLElement>(".scrim");
  const set = (open: boolean): void => {
    document.body.classList.toggle("nav-open", open);
    if (scrim) scrim.hidden = !open;
    toggle?.setAttribute("aria-expanded", String(open));
    toggle?.setAttribute("aria-label", open ? "Hide the contents" : "Show the contents");
  };
  toggle?.addEventListener("click", () => set(!document.body.classList.contains("nav-open")));
  scrim?.addEventListener("click", () => set(false));
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && document.body.classList.contains("nav-open")) {
      set(false);
      toggle?.focus();
    }
  });
  return { close: () => set(false) };
}

function setupNav(menu: { close: () => void }): void {
  const links = [...document.querySelectorAll<HTMLAnchorElement>(".nav-group a")];
  for (const a of links) a.addEventListener("click", () => menu.close());

  // The contents filter matches a section's name and its text.
  const input = document.querySelector<HTMLInputElement>(".search input");
  const empty = document.querySelector<HTMLElement>(".nav-empty");
  const text = new Map(
    links.map((a) => {
      const sec = document.getElementById(a.hash.slice(1));
      return [a, `${a.textContent ?? ""} ${sec?.textContent ?? ""}`.toLowerCase()];
    }),
  );
  input?.addEventListener("input", () => {
    const words = input.value.toLowerCase().split(/\s+/).filter(Boolean);
    let shown = 0;
    for (const a of links) {
      const hit = words.every((w) => text.get(a)?.includes(w));
      a.hidden = !hit;
      if (hit) shown++;
    }
    for (const g of document.querySelectorAll<HTMLElement>(".nav-group")) {
      g.hidden = ![...g.querySelectorAll("a")].some((a) => !a.hidden);
    }
    if (empty) empty.hidden = shown > 0;
  });
  input?.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      links.find((a) => !a.hidden)?.click();
      input.blur();
    } else if (e.key === "Escape") {
      input.value = "";
      input.dispatchEvent(new Event("input"));
      input.blur();
    }
  });
  document.addEventListener("keydown", (e) => {
    const t = e.target as HTMLElement | null;
    const typing = t?.closest("input, textarea, [contenteditable]");
    if (e.key === "/" && !typing && !e.metaKey && !e.ctrlKey && !e.altKey) {
      e.preventDefault();
      document.body.classList.add("nav-open");
      document.querySelector<HTMLElement>(".scrim")?.removeAttribute("hidden");
      input?.focus();
    }
  });

  // The contents mark the section at the top of the window.
  const sections = links
    .map((a) => document.getElementById(a.hash.slice(1)))
    .filter((s): s is HTMLElement => s !== null);
  const mark = (): void => {
    const line = 96;
    let current = sections[0];
    for (const s of sections) if (s.getBoundingClientRect().top <= line) current = s;
    // At the end of the page the last short sections can never reach the line.
    if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 4) current = sections.at(-1);
    for (const a of links) {
      const on = a.hash === `#${current?.id}`;
      a.classList.toggle("active", on);
      if (on) a.setAttribute("aria-current", "location");
      else a.removeAttribute("aria-current");
    }
  };
  let queued = false;
  window.addEventListener(
    "scroll",
    () => {
      if (queued) return;
      queued = true;
      requestAnimationFrame(() => {
        queued = false;
        mark();
      });
    },
    { passive: true },
  );
  mark();
}

/** Tabs that follow the ARIA pattern: arrow keys move between them. */
function setupTabs(): void {
  for (const box of document.querySelectorAll<HTMLElement>("[data-tabs]")) {
    const tabs = [...box.querySelectorAll<HTMLButtonElement>('[role="tab"]')];
    const select = (tab: HTMLButtonElement): void => {
      for (const t of tabs) {
        const on = t === tab;
        t.setAttribute("aria-selected", String(on));
        t.tabIndex = on ? 0 : -1;
        const panel = document.getElementById(t.getAttribute("aria-controls") ?? "");
        if (panel) panel.hidden = !on;
      }
    };
    tabs.forEach((t, i) => {
      t.addEventListener("click", () => select(t));
      t.addEventListener("keydown", (e) => {
        const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
        if (!step) return;
        e.preventDefault();
        const next = tabs[(i + step + tabs.length) % tabs.length];
        if (next) {
          select(next);
          next.focus();
        }
      });
    });
  }
}

setupTheme();
setupCopy();
setupAnchors();
setupTabs();
setupNav(setupMenu());
