// Hosts: the dialog that lists the herdsman servers this app knows, adds one
// from its pairing link or address, renames, forgets and switches; and the
// connect screen shown when there is no host to talk to yet.
import { h, icon, icons, logoMark, mount } from "../dom.ts";
import {
  blockedReason,
  type Host,
  type Hosts,
  loadHosts,
  normalizeUrl,
  parseLink,
  saveHosts,
  upsert,
} from "../hosts.ts";
import { dismissOnBackdrop, toast } from "../ui.ts";

const pairKey = "herdsman.pending-link";

/**
 * Makes a host current and starts the app over with it. A pairing token
 * rides along in session storage, so it never sits in the address bar.
 */
export function switchTo(hs: Hosts, host: Host, token = ""): void {
  hs.current = host.id;
  // The page starts over from the stored list, so a choice it cannot store
  // would come back as the old host.
  if (!saveHosts(hs)) {
    toast(
      "This browser does not let the app store its hosts, so it cannot switch. Allow site data and try again.",
      "error",
    );
    return;
  }
  try {
    if (token) sessionStorage.setItem(pairKey, JSON.stringify({ url: host.url, token }));
  } catch {
    // Without session storage the host asks for its link again.
  }
  location.assign(location.pathname);
}

/** A pairing token that a switch left for the host it named. */
export function takePendingLink(url: string): string {
  try {
    const v = JSON.parse(sessionStorage.getItem(pairKey) ?? "null") as { url?: string; token?: string } | null;
    sessionStorage.removeItem(pairKey);
    return v?.url === url && v.token ? v.token : "";
  } catch {
    return "";
  }
}

/** The add-a-host form: a pairing link, or a bare address to pair later. */
function addForm(onAdded: () => void): HTMLElement {
  const input = h("input", {
    class: "input",
    name: "host",
    placeholder: "http://127.0.0.1:7878/#t=…  or  192.168.1.20:7878",
    "aria-label": "Pairing link or host address",
    spellcheck: false,
    autocomplete: "off",
  });
  const name = h("input", { class: "input", name: "name", placeholder: "Name (optional)", "aria-label": "Host name" });
  const note = h("p", { class: "hosts-note", role: "status", "aria-live": "polite" });
  const submit = (e: Event): void => {
    e.preventDefault();
    const raw = input.value.trim();
    const link = parseLink(raw, location.origin);
    const url = link?.url ?? normalizeUrl(raw);
    if (!url) {
      note.className = "hosts-note error";
      note.textContent = "That is not a host address or a pairing link.";
      return;
    }
    const hs = loadHosts();
    const was = hs.current;
    const host = upsert(hs, url, name.value);
    if (!link && was) hs.current = was; // an address alone does not switch: the host still needs its link
    if (link) {
      // A device secret the host still knows pairs at once; a stale one asks for a code.
      switchTo(hs, host, link.token);
      return;
    }
    saveHosts(hs);
    input.value = "";
    name.value = "";
    note.className = "hosts-note";
    note.textContent = `Added ${host.name}. Paste its pairing link to pair this browser with it.`;
    onAdded();
  };
  return h(
    "form",
    { class: "hosts-add", onsubmit: submit },
    h("label", { class: "hosts-label" }, "Add a host"),
    h("div", { class: "hosts-row" }, input, name, h("button", { class: "btn primary", type: "submit" }, "Add")),
    note,
    h(
      "p",
      { class: "hosts-help" },
      "Run ",
      h("code", null, "herdsman web"),
      " on the host and paste the link it prints. To reach it from another device, start it with ",
      h("code", null, "--host <LAN or Tailscale IP>"),
      ", or put it behind HTTPS and add ",
      h("code", null, "--allow-host <domain>"),
      ".",
    ),
  );
}

