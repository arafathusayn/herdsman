import { attach, batch, For, Show } from "@reactre/core";
import { map } from "@reactre/core/features/lists";
import type { Child } from "@reactre/core/jsx-runtime";

import { Icon, Logo } from "../components/icons.tsx";
import { mountRoot } from "../components/mount-root.ts";
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
import { icons } from "../icons.ts";
import { lifetime } from "../state/lifetime.ts";
import { valueState } from "../state/value.ts";
import { dismissOnBackdrop, toast } from "../ui.ts";

const pairKey = "herdsman.pending-link";

/**
 * Makes a host current and starts the app over with it. A pairing token
 * rides along in session storage, so it never sits in the address bar.
 */
export function switchTo(hs: Hosts, host: Host, token = ""): void {
  // The page starts over from the stored list, so a choice it cannot store
  // would come back as the old host.
  if (!saveHosts({ ...hs, current: host.id })) {
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
    const v = JSON.parse(sessionStorage.getItem(pairKey) ?? "null") as {
      url?: string;
      token?: string;
    } | null;
    sessionStorage.removeItem(pairKey);
    return v?.url === url && v.token ? v.token : "";
  } catch {
    return "";
  }
}

interface HostsFormState {
  host: string;
  name: string;
  note: string;
  noteError: boolean;
  renaming: string | null;
  hosts: Hosts;
}

export interface HostsState {
  state: () => HostsFormState;
  set: (patch: Partial<HostsFormState>) => void;
  reload: () => void;
}

/** A dialog owns one actor snapshot; edits never invalidate the app. */
export function hostsState(): HostsState {
  const [state, setState] = valueState<HostsFormState>({
    host: "",
    name: "",
    note: "",
    noteError: false,
    renaming: null,
    hosts: loadHosts(),
  });
  const set = (patch: Partial<HostsFormState>): void => setState((current) => ({ ...current, ...patch }));
  const reload = (): void => {
    const hosts = loadHosts();
    if (JSON.stringify(hosts) !== JSON.stringify(state().hosts)) set({ hosts });
  };
  return { state, set, reload };
}

function AddForm(props: { form: HostsState }) {
  const state = props.form.state;
  const submit = (event: SubmitEvent): void => {
    event.preventDefault();
    const raw = state().host.trim();
    const link = parseLink(raw, location.origin);
    const url = link?.url ?? normalizeUrl(raw);
    if (!url) {
      props.form.set({
        noteError: true,
        note: "That is not a host address or a pairing link.",
      });
      return;
    }
    const hosts = loadHosts();
    const was = hosts.current;
    const host = upsert(hosts, url, state().name);
    if (!link && was) hosts.current = was;
    if (link) {
      switchTo(hosts, host, link.token);
      return;
    }
    saveHosts(hosts);
    batch(() => {
      props.form.reload();
      props.form.set({
        host: "",
        name: "",
        noteError: false,
        note: `Added ${host.name}. Paste its pairing link to pair this browser with it.`,
      });
    });
  };
  return (
    <form class="hosts-add" onSubmit={submit}>
      <label class="hosts-label">Add a host</label>
      <div class="hosts-row">
        <input
          class="input"
          name="host"
          placeholder="http://127.0.0.1:7878/#t=…  or  192.168.1.20:7878"
          aria-label="Pairing link or host address"
          autocomplete="off"
          value={state().host}
          onInput={(event) =>
            props.form.set({
              host: (event.currentTarget as HTMLInputElement).value,
            })
          }
        />
        <input
          class="input"
          name="name"
          placeholder="Name (optional)"
          aria-label="Host name"
          value={state().name}
          onInput={(event) =>
            props.form.set({
              name: (event.currentTarget as HTMLInputElement).value,
            })
          }
        />
        <button class="btn primary" type="submit">
          Add
        </button>
      </div>
      <p class={state().noteError ? "hosts-note error" : "hosts-note"} role="status" aria-live="polite">
        {state().note}
      </p>
      <p class="hosts-help">
        Run <code>herdsman web</code>
        {" on the host and paste the link it prints. To reach it from another device, start it with "}
        <code>{"--host <LAN or Tailscale IP>"}</code>
        {", or put it behind HTTPS and add "}
        <code>{"--allow-host <domain>"}</code>.
      </p>
    </form>
  );
}

