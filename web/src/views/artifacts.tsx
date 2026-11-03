import { batch, For, memo, onCleanup, Show, signal } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { listArtifacts, openArtifact } from "../api.ts";
import { Highlight } from "../components/highlight.tsx";
import { Icon } from "../components/icons.tsx";
import { fuzzy } from "../fuzzy.ts";
import { icons } from "../icons.ts";
import { ago } from "../model.ts";
import { store } from "../store.ts";
import type { Artifact } from "../types.ts";
import { toast } from "../ui.ts";

const refreshEvery = 5000;

/** An artifact's full path, the key the URL keeps. */
export const artifactPath = (a: Artifact): string => `${a.root}/${a.path}`;

/** A workspace folder as the list names it: its last two parts. */
export function rootLabel(root: string): string {
  const parts = root.split("/").filter(Boolean);
  return parts.slice(-2).join("/") || "/";
}

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

type ArtifactBody = { kind: "note"; cls: string; text: string } | { kind: "frame"; url: string; title: string };
interface ArtifactSnapshot {
  items: Artifact[];
  loaded: boolean;
  error: string;
  selected: string;
  query: string;
  active: number;
  body: ArtifactBody;
  open: Map<string, boolean>;
}
export interface ArtifactsState {
  routeId: string;
  state: () => ArtifactSnapshot;
  query: (value: string) => void;
  cursor: (value: number) => void;
  group: (root: string, open: boolean) => void;
  update: (selected: string) => void;
  reload: () => void;
  start: () => void;
  stop: () => void;
  focusSearch: () => void;
  search: (input: HTMLInputElement | null) => void;
  openTab: () => void;
}

/** A route's cached list and search survive tab switches; only its active tab polls. */
export function createArtifactsState(routeId: string): ArtifactsState {
  const [state, setState] = signal<ArtifactSnapshot>({
    items: [],
    loaded: false,
    error: "",
    selected: "",
    query: "",
    active: 0,
    open: new Map(),
    body: { kind: "note", cls: "muted artifact-empty", text: "Pick a page to show it." },
  });
  const set = (patch: Partial<ArtifactSnapshot>): void => setState((current) => ({ ...current, ...patch }));
  let search: HTMLInputElement | null = null;
  let shownKey = "",
    frameUrl = "";
  let request = 0,
    run = 0,
    timer: ReturnType<typeof setTimeout> | undefined;
  const syncBody = (): void => {
    const current = state();
    const item = current.items.find(
      (item) => artifactPath(item) === (current.selected || artifactPath(current.items[0]!)),
    );
    if (!item) {
      shownKey = "";
      request++;
      set({
        body: {
          kind: "note",
          cls: "muted artifact-empty",
          text:
            current.loaded && current.selected
              ? "This page is no longer in the route's workspace folders."
              : "Pick a page to show it.",
        },
      });
      return;
    }
    const key = JSON.stringify([artifactPath(item), item.modified]);
    if (shownKey === key) return;
    shownKey = key;
    const id = ++request;
    set({ body: { kind: "note", cls: "muted", text: "Loading…" } });
    void openArtifact(routeId, item.root, item.path)
      .then((url) => {
        if (id !== request) return;
        frameUrl = url;
        set({ body: { kind: "frame", url, title: item.path } });
      })
      .catch((error: unknown) => {
        if (id === request)
          set({ body: { kind: "note", cls: "error", text: error instanceof Error ? error.message : String(error) } });
      });
  };
  const load = async (): Promise<void> => {
    try {
      const result = await listArtifacts(routeId);
      const items = result.artifacts ?? [];
      const current = state();
      if (!current.loaded || current.error || JSON.stringify(items) !== JSON.stringify(current.items)) {
        batch(() => {
          set({ items, loaded: true, error: "" });
          syncBody();
        });
      }
    } catch (error) {
      set({ loaded: true, error: error instanceof Error ? error.message : String(error) });
    }
  };
  const stop = (): void => {
    clearTimeout(timer);
    timer = undefined;
    run++;
  };
  const start = (): void => {
    if (timer !== undefined) return;
    const current = ++run;
    const tick = async (): Promise<void> => {
      await load();
      if (timer !== undefined && current === run) timer = setTimeout(() => void tick(), refreshEvery);
    };
    timer = setTimeout(() => void tick(), 0);
  };
  return {
    routeId,
    state,
    start,
    stop,
    query: (query) => set({ query, active: 0 }),
    cursor: (active) => set({ active }),
    group: (root, open) => {
      if (state().open.get(root) !== open) set({ open: new Map([...state().open, [root, open]]) });
    },
    update: (selected) => {
      batch(() => {
        if (state().selected !== selected) set({ selected });
        syncBody();
      });
    },
    reload: () => {
      shownKey = "";
      syncBody();
    },
    search: (input) => {
      search = input;
    },
    focusSearch: () => {
      search?.focus();
      search?.select();
    },
    openTab: () => {
      if (frameUrl) window.open(frameUrl, "_blank", "noopener,noreferrer");
    },
  };
}

