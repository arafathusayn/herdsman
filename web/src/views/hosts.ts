// Hosts: the dialog that lists the herdsman servers this app knows, adds one
// from its pairing link or address, renames, forgets and switches; and the
// connect screen shown when there is no host to talk to yet.
import { icon, type IconNode, icons, logoMark, type LogoNode, mountElement } from "../dom.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  prop,
  render,
  renderDirect,
  text,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
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
  keepPendingLink(host.url, token);
  location.assign(location.pathname);
}

/**
 * Keeps a pairing token for its host until the host answers: across a
 * switch, or a reload after the host could not be reached.
 */
export function keepPendingLink(url: string, token: string): void {
  try {
    if (token) sessionStorage.setItem(pairKey, JSON.stringify({ url, token }));
  } catch {
    // Without session storage the host asks for its link again.
  }
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

/**
 * What the host list and the add form keep between renders of one dialog or
 * connect screen: the add form's fields and note, and the host being renamed
 * with its field, an element the rename owns.
 */
export interface HostsState {
  host: string;
  name: string;
  note: string;
  noteError: boolean;
  renaming: { id: string; field: HTMLInputElement } | null;
}

export function hostsState(): HostsState {
  return { host: "", name: "", note: "", noteError: false, renaming: null };
}

// Prose is in text holes: the formatter wraps text in the markup, and a
// wrapped line keeps its line break and indent as text. For the same reason
// text that ends an element is a hole when the formatter may break after it.
const HostsHelp = html`<p class="hosts-help">
  ${text()}<code>herdsman web</code>${text()}<code>${text()}</code>${text()}<code>${text()}</code>${text()}
</p>`;

/** The add-a-host form: a pairing link, or a bare address to pair later. */
const AddForm = html`<form class="hosts-add" onsubmit=${on<"submit", HTMLFormElement>()}>
  <label class="hosts-label">Add a host</label>
  <div class="hosts-row">
    <input
      class="input"
      name="host"
      placeholder=${attr()}
      aria-label="Pairing link or host address"
      autocomplete="off"
      .value=${prop<HTMLInputElement, "value">()}
      oninput=${on<"input", HTMLInputElement>()}
    />
    <input
      class="input"
      name="name"
      placeholder="Name (optional)"
      aria-label="Host name"
      .value=${prop<HTMLInputElement, "value">()}
      oninput=${on<"input", HTMLInputElement>()}
    />
    <button class="btn primary" type="submit">Add</button>
  </div>
  <p class=${cls()} role="status" aria-live="polite">${text()}</p>
  ${child<ViewNodeOf<typeof HostsHelp>>()}
</form>`;

/** What `addForm` returns. */
export type AddFormNode = ViewNodeOf<typeof AddForm>;

function addForm(s: HostsState, redraw: () => void): AddFormNode {
  const submit = (e: SubmitEvent): void => {
    e.preventDefault();
    const raw = s.host.trim();
    const link = parseLink(raw, location.origin);
    const url = link?.url ?? normalizeUrl(raw);
    if (!url) {
      s.noteError = true;
      s.note = "That is not a host address or a pairing link.";
      redraw();
      return;
    }
    const hs = loadHosts();
    const was = hs.current;
    const host = upsert(hs, url, s.name);
    if (!link && was) hs.current = was; // an address alone does not switch: the host still needs its link
    if (link) {
      // A device secret the host still knows pairs at once; a stale one asks for a code.
      switchTo(hs, host, link.token);
      return;
    }
    saveHosts(hs);
    s.host = "";
    s.name = "";
    s.noteError = false;
    s.note = `Added ${host.name}. Paste its pairing link to pair this browser with it.`;
    redraw();
  };
  return AddForm([
    submit,
    "http://127.0.0.1:7878/#t=…  or  192.168.1.20:7878",
    s.host,
    function () {
      s.host = this.value;
      redraw();
    },
    s.name,
    function () {
      s.name = this.value;
      redraw();
    },
    s.noteError ? "hosts-note error" : "hosts-note",
    s.note,
    HostsHelp([
      "Run ",
      " on the host and paste the link it prints. To reach it from another device, start it with ",
      "--host <LAN or Tailscale IP>",
      ", or put it behind HTTPS and add ",
      "--allow-host <domain>",
      ".",
    ]),
  ]);
}

const HostName = html`<span class="host-name">${text()}</span>`;
const HostChip = html`<span class="host-chip">Current</span>`;
const HostBlocked = html`<p class="hosts-note error">${text()}</p>`;
const HostConnect = html`<button type="button" class="btn" onclick=${on<"click">()}>Connect</button>`;
const RenameField = html`<input
  class="input host-rename"
  .value=${prop<HTMLInputElement, "value">()}
  aria-label="Host name"
  onkeydown=${on<"keydown", HTMLInputElement>()}
  onblur=${on<"blur", HTMLInputElement>()}
/>`;
const HostRow = html`<li class=${cls()}>
  <div class="host-main">
    <div class="host-title">
      ${child<ViewNodeOf<typeof HostName> | HTMLInputElement>()}${child<ViewNodeOf<typeof HostChip> | null>()}
    </div>
    <code class="host-url">${text()}</code>
    <span class=${cls()}>${text()}</span>
    ${child<ViewNodeOf<typeof HostBlocked> | null>()}
  </div>
  <div class="host-actions">
    ${child<ViewNodeOf<typeof HostConnect> | null>()}
    <button type="button" class="icon-btn" aria-label=${attr()} data-tip="Rename" onclick=${on<"click">()}>
      ${child<IconNode>()}
    </button>
    <button type="button" class="icon-btn" aria-label=${attr()} data-tip="Forget" onclick=${on<"click">()}>
      ${child<IconNode>()}
    </button>
  </div>
</li>`;
const HostList = html`<ul class="hosts-list">
  ${child<ListNode<ViewNodeOf<typeof HostRow>, string>>()}
</ul>`;
const NoHosts = html`<p class="hosts-empty">No hosts yet.</p>`;

/** What `hostList` returns. */
export type HostListNode = ViewNodeOf<typeof HostList> | ViewNodeOf<typeof NoHosts>;

function hostList(s: HostsState, redraw: () => void): HostListNode {
  const hs = loadHosts();
  if (!hs.hosts.length) return NoHosts([]);
  const rename = (host: Host): void => {
    if (s.renaming?.id === host.id) return;
    const done = (save: boolean): void => {
      // Enter, Escape and the blur that follows them finish the rename once.
      if (s.renaming?.field !== field) return;
      if (save && field.value.trim()) {
        const all = loadHosts();
        const x = all.hosts.find((y) => y.id === host.id);
        if (x) x.name = field.value.trim();
        saveHosts(all);
      }
      s.renaming = null;
      redraw();
    };
    const field = mountElement("input", RenameField, [
      host.name,
      (e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          done(true);
        } else if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          done(false);
        }
      },
      () => done(true),
    ]);
    s.renaming = { id: host.id, field };
    redraw();
    field.focus();
    field.select();
  };
  return HostList([
    list(
      hs.hosts,
      (host) => host.id,
      (host) => {
        const current = host.id === hs.current;
        const blocked = blockedReason(location.protocol, host.url);
        return HostRow([
          current ? "host current" : "host",
          s.renaming?.id === host.id ? s.renaming.field : HostName([host.name]),
          current ? HostChip([]) : null,
          host.url,
          host.device ? "host-state paired" : "host-state",
          host.device ? "Paired" : "Not paired",
          blocked ? HostBlocked([blocked]) : null,
          current ? null : HostConnect([() => switchTo(loadHosts(), host)]),
          `Rename ${host.name}`,
          () => rename(host),
          icon(icons.tag),
          `Forget ${host.name}`,
          () => {
            const all = loadHosts();
            all.hosts = all.hosts.filter((y) => y.id !== host.id);
            if (all.current === host.id) all.current = all.hosts[0]?.id ?? "";
            saveHosts(all);
            toast(`Forgot ${host.name}`);
            if (current) location.assign(location.pathname);
            else redraw();
          },
          icon(icons.trash),
        ]);
      },
    ),
  ]);
}