function HostRow(props: { host: Host; form: HostsState }) {
  const state = props.form.state;
  let field: HTMLInputElement | undefined;
  const finish = (input: HTMLInputElement, save: boolean): void => {
    if (field !== input || state().renaming !== props.host.id) return;
    field = undefined;
    if (save && input.value.trim()) {
      const hosts = loadHosts();
      const host = hosts.hosts.find((host) => host.id === props.host.id);
      if (host) host.name = input.value.trim();
      saveHosts(hosts);
    }
    batch(() => {
      props.form.set({ renaming: null });
      props.form.reload();
    });
  };
  const rename = (): void => {
    if (state().renaming === props.host.id) return;
    props.form.set({ renaming: props.host.id });
    field?.focus();
    field?.select();
  };
  const forget = (): void => {
    const hosts = loadHosts();
    hosts.hosts = hosts.hosts.filter((host) => host.id !== props.host.id);
    const current = hosts.current === props.host.id;
    if (current) hosts.current = hosts.hosts[0]?.id ?? "";
    saveHosts(hosts);
    toast(`Forgot ${props.host.name}`);
    if (current) location.assign(location.pathname);
    else props.form.reload();
  };
  return (
    <li class={state().hosts.current === props.host.id ? "host current" : "host"}>
      <div class="host-main">
        <div class="host-title">
          <Show when={state().renaming === props.host.id} fallback={<span class="host-name">{props.host.name}</span>}>
            <input
              class="input host-rename"
              value={props.host.name}
              aria-label="Host name"
              ref={(element) => {
                field = element;
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === "Escape") {
                  event.preventDefault();
                  if (event.key === "Escape") event.stopPropagation();
                  finish(event.currentTarget as HTMLInputElement, event.key === "Enter");
                }
              }}
              onBlur={(event) => finish(event.currentTarget as HTMLInputElement, true)}
            />
          </Show>
          <Show when={state().hosts.current === props.host.id}>
            <span class="host-chip">Current</span>
          </Show>
        </div>
        <code class="host-url">{props.host.url}</code>
        <span class={props.host.device ? "host-state paired" : "host-state"}>
          {props.host.device ? "Paired" : "Not paired"}
        </span>
        <Show when={blockedReason(location.protocol, props.host.url)}>
          {(reason) => <p class="hosts-note error">{reason()}</p>}
        </Show>
      </div>
      <div class="host-actions">
        <Show when={state().hosts.current !== props.host.id}>
          <button type="button" class="btn" onClick={() => switchTo(loadHosts(), props.host)}>
            Connect
          </button>
        </Show>
        <button
          type="button"
          class="icon-btn"
          aria-label={`Rename ${props.host.name}`}
          data-tip="Rename"
          onClick={rename}
        >
          <Icon paths={icons.tag} />
        </button>
        <button
          type="button"
          class="icon-btn"
          aria-label={`Forget ${props.host.name}`}
          data-tip="Forget"
          onClick={forget}
        >
          <Icon paths={icons.trash} />
        </button>
      </div>
    </li>
  );
}

function HostList(props: { form: HostsState }) {
  const rows = map(
    () => props.form.state().hosts.hosts,
    (host) => host,
    { keyed: (host) => host.id },
  );
  return (
    <Show when={rows().length > 0} fallback={<p class="hosts-empty">No hosts yet.</p>}>
      <ul class="hosts-list">
        <For each={rows()} key={(host) => host().id}>
          {(host) => <HostRow host={host()} form={props.form} />}
        </For>
      </ul>
    </Show>
  );
}

export function HostsDialog(props: {
  form: HostsState;
  close?: () => void;
  ref?: (dialog: HTMLDialogElement) => void;
}) {
  return (
    <dialog class="dialog hosts" ref={props.ref} onClose={() => props.close?.()}>
      <h2>Hosts</h2>
      <p class="hosts-lead">
        The herdsman servers this browser talks to. Each one pairs with a code from its terminal.
      </p>
      <div class="hosts-list-wrap">
        <HostList form={props.form} />
      </div>
      <AddForm form={props.form} />
      <form method="dialog" class="dialog-actions">
        <button value="ok" class="btn">
          Done
        </button>
      </form>
    </dialog>
  );
}

export function openHosts(): void {
  const form = hostsState();
  let dialog!: HTMLDialogElement;
  let dispose = (): void => {};
  dispose = attach(
    () => (
      <HostsDialog
        form={form}
        ref={(element) => {
          dialog = element;
        }}
        close={() => dispose()}
      />
    ),
    document.body,
  );
  dismissOnBackdrop(dialog);
  dialog.showModal();
}