function ArtifactRow(props: { item: Artifact; hits: number[]; cursor: boolean; showRoot: boolean; current: string }) {
  const data = memo(() => {
    const slash = props.item.path.lastIndexOf("/");
    return {
      full: artifactPath(props.item),
      name: props.item.path.slice(slash + 1),
      dir: slash < 0 ? "" : props.item.path.slice(0, slash + 1),
      nameHits: props.hits.filter((index) => index > slash).map((index) => index - slash - 1),
      dirHits: props.hits.filter((index) => index <= slash),
    };
  });
  return (
    <button
      class={["file-row", props.current === data().full ? "active" : "", props.cursor ? "cursor" : ""]
        .filter(Boolean)
        .join(" ")}
      role="option"
      aria-selected={props.current === data().full ? "true" : "false"}
      title={data().full}
      onClick={() => store.go({ artifact: data().full }, false)}
    >
      <Icon paths={icons.artifact} />
      <span class="file-name">
        <span class="name">
          <Highlight value={data().name} hits={data().nameHits} />
        </span>
        <Show when={data().dir || props.showRoot}>
          <span class="dir">
            {props.showRoot ? `${rootLabel(props.item.root)}/` : ""}
            <Highlight value={data().dir} hits={data().dirHits} />
          </span>
        </Show>
      </span>
      <small>{ago(props.item.modified)}</small>
    </button>
  );
}

interface ArtifactGroup {
  root: string;
  items: Artifact[];
}
function ArtifactGroupView(props: { group: ArtifactGroup; current: string; state: ArtifactsState }) {
  const items = map(
    () => props.group.items,
    (item) => item,
    { keyed: artifactPath },
  );
  return (
    <details
      class="file-group"
      open={props.state.state().open.get(props.group.root) ?? true}
      onToggle={(event) => props.state.group(props.group.root, (event.currentTarget as HTMLDetailsElement).open)}
    >
      <summary title={props.group.root}>
        <Icon paths={icons.folder} />
        <span>{rootLabel(props.group.root)}</span>
        <span class="count">{String(props.group.items.length)}</span>
      </summary>
      <For each={items()} key={(item) => artifactPath(item())}>
        {(item) => <ArtifactRow item={item()} hits={[]} cursor={false} showRoot={false} current={props.current} />}
      </For>
    </details>
  );
}