// The add form submits on its own, so the dialog's close form holds only Done.
const HostsDialog = html`<dialog class="dialog hosts" onclose=${on<"close", HTMLDialogElement>()}>
  <h2>Hosts</h2>
  <p class="hosts-lead">${text()}</p>
  <div class="hosts-list-wrap">${child<HostListNode>()}</div>
  ${child<AddFormNode>()}
  <form method="dialog" class="dialog-actions"><button value="ok" class="btn">Done</button></form>
</dialog>`;

/** What `hostsDialog` returns. */
export type HostsDialogNode = ViewNodeOf<typeof HostsDialog>;

/** The Hosts dialog for a state; `redraw` renders it again. */
export function hostsDialog(s: HostsState, redraw: () => void): HostsDialogNode {
  return HostsDialog([
    function () {
      this.remove();
    },
    "The herdsman servers this browser talks to. Each one pairs with a code from its terminal.",
    hostList(s, redraw),
    addForm(s, redraw),
  ]);
}

// A checked narrowing, not a cast.
const isDialog = (n: Node | null): n is HTMLDialogElement => n !== null && n.nodeName === "DIALOG";

/** Opens the Hosts dialog. */
export function openHosts(): void {
  const s = hostsState();
  // The dialog is the view's root (its children are the dialog's own: a view
  // cannot fill a container with several roots), so it is rendered in a
  // holder and moved to the page. renderDirect, not render: render's
  // delegated handlers listen on the holder, which the dialog leaves. Later
  // renders of the same view patch it where it is.
  const holder = document.createElement("div");
  const redraw = (): void => renderDirect(holder, hostsDialog(s, redraw));
  redraw();
  const dlg = holder.firstChild;
  if (!isDialog(dlg)) throw new Error("hosts: the view's root is not a dialog");
  dismissOnBackdrop(dlg);
  document.body.append(dlg);
  dlg.showModal();
}

// The pairing screens replace the app's root with a <main> they own: main.ts
// also fills that root with mount(), so a render() kept on the root itself
// would go stale.
const PairMain = html`<main class="pair"></main>`;

