import { batch, For, memo, onCleanup, Show, signal } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { routeFile } from "../api.ts";
import { Highlight } from "../components/highlight.tsx";
import { Icon } from "../components/icons.tsx";
import { fuzzy } from "../fuzzy.ts";
import { icons } from "../icons.ts";
import { CodeBlock, Markdown } from "../markdown.tsx";
import { ago } from "../model.ts";
import { store } from "../store.ts";
import type { Route, RouteFile } from "../types.ts";
import { toast } from "../ui.ts";

const folderRank = ["", "reports", "doc", "ref"];
const collapsedByDefault = new Set(["probes"]);
const modes = ["Rendered", "Raw"] as const;
const folderOf = (path: string): string => (path.includes("/") ? path.slice(0, path.indexOf("/")) : "");
function size(value: number): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 / 1024).toFixed(1)} MB`;
}

interface FilesSnapshot {
  route: Route | null;
  selected: string;
  query: string;
  active: number;
  raw: boolean;
  text: string;
  status: "empty" | "loading" | "loaded" | "error";
  error: string;
  open: Map<string, boolean>;
}
export interface FilesState {
  routeId: string;
  state: () => FilesSnapshot;
  update: (route: Route, selected: string) => void;
  query: (query: string) => void;
  cursor: (index: number) => void;
  raw: (raw: boolean) => void;
  group: (folder: string, open: boolean) => void;
  retry: () => void;
  search: (input: HTMLInputElement | null) => void;
  focusSearch: () => void;
}

/** Data and local preferences persist for this route; components own its DOM. */
export function createFilesState(routeId: string): FilesState {
  const [state, setState] = signal<FilesSnapshot>({
    route: null,
    selected: "",
    query: "",
    active: 0,
    raw: false,
    text: "",
    status: "empty",
    error: "",
    open: new Map(),
  });
  const set = (patch: Partial<FilesSnapshot>): void => setState((current) => ({ ...current, ...patch }));
  let search: HTMLInputElement | null = null;
  let requested = "",
    cached = "",
    request = 0;
  const load = (): void => {
    const current = state();
    const route = current.route;
    if (!route) return;
    const file = route.files?.find((file) => file.path === current.selected);
    const key = JSON.stringify([route.id, current.selected, file?.modified ?? ""]);
    if (requested === key) return;
    requested = key;
    const id = ++request;
    if (key === cached) {
      set({ status: "loaded", error: "" });
      return;
    }
    set({ status: "loading", error: "" });
    void routeFile(route.id, current.selected)
      .then((text) => {
        if (id !== request) return;
        cached = key;
        set({ text, status: "loaded" });
      })
      .catch((error: unknown) => {
        if (id !== request) return;
        requested = "";
        set({ status: "error", error: error instanceof Error ? error.message : String(error) });
      });
  };
  return {
    routeId,
    state,
    update: (route, selected) =>
      batch(() => {
        selected ||= "00-route.md";
        if (state().route !== route || state().selected !== selected) set({ route, selected });
        load();
      }),
    query: (query) => set({ query, active: 0 }),
    cursor: (active) => set({ active }),
    raw: (raw) => set({ raw }),
    group: (folder, open) => {
      if (state().open.get(folder) !== open) set({ open: new Map([...state().open, [folder, open]]) });
    },
    retry: () => {
      requested = "";
      load();
    },
    search: (input) => {
      search = input;
    },
    focusSearch: () => {
      search?.focus();
      search?.select();
    },
  };
}

function FileRow(props: { file: RouteFile; hits: number[]; cursor: boolean; showFolder: boolean; selected: string }) {
  const data = memo(() => {
    const slash = props.file.path.lastIndexOf("/");
    return {
      name: props.file.path.slice(slash + 1),
      dir: slash < 0 ? "" : props.file.path.slice(0, slash + 1),
      nameHits: props.hits.filter((hit) => hit > slash).map((hit) => hit - slash - 1),
      dirHits: props.hits.filter((hit) => hit <= slash),
    };
  });
  return (
    <button
      class={["file-row", props.selected === props.file.path ? "active" : "", props.cursor ? "cursor" : ""]
        .filter(Boolean)
        .join(" ")}
      role="option"
      aria-selected={props.selected === props.file.path ? "true" : "false"}
      title={props.file.path}
      onClick={() => store.go({ file: props.file.path }, false)}
    >
      <Icon paths={icons.file} />
      <span class="file-name">
        <span class="name">
          <Highlight value={data().name} hits={data().nameHits} />
        </span>
        <Show when={props.showFolder && data().dir}>
          <span class="dir">
            <Highlight value={data().dir} hits={data().dirHits} />
          </span>
        </Show>
      </span>
      <small>{ago(props.file.modified)}</small>
    </button>
  );
}

interface FileGroup {
  folder: string;
  items: RouteFile[];
}
function FileGroupView(props: { group: FileGroup; state: FilesState }) {
  const snapshot = props.state.state;
  const items = map(
    () => props.group.items,
    (item) => item,
    { keyed: (item) => item.path },
  );
  return (
    <details
      class="file-group"
      open={
        snapshot().open.get(props.group.folder) ??
        (props.group.items.some((file) => file.path === snapshot().selected) ||
          !collapsedByDefault.has(props.group.folder))
      }
      onToggle={(event) => props.state.group(props.group.folder, (event.currentTarget as HTMLDetailsElement).open)}
    >
      <summary>
        <Icon paths={icons.folder} />
        <span>{props.group.folder ? `${props.group.folder}/` : "Route files"}</span>
        <span class="count">{String(props.group.items.length)}</span>
      </summary>
      <For each={items()} key={(file) => file().path}>
        {(file) => <FileRow file={file()} hits={[]} cursor={false} showFolder={false} selected={snapshot().selected} />}
      </For>
    </details>
  );
}

export function FilesView(props: { state: FilesState }) {
  onCleanup(() => props.state.search(null));
  const snapshot = props.state.state;
  const files = memo(() => snapshot().route?.files ?? []);
  const query = memo(() => snapshot().query.trim());
  const text = memo(() => snapshot().text);
  const lines = memo(() => text().split("\n"));
  const matches = memo(() =>
    files()
      .map((file) => ({ file, match: fuzzy(query(), file.path) }))
      .filter((entry): entry is { file: RouteFile; match: NonNullable<ReturnType<typeof fuzzy>> } => !!entry.match)
      .sort((left, right) => right.match.score - left.match.score),
  );
  const active = memo(() => Math.min(snapshot().active, Math.max(0, matches().length - 1)));
  const rows = map(matches, (entry) => entry, { keyed: (entry) => entry.file.path });
  const groups = map(
    () => {
      const byFolder = new Map<string, RouteFile[]>();
      for (const file of files()) {
        const folder = folderOf(file.path);
        byFolder.set(folder, [...(byFolder.get(folder) ?? []), file]);
      }
      const rank = (folder: string): number => {
        const index = folderRank.indexOf(folder);
        return index >= 0 ? index : collapsedByDefault.has(folder) ? 99 : 50;
      };
      return [...byFolder]
        .sort(([left], [right]) => rank(left) - rank(right) || left.localeCompare(right))
        .map(([folder, items]) => ({
          folder,
          items: items.sort((left, right) => left.path.localeCompare(right.path)),
        }));
    },
    (group) => group,
    { keyed: (group) => group.folder },
  );
  const selectedFile = memo(() => files().find((file) => file.path === snapshot().selected));
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
      const file = matches()[active()]?.file;
      if (file) store.go({ file: file.path }, false);
    }
  };
  return (
    <div
      class="files"
      ref={(element) => {
        root = element;
      }}
    >
      <div class="file-pane">
        <label class="file-search">
          <Icon paths={icons.search} />
          <input
            type="search"
            placeholder="Search files…"
            aria-label="Search files"
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
            {!snapshot().route ? "" : query() ? `${matches().length}/${files().length}` : String(files().length)}
          </span>
        </label>
        <div class="file-groups" role="listbox" aria-label="Files">
          <Show when={snapshot().route}>
            <Show
              when={query()}
              fallback={
                <For each={groups()} key={(group) => group().folder}>
                  {(group) => <FileGroupView group={group()} state={props.state} />}
                </For>
              }
            >
              <For
                each={rows()}
                key={(entry) => entry().file.path}
                fallback={<p class="file-empty">{`No file matches “${query()}”.`}</p>}
              >
                {(entry) => (
                  <FileRow
                    file={entry().file}
                    hits={entry().match.hits}
                    cursor={matches()[active()]?.file.path === entry().file.path}
                    showFolder={true}
                    selected={snapshot().selected}
                  />
                )}
              </For>
            </Show>
          </Show>
        </div>
      </div>
      <section class="viewer" aria-live="polite">
        <div class="viewer-head">
          <Show when={snapshot().route}>
            <div class="viewer-title">
              <h2 title={snapshot().selected}>
                <Show when={snapshot().selected.includes("/")}>
                  <span class="dir">{snapshot().selected.slice(0, snapshot().selected.lastIndexOf("/") + 1)}</span>
                </Show>
                {snapshot().selected.slice(snapshot().selected.lastIndexOf("/") + 1)}
              </h2>
              <Show when={selectedFile()}>{(file) => <p>{`${size(file().size)} · ${ago(file().modified)}`}</p>}</Show>
            </div>
            <div class="seg" role="radiogroup" aria-label="View">
              <For each={modes} key={(mode) => mode}>
                {(mode) => (
                  <button
                    role="radio"
                    title={mode === "Raw" ? "Show the Markdown source" : "Show the file formatted"}
                    aria-checked={snapshot().raw === (mode === "Raw") ? "true" : "false"}
                    class={snapshot().raw === (mode === "Raw") ? "active" : ""}
                    onClick={() => props.state.raw(mode === "Raw")}
                  >
                    {mode}
                  </button>
                )}
              </For>
            </div>
            <button
              class="icon-btn"
              title="Copy the file path"
              aria-label="Copy the file path"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(`${snapshot().route?.dir}/${snapshot().selected}`);
                  toast("Copied the file path.");
                } catch {
                  toast("The browser blocked the clipboard.", "error");
                }
              }}
            >
              <Icon paths={icons.copy} />
            </button>
          </Show>
        </div>
        <div class="viewer-body">
          <Show when={snapshot().status === "loaded"}>
            <Show when={snapshot().raw} fallback={<Markdown source={text()} />}>
              <pre class="raw">
                <CodeBlock lines={lines()} lang="md" />
              </pre>
            </Show>
          </Show>
          <Show when={snapshot().status === "loading"}>
            <p class="muted">Loading…</p>
          </Show>
          <Show when={snapshot().status === "error"}>
            <p class="error">{snapshot().error}</p>
            <button type="button" class="btn" onClick={() => props.state.retry()}>
              Try again
            </button>
          </Show>
        </div>
      </section>
    </div>
  );
}