export function ArtifactsView(props: { state: ArtifactsState }) {
  onCleanup(() => props.state.search(null));
  const snapshot = props.state.state;
  const query = memo(() => snapshot().query.trim());
  const current = memo(() => snapshot().selected || (snapshot().items[0] ? artifactPath(snapshot().items[0]!) : ""));
  const item = memo(() => snapshot().items.find((item) => artifactPath(item) === current()));
  const body = memo(() => snapshot().body);
  const frame = memo(() => {
    const value = body();
    return value.kind === "frame" ? value : null;
  });
  const note = memo(() => {
    const value = body();
    return value.kind === "note" ? value : null;
  });
  const matches = memo(() =>
    snapshot()
      .items.map((item) => ({ item, match: fuzzy(query(), item.path) }))
      .filter((entry): entry is { item: Artifact; match: NonNullable<ReturnType<typeof fuzzy>> } => !!entry.match)
      .sort((left, right) => right.match.score - left.match.score),
  );
  const active = memo(() => Math.min(snapshot().active, Math.max(0, matches().length - 1)));
  const rows = map(matches, (entry) => entry, { keyed: (entry) => artifactPath(entry.item) });
  const groups = map(
    () => {
      const groups = new Map<string, Artifact[]>();
      for (const item of snapshot().items) groups.set(item.root, [...(groups.get(item.root) ?? []), item]);
      return [...groups].map(([root, items]) => ({ root, items }));
    },
    (group) => group,
    { keyed: (group) => group.root },
  );
  let root!: HTMLDivElement, search!: HTMLInputElement;
  const key = (event: KeyboardEvent): void => {
    if (event.key === "Escape") {
      if (search.value) {
        event.preventDefault();
        props.state.query("");
      } else search.blur();
      return;
    }
    if (!query() || !matches().length) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const count = matches().length;
      props.state.cursor((active() + (event.key === "ArrowDown" ? 1 : count - 1)) % count);
      root.querySelector(".file-groups .file-row.cursor")?.scrollIntoView({ block: "nearest" });
    } else if (event.key === "Enter") {
      event.preventDefault();
      const selected = matches()[active()]?.item;
      if (selected) store.go({ artifact: artifactPath(selected) }, false);
    }
  };
  return (
    <div
      class="files artifacts"
      ref={(element) => {
        root = element;
      }}
    >
      <div class="file-pane">
        <label class="file-search">
          <Icon paths={icons.search} />
          <input
            type="search"
            placeholder="Search pages…"
            aria-label="Search artifacts"
            autocomplete="off"
            spellcheck="false"
            value={snapshot().query}
            ref={(element) => {
              search = element;
              props.state.search(element);
            }}
            onInput={() => props.state.query(search.value)}
            onKeyDown={key}
          />
          <span class="files-count">
            {!snapshot().loaded
              ? ""
              : query()
                ? `${matches().length}/${snapshot().items.length}`
                : String(snapshot().items.length)}
          </span>
        </label>
        <div class="file-groups" role="listbox" aria-label="Artifacts">
          <Show when={snapshot().loaded} fallback={<p class="file-empty">Looking for HTML pages…</p>}>
            <Show
              when={!snapshot().error || snapshot().items.length > 0}
              fallback={<p class="file-empty error">{snapshot().error}</p>}
            >
              <Show
                when={query()}
                fallback={
                  <Show
                    when={groups().length > 0}
                    fallback={
                      <p class="file-empty">
                        No HTML pages in this route's workspace folders yet. Pages agents write (for example under
                        .lavish/ or reports/) show here.
                      </p>
                    }
                  >
                    <For each={groups()} key={(group) => group().root}>
                      {(group) => <ArtifactGroupView group={group()} current={current()} state={props.state} />}
                    </For>
                  </Show>
                }
              >
                <For
                  each={rows()}
                  key={(entry) => artifactPath(entry().item)}
                  fallback={<p class="file-empty">{`No page matches “${query()}”.`}</p>}
                >
                  {(entry) => (
                    <ArtifactRow
                      item={entry().item}
                      hits={entry().match.hits}
                      cursor={matches()[active()]?.item === entry().item}
                      showRoot={true}
                      current={current()}
                    />
                  )}
                </For>
              </Show>
            </Show>
          </Show>
        </div>
      </div>
      <section class="viewer" aria-live="polite">
        <div class="viewer-head">
          <Show when={item()}>
            {(item) => (
              <>
                <div class="viewer-title">
                  <h2 title={artifactPath(item())}>
                    <Show when={item().path.includes("/")}>
                      <span class="dir">{item().path.slice(0, item().path.lastIndexOf("/") + 1)}</span>
                    </Show>
                    {item().path.slice(item().path.lastIndexOf("/") + 1)}
                  </h2>
                  <p>{`${rootLabel(item().root)} · ${size(item().size)} · ${ago(item().modified)}`}</p>
                </div>
                <button
                  class="icon-btn"
                  data-tip="Reload the page"
                  aria-label="Reload the page"
                  onClick={() => props.state.reload()}
                >
                  <Icon paths={icons.refresh} />
                </button>
                <button
                  class="icon-btn"
                  data-tip="Open in a new tab"
                  aria-label="Open in a new tab"
                  onClick={() => props.state.openTab()}
                >
                  <Icon paths={icons.external} />
                </button>
                <button
                  class="icon-btn"
                  data-tip="Copy the file path"
                  aria-label="Copy the file path"
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(artifactPath(item()));
                      toast("Copied the file path.");
                    } catch {
                      toast("The browser blocked the clipboard.", "error");
                    }
                  }}
                >
                  <Icon paths={icons.copy} />
                </button>
              </>
            )}
          </Show>
        </div>
        <div class="viewer-body artifact-body">
          <Show when={frame()} fallback={<p class={note()?.cls ?? ""}>{note()?.text ?? ""}</p>}>
            {(frame) => (
              <iframe
                class="artifact-frame"
                src={frame().url}
                title={frame().title}
                sandbox="allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads"
                referrerpolicy="no-referrer"
              />
            )}
          </Show>
        </div>
      </section>
    </div>
  );
}