const screens = new WeakMap<HTMLElement, () => void>();
/** Release a connect screen before the app frame or pairing form takes over. */
export function disposeHostScreen(root: HTMLElement): void {
  screens.get(root)?.();
  screens.delete(root);
}
function showScreen(root: HTMLElement, content: () => Child): () => void {
  disposeHostScreen(root);
  const view = mountRoot("main", () => (
    <main class="pair">
      <>{content()}</>
    </main>
  ));
  root.replaceChildren(view.el);
  screens.set(root, view.dispose);
  return () => {
    if (screens.get(root) === view.dispose) screens.delete(root);
    view.dispose();
  };
}

/** Ask before a link grants a new host access to the browser's agents. */
export function confirmHost(root: HTMLElement, url: string): Promise<boolean> {
  return new Promise((resolve) => {
    const answered = lifetime();
    const answer = (value: boolean): void => {
      if (!answered.close()) return;
      resolve(value);
    };
    showScreen(root, () => (
      <div class="pair-card connect-card">
        <Logo />
        <h1>Add this host?</h1>
        <p class="pair-lead">A link asks this app to connect to a host it does not know yet:</p>
        <code class="host-url">{url}</code>
        <p class="pair-note">
          <Icon paths={icons.server} />
          Add it only if you started herdsman web there. The host sees your agents' screens and what you type to them.
        </p>
        <div class="dialog-actions">
          <button type="button" class="btn" onClick={() => answer(false)}>
            Cancel
          </button>
          <button type="button" class="btn primary" onClick={() => answer(true)}>
            Add and pair
          </button>
        </div>
      </div>
    ));
  });
}

export function showConnect(root: HTMLElement, title: string, text: string, retry?: () => void): () => void {
  const form = hostsState();
  const dispose = showScreen(root, () => (
    <div class="pair-card connect-card">
      <Logo />
      <h1>{title}</h1>
      <p class="pair-lead">{text}</p>
      <Show when={retry}>
        <button class="btn primary pair-retry" type="button" onClick={() => retry?.()}>
          Try again
        </button>
      </Show>
      <AddForm form={form} />
      <div class="hosts-list-wrap">
        <HostList form={form} />
      </div>
      <p class="pair-note">
        <Icon paths={icons.keyboard} />
        Pairing proves you can see the host's terminal, so a shared or leaked link is not enough to control your agents.
      </p>
      <InstallGuide />
    </div>
  ));
  root.querySelector<HTMLInputElement>(".hosts-add input")?.focus();
  return dispose;
}

const brewInstall =
  "brew tap arafathusayn/tap\nbrew trust arafathusayn/tap\nbrew install --cask arafathusayn/tap/herdsman";
function Command(props: { text: string }) {
  return (
    <div class="install-cmd">
      <pre>
        <code>{props.text}</code>
      </pre>
      <button
        type="button"
        class="icon-btn"
        aria-label="Copy"
        data-tip="Copy"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(props.text);
            toast("Copied.");
          } catch {
            toast("The browser blocked the clipboard. Select the text and copy it.", "error");
          }
        }}
      >
        <Icon paths={icons.copy} />
      </button>
    </div>
  );
}

function InstallGuide() {
  return (
    <section class="install" aria-labelledby="install-title">
      <h2 id="install-title">How to install herdsman</h2>
      <p class="install-lead">
        {"herdsman runs a team of coding agents inside herdr. This app shows and steers them from any browser, " +
          "and talks only to the machines you pair it with."}
      </p>
      <ol class="install-steps">
        <li>
          <p>On the machine that runs herdr, install the herdsman command with Homebrew:</p>
          <Command text={brewInstall} />
          <p class="install-alt">
            {"Or download it for macOS or Linux from "}
            <a href="https://github.com/arafathusayn/herdsman/releases" rel="noopener" target="_blank">
              Releases
            </a>
            .
          </p>
        </li>
        <li>
          <p>Start it there:</p>
          <Command text="herdsman web --open" />
        </li>
        <li>
          <p>Open the link it prints, or paste it above. Then type the 6-digit code that the terminal shows.</p>
        </li>
      </ol>
      <a class="install-docs" href="./docs/">
        Read the docs: install the skill, run a route, use a phone
        <Icon paths={icons.arrow} />
      </a>
    </section>
  );
}