function hostList(redraw: () => void): HTMLElement {
  const hs = loadHosts();
  if (!hs.hosts.length) return h("p", { class: "hosts-empty" }, "No hosts yet.");
  return h(
    "ul",
    { class: "hosts-list" },
    hs.hosts.map((host) => {
      const current = host.id === hs.current;
      const blocked = blockedReason(location.protocol, host.url);
      const nameEl = h("span", { class: "host-name" }, host.name);
      const rename = (): void => {
        const field = h("input", { class: "input host-rename", value: host.name, "aria-label": "Host name" });
        const done = (save: boolean): void => {
          if (save && field.value.trim()) {
            const all = loadHosts();
            const x = all.hosts.find((y) => y.id === host.id);
            if (x) x.name = field.value.trim();
            saveHosts(all);
          }
          redraw();
        };
        field.addEventListener("keydown", (e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            done(true);
          } else if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            done(false);
          }
        });
        field.addEventListener("blur", () => done(true));
        nameEl.replaceWith(field);
        field.focus();
        field.select();
      };
      return h(
        "li",
        { class: current ? "host current" : "host" },
        h(
          "div",
          { class: "host-main" },
          h("div", { class: "host-title" }, nameEl, current ? h("span", { class: "host-chip" }, "Current") : null),
          h("code", { class: "host-url" }, host.url),
          h("span", { class: host.device ? "host-state paired" : "host-state" }, host.device ? "Paired" : "Not paired"),
          blocked ? h("p", { class: "hosts-note error" }, blocked) : null,
        ),
        h(
          "div",
          { class: "host-actions" },
          current
            ? null
            : h("button", { type: "button", class: "btn", onclick: () => switchTo(loadHosts(), host) }, "Connect"),
          h(
            "button",
            {
              type: "button",
              class: "icon-btn",
              "aria-label": `Rename ${host.name}`,
              "data-tip": "Rename",
              onclick: rename,
            },
            icon(icons.tag),
          ),
          h(
            "button",
            {
              type: "button",
              class: "icon-btn",
              "aria-label": `Forget ${host.name}`,
              "data-tip": "Forget",
              onclick: () => {
                const all = loadHosts();
                all.hosts = all.hosts.filter((y) => y.id !== host.id);
                if (all.current === host.id) all.current = all.hosts[0]?.id ?? "";
                saveHosts(all);
                toast(`Forgot ${host.name}`);
                if (current) location.assign(location.pathname);
                else redraw();
              },
            },
            icon(icons.trash),
          ),
        ),
      );
    }),
  );
}

/** The Hosts dialog. */
export function openHosts(): void {
  const list = h("div", { class: "hosts-list-wrap" });
  const redraw = (): void => mount(list, hostList(redraw));
  redraw();
  // The add form submits on its own, so the dialog's close form holds only Done.
  const dlg = h(
    "dialog",
    { class: "dialog hosts" },
    h("h2", null, "Hosts"),
    h(
      "p",
      { class: "hosts-lead" },
      "The herdsman servers this browser talks to. Each one pairs with a code from its terminal.",
    ),
    list,
    addForm(redraw),
    h("form", { method: "dialog", class: "dialog-actions" }, h("button", { value: "ok", class: "btn" }, "Done")),
  );
  dlg.addEventListener("close", () => dlg.remove());
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  dlg.showModal();
}

/**
 * Asks before a link adds a host this browser does not know: any page or
 * artifact can open such a link, and the host gets what you type to agents.
 */
export function confirmHost(root: HTMLElement, url: string): Promise<boolean> {
  return new Promise((resolve) => {
    const answer = (yes: boolean) => () => resolve(yes);
    mount(
      root,
      h(
        "main",
        { class: "pair" },
        h(
          "div",
          { class: "pair-card connect-card" },
          logoMark(),
          h("h1", null, "Add this host?"),
          h("p", { class: "pair-lead" }, "A link asks this app to connect to a host it does not know yet:"),
          h("code", { class: "host-url" }, url),
          h(
            "p",
            { class: "pair-note" },
            icon(icons.server),
            "Add it only if you started herdsman web there. The host sees your agents' screens and what you type to them.",
          ),
          h(
            "div",
            { class: "dialog-actions" },
            h("button", { type: "button", class: "btn", onclick: answer(false) }, "Cancel"),
            h("button", { type: "button", class: "btn primary", onclick: answer(true) }, "Add and pair"),
          ),
        ),
      ),
    );
  });
}

/**
 * The screen for a browser with nothing to show: no host yet, a host that
 * does not know this browser, or one it cannot reach.
 */
export function showConnect(root: HTMLElement, title: string, text: string): void {
  const list = h("div", { class: "hosts-list-wrap" });
  const redraw = (): void => mount(list, hostList(redraw));
  redraw();
  mount(
    root,
    h(
      "main",
      { class: "pair" },
      h(
        "div",
        { class: "pair-card connect-card" },
        logoMark(),
        h("h1", null, title),
        h("p", { class: "pair-lead" }, text),
        addForm(redraw),
        list,
        h(
          "p",
          { class: "pair-note" },
          icon(icons.keyboard),
          "Pairing proves you can see the host's terminal, so a shared or leaked link is not enough to control your agents.",
        ),
      ),
    ),
  );
  root.querySelector<HTMLInputElement>(".hosts-add input")?.focus();
}
