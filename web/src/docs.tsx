// Static documentation remains readable without JavaScript. Its interactive controls are owned components.
import { attach, effect, onCleanup } from "@reactre/core";
import { createActor } from "@reactre/state";

import { Icon } from "./components/icons.tsx";
import { ThemeSwitch } from "./components/theme-switch.tsx";
import { icons } from "./icons.ts";
import { copyMachine } from "./state/copy.machine.ts";
import { observeActor } from "./state/observe.ts";
import { registerRequest } from "./state/request-service.ts";
import { valueState } from "./state/value.ts";
import { applyTheme, loadTheme } from "./ui.ts";

function listen<T extends Event>(
  target: EventTarget | null,
  type: string,
  handler: (event: T) => void,
  options?: AddEventListenerOptions,
): void {
  const listener = handler as EventListener;
  target?.addEventListener(type, listener, options);
  onCleanup(() => target?.removeEventListener(type, listener, options));
}

function setupTheme(): void {
  const [theme, setTheme] = valueState(loadTheme());
  applyTheme(theme());
  const slot = document.getElementById("theme");
  if (!slot) return;
  onCleanup(
    attach(
      () => (
        <ThemeSwitch
          current={theme()}
          choose={(theme) => {
            applyTheme(theme);
            setTheme(theme);
          }}
        />
      ),
      slot,
    ),
  );
}

/** Plain HTTP pages can still copy through a temporary, owned text field. */
async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    let field!: HTMLTextAreaElement;
    const dispose = attach(
      () => (
        <textarea
          defaultValue={text}
          readonly
          style={{ position: "fixed", opacity: "0" }}
          ref={(element) => {
            field = element;
          }}
        />
      ),
      document.body,
    );
    try {
      field.select();
      return document.execCommand("copy");
    } catch {
      return false;
    } finally {
      dispose();
    }
  }
}

function CopyCommands(props: { code: HTMLElement }) {
  const service = registerRequest(async (text) => String(await copyText(text)));
  const actor = createActor(copyMachine, { input: { service: service.id } });
  const snapshot = observeActor(actor).read;
  onCleanup(service.dispose);
  const state = () => {
    const current = snapshot();
    const done = current.state === "shown" && current.data.ok;
    return { done, label: current.state === "shown" ? (done ? "Copied" : "Press ⌘C") : "Copy" };
  };
  const copy = (): void => {
    const text = (props.code.textContent ?? "")
      .split("\n")
      .filter((line) => !line.trimStart().startsWith("# "))
      .join("\n")
      .replace(/\n{3,}/g, "\n\n")
      .trim();
    actor.send({ type: "copy", text });
  };
  return (
    <button
      type="button"
      class={state().done ? "copy done" : "copy"}
      aria-label="Copy the commands"
      onClick={() => void copy()}
    >
      <Icon paths={icons.copy} />
      <span>{state().label}</span>
    </button>
  );
}

function setupCopy(): void {
  for (const pre of document.querySelectorAll<HTMLPreElement>(".content pre")) {
    if (pre.closest(".hero-demo")) continue;
    const code = pre.querySelector("code");
    if (!code) continue;
    pre.classList.add("has-copy");
    onCleanup(attach(() => <CopyCommands code={code} />, pre));
  }
}

function setupAnchors(): void {
  for (const heading of document.querySelectorAll<HTMLElement>(".content h2, .content h3")) {
    const id = heading.id || heading.closest("section")?.id;
    if (!id || heading.closest(".role")) continue;
    const label = `Link to ${heading.textContent ?? "this section"}`;
    onCleanup(
      attach(
        () => (
          <a class="anchor" href={`#${id}`} aria-label={label}>
            #
          </a>
        ),
        heading,
      ),
    );
  }
}

type Menu = { open: () => void; close: () => void };
function setupMenu(): Menu {
  const [open, setOpen] = valueState(false);
  const toggle = document.querySelector<HTMLButtonElement>(".nav-toggle");
  const scrim = document.querySelector<HTMLElement>(".scrim");
  effect(() => {
    document.body.classList.toggle("nav-open", open());
    if (scrim) scrim.hidden = !open();
    toggle?.setAttribute("aria-expanded", String(open()));
    toggle?.setAttribute("aria-label", open() ? "Hide the contents" : "Show the contents");
  });
  listen(toggle, "click", () => setOpen((open) => !open));
  listen(scrim, "click", () => setOpen(false));
  listen<KeyboardEvent>(document, "keydown", (event) => {
    if (event.key === "Escape" && open()) {
      setOpen(false);
      toggle?.focus();
    }
  });
  return { open: () => setOpen(true), close: () => setOpen(false) };
}