function showScreen(root: HTMLElement): HTMLElement {
  const main = mountElement("main", PairMain, []);
  root.replaceChildren(main);
  return main;
}

const ConfirmCard = html`<div class="pair-card connect-card">
  ${child<LogoNode>()}
  <h1>Add this host?</h1>
  <p class="pair-lead">A link asks this app to connect to a host it does not know yet:</p>
  <code class="host-url">${text()}</code>
  <p class="pair-note">${child<IconNode>()}${text()}</p>
  <div class="dialog-actions">
    <button type="button" class="btn" onclick=${on<"click">()}>Cancel</button>
    <button type="button" class="btn primary" onclick=${on<"click">()}>Add and pair</button>
  </div>
</div>`;

/**
 * Asks before a link adds a host this browser does not know: any page or
 * artifact can open such a link, and the host gets what you type to agents.
 */
export function confirmHost(root: HTMLElement, url: string): Promise<boolean> {
  return new Promise((resolve) => {
    render(
      showScreen(root),
      ConfirmCard([
        logoMark(),
        url,
        icon(icons.server),
        "Add it only if you started herdsman web there. The host sees your agents' screens and what you type to them.",
        () => resolve(false),
        () => resolve(true),
      ]),
    );
  });
}

const Retry = html`<button class="btn primary pair-retry" type="button" onclick=${on<"click">()}>Try again</button>`;
const ConnectCard = html`<div class="pair-card connect-card">
  ${child<LogoNode>()}
  <h1>${text()}</h1>
  <p class="pair-lead">${text()}</p>
  ${child<ViewNodeOf<typeof Retry> | null>()}${child<AddFormNode>()}
  <div class="hosts-list-wrap">${child<HostListNode>()}</div>
  <p class="pair-note">${child<IconNode>()}${text()}</p>
  ${child<InstallGuideNode>()}
</div>`;

/**
 * The screen for a browser with nothing to show: no host yet, a host that
 * does not know this browser, or one it cannot reach.
 */
export function showConnect(root: HTMLElement, title: string, text: string, retry?: () => void): void {
  const s = hostsState();
  const main = showScreen(root);
  const redraw = (): void =>
    render(
      main,
      ConnectCard([
        logoMark(),
        title,
        text,
        retry ? Retry([retry]) : null,
        addForm(s, redraw),
        hostList(s, redraw),
        icon(icons.keyboard),
        "Pairing proves you can see the host's terminal, so a shared or leaked link is not enough to control your agents.",
        installGuide(),
      ]),
    );
  redraw();
  main.querySelector?.<HTMLInputElement>(".hosts-add input")?.focus();
}

const brewInstall =
  "brew tap arafathusayn/tap\nbrew trust arafathusayn/tap\nbrew install --cask arafathusayn/tap/herdsman";

const Command = html`<div class="install-cmd">
  <pre><code>${text()}</code></pre>
  <button type="button" class="icon-btn" aria-label="Copy" data-tip="Copy" onclick=${on<"click">()}>
    ${child<IconNode>()}
  </button>
</div>`;

/** A command with a copy button. */
function command(text: string): ViewNodeOf<typeof Command> {
  const copy = async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(text);
      toast("Copied.");
    } catch {
      toast("The browser blocked the clipboard. Select the text and copy it.", "error");
    }
  };
  return Command([text, () => void copy(), icon(icons.copy)]);
}

const InstallGuide = html`<section class="install" aria-labelledby="install-title">
  <h2 id="install-title">How to install herdsman</h2>
  <p class="install-lead">${text()}${text()}</p>
  <ol class="install-steps">
    <li>
      <p>${text()}</p>
      ${child<ViewNodeOf<typeof Command>>()}
      <p class="install-alt">
        ${text()}<a href="https://github.com/arafathusayn/herdsman/releases" rel="noopener" target="_blank">Releases</a
        >${text()}
      </p>
    </li>
    <li>
      <p>Start it there:</p>
      ${child<ViewNodeOf<typeof Command>>()}
    </li>
    <li>
      <p>${text()}</p>
    </li>
  </ol>
  <a class="install-docs" href="./docs/">${text()}${child<IconNode>()}</a>
</section>`;

/** What `installGuide` returns. */
export type InstallGuideNode = ViewNodeOf<typeof InstallGuide>;

/**
 * How someone who lands on this app without herdsman gets it running: the
 * hosted copy is often the first thing a new person sees.
 */
function installGuide(): InstallGuideNode {
  return InstallGuide([
    "herdsman runs a team of coding agents inside herdr. This app shows and steers them from any browser, ",
    "and talks only to the machines you pair it with.",
    "On the machine that runs herdr, install the herdsman command with Homebrew:",
    command(brewInstall),
    "Or download it for macOS or Linux from ",
    ".",
    command("herdsman web --open"),
    "Open the link it prints, or paste it above. Then type the 6-digit code that the terminal shows.",
    "Read the docs: install the skill, run a route, use a phone",
    icon(icons.arrow),
  ]);
}