function setupNav(menu: Menu): void {
  const links = [...document.querySelectorAll<HTMLAnchorElement>(".nav-group a")];
  for (const link of links) listen(link, "click", () => menu.close());
  const input = document.querySelector<HTMLInputElement>(".search input");
  const empty = document.querySelector<HTMLElement>(".nav-empty");
  const groups = [...document.querySelectorAll<HTMLElement>(".nav-group")];
  const text = new Map(
    links.map((link) => {
      const section = document.getElementById(link.hash.slice(1));
      return [link, `${link.textContent ?? ""} ${section?.textContent ?? ""}`.toLowerCase()];
    }),
  );
  const [query, setQuery] = valueState(input?.value ?? "");
  effect(() => {
    const words = query().toLowerCase().split(/\s+/).filter(Boolean);
    let shown = 0;
    for (const link of links) {
      const hit = words.every((word) => text.get(link)?.includes(word));
      link.hidden = !hit;
      if (hit) shown++;
    }
    for (const group of groups) group.hidden = ![...group.querySelectorAll("a")].some((link) => !link.hidden);
    if (empty) empty.hidden = shown > 0;
  });
  listen(input, "input", () => setQuery(input?.value ?? ""));
  listen<KeyboardEvent>(input, "keydown", (event) => {
    if (event.key === "Enter") {
      links.find((link) => !link.hidden)?.click();
      input?.blur();
    } else if (event.key === "Escape") {
      if (input) input.value = "";
      setQuery("");
      input?.blur();
    }
  });
  listen<KeyboardEvent>(document, "keydown", (event) => {
    const typing = event.target instanceof Element && event.target.closest("input, textarea, [contenteditable]");
    if (event.key === "/" && !typing && !event.metaKey && !event.ctrlKey && !event.altKey) {
      event.preventDefault();
      if (getComputedStyle(document.querySelector(".nav-toggle") ?? document.body).display !== "none") menu.open();
      input?.focus();
    }
  });
  const sections = links
    .map((link) => document.getElementById(link.hash.slice(1)))
    .filter((section): section is HTMLElement => section !== null);
  const [current, setCurrent] = valueState<string | undefined>(undefined);
  const mark = (): void => {
    let current = sections[0];
    for (const section of sections) if (section.getBoundingClientRect().top <= 96) current = section;
    if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 4) current = sections.at(-1);
    setCurrent(current?.id);
  };
  effect(() => {
    const id = current();
    for (const link of links) {
      const active = link.hash === `#${id}`;
      link.classList.toggle("active", active);
      if (active) link.setAttribute("aria-current", "location");
      else link.removeAttribute("aria-current");
    }
  });
  let frame = 0;
  listen(
    window,
    "scroll",
    () => {
      if (frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        mark();
      });
    },
    { passive: true },
  );
  onCleanup(() => cancelAnimationFrame(frame));
  mark();
}

/** ARIA tabs on the static page project an actor-owned selected index. */
function setupTabs(): void {
  for (const box of document.querySelectorAll<HTMLElement>("[data-tabs]")) {
    const tabs = [...box.querySelectorAll<HTMLButtonElement>('[role="tab"]')];
    const [selected, select] = valueState(
      Math.max(
        0,
        tabs.findIndex((tab) => tab.getAttribute("aria-selected") === "true"),
      ),
    );
    effect(() => {
      const current = tabs[selected()];
      for (const tab of tabs) {
        const active = tab === current;
        tab.setAttribute("aria-selected", String(active));
        tab.tabIndex = active ? 0 : -1;
        const panel = document.getElementById(tab.getAttribute("aria-controls") ?? "");
        if (panel) panel.hidden = !active;
      }
    });
    const list = box.querySelector<HTMLElement>('[role="tablist"]');
    if (list) list.hidden = false;
    for (const title of box.querySelectorAll<HTMLElement>(".tab-title")) title.hidden = true;
    tabs.forEach((tab, index) => {
      listen(tab, "click", () => select(index));
      listen<KeyboardEvent>(tab, "keydown", (event) => {
        const step = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
        if (!step) return;
        event.preventDefault();
        const next = tabs[(index + step + tabs.length) % tabs.length];
        if (next) {
          select((index + step + tabs.length) % tabs.length);
          next.focus();
        }
      });
    });
  }
}

export function enhanceDocs(): () => void {
  return attach(() => {
    setupTheme();
    setupCopy();
    setupAnchors();
    setupTabs();
    setupNav(setupMenu());
    return null;
  }, document.body);
}
export const disposeDocs = enhanceDocs();
