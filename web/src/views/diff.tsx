import { For, Match, memo, onCleanup, Show, Switch } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { AuthError, routeDiff } from "../api.ts";
import { Highlight } from "../components/highlight.tsx";
import { Icon } from "../components/icons.tsx";
import { parseSection, splitPatch } from "../diff.ts";
import { fuzzy } from "../fuzzy.ts";
import { icons } from "../icons.ts";
import { createPoll } from "../state/poll.ts";
import { createRequest } from "../state/request.ts";
import { valueState } from "../state/value.ts";
import { errorMessage } from "../state/wire.ts";
import { store } from "../store.ts";
import type { DiffFile, DiffSource } from "../types.ts";
import { toast } from "../ui.ts";
import { DiffTable } from "./diff-code.tsx";

const pollMs = 5000;
type Layout = "inline" | "split";

const layoutKey = "herdsman.diff-layout";
/** A viewer narrower than this has no room for two columns of code: the diff shows inline whatever the choice. */
const splitMin = 640;

function loadLayout(): Layout {
  try {
    return localStorage.getItem(layoutKey) === "split" ? "split" : "inline";
  } catch {
    return "inline";
  }
}

const [layout, setLayout] = valueState(loadLayout());

const statusName: Record<string, string> = {
  A: "Added",
  M: "Modified",
  D: "Deleted",
  R: "Renamed",
  C: "Copied",
  T: "Type changed",
  "?": "Untracked",
};

function totals(files: DiffFile[]): { add: number; del: number } {
  let add = 0;
  let del = 0;
  for (const f of files) {
    add += f.add;
    del += f.del;
  }
  return { add, del };
}

interface DiffSnapshot {
  sources: DiffSource[] | null;
  error: string;
  sourceId: string;
  file: string;
  query: string;
  narrow: boolean;
  showAll: boolean;
}
export interface DiffState {
  routeId: string;
  state: () => DiffSnapshot;
  update: (source: string, file: string) => void;
  query: (query: string) => void;
  narrow: (narrow: boolean) => void;
  all: () => void;
  start: () => void;
  stop: () => void;
  search: (input: HTMLInputElement | null) => void;
  focusSearch: () => void;
}

export function createDiffState(routeId: string): DiffState {
  const [local, setState] = valueState<Omit<DiffSnapshot, "sources" | "error">>({
    sourceId: "",
    file: "",
    query: "",
    narrow: false,
    showAll: false,
  });
  const request = createRequest(async (_, signal) => routeDiff(routeId, signal));
  const state = (): DiffSnapshot => ({
    ...local(),
    sources: request.data() ?? null,
    error: request.error() === undefined ? "" : errorMessage(request.error()),
  });
  const set = (patch: Partial<Omit<DiffSnapshot, "sources" | "error">>): void =>
    setState((current) => ({ ...current, ...patch }));
  let search: HTMLInputElement | null = null;
  const poll = createPoll(async (signal) => {
    if (document.visibilityState !== "visible") return pollMs;
    const outcome = await request.run(undefined);
    if (!signal.aborted && outcome.status === "failed" && outcome.error instanceof AuthError) location.reload();
    return pollMs;
  }, pollMs);
  return {
    routeId,
    state,
    update: (sourceId, file) => {
      if (state().sourceId !== sourceId || state().file !== file) set({ sourceId, file, showAll: false });
    },
    query: (query) => {
      if (state().query !== query) set({ query });
    },
    narrow: (narrow) => {
      if (state().narrow !== narrow) set({ narrow });
    },
    all: () => set({ showAll: true }),
    start: poll.start,
    stop: () => {
      poll.stop();
      request.cancel();
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

function sourceOf(state: DiffSnapshot): DiffSource | null {
  const sources = state.sources ?? [];
  return (
    sources.find((source) => source.id === state.sourceId) ??
    sources.find((source) => (source.diff?.files?.length ?? 0) > 0) ??
    sources[0] ??
    null
  );
}

function Counts(props: { add: number; del: number }) {
  return (
    <span class="counts">
      <span class="plus">{`+${props.add}`}</span>
      <span class="minus">{`−${props.del}`}</span>
    </span>
  );
}

function SourceRow(props: { source: DiffSource; current: string }) {
  const files = memo(() => props.source.diff?.files ?? []);
  const total = memo(() => totals(files()));
  const agents = memo(() => [...new Set(props.source.agents ?? [])]);
  return (
    <button
      class={props.source.id === props.current ? "diff-source active" : "diff-source"}
      role="option"
      aria-selected={props.source.id === props.current ? "true" : "false"}
      title={props.source.dir ?? props.source.label}
      onClick={() => store.go({ source: props.source.id, diffFile: "" }, false)}
    >
      <span class="diff-source-top">
        <strong>{props.source.label}</strong>
        <Show when={props.source.error} fallback={<Counts add={total().add} del={total().del} />}>
          <span class="chip warn">unavailable</span>
        </Show>
      </span>
      <Show when={agents().length > 0}>
        <span class="diff-source-agents" title="Agents working in this folder">
          <For each={agents()} key={(agent) => agent}>
            {(agent) => <span class="who">{agent}</span>}
          </For>
        </span>
      </Show>
      <span class="diff-source-sub">
        <Icon paths={props.source.kind === "checkout" ? icons.folder : icons.branch} />
        {props.source.diff
          ? `${props.source.diff.head} ← ${props.source.diff.base}`
          : (props.source.branch ?? props.source.dir ?? "")}
        <Show when={props.source.diff}>
          <span class="n">{`${files().length} ${files().length === 1 ? "file" : "files"}`}</span>
        </Show>
      </span>
    </button>
  );
}

function FileRow(props: { file: DiffFile; hits: number[]; source: string; current: string }) {
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
      class={props.file.path === props.current ? "file-row active" : "file-row"}
      role="option"
      aria-selected={props.file.path === props.current ? "true" : "false"}
      title={props.file.old_path ? `${props.file.old_path} → ${props.file.path}` : props.file.path}
      onClick={() => store.go({ diffFile: props.file.path, source: props.source }, false)}
    >
      <span
        class={`st st-${props.file.status === "?" ? "u" : props.file.status}`}
        title={statusName[props.file.status] ?? props.file.status}
      >
        {props.file.status}
      </span>
      <span class="file-name">
        <span class="name">
          <Highlight value={data().name} hits={data().nameHits} />
        </span>
        <Show when={data().dir}>
          <span class="dir">
            <Highlight value={data().dir} hits={data().dirHits} />
          </span>
        </Show>
      </span>
      <Show when={!props.file.binary && props.file.status !== "?"}>
        <Counts add={props.file.add} del={props.file.del} />
      </Show>
    </button>
  );
}

const layouts: readonly { id: Layout; label: string; tip: string }[] = [
  {
    id: "inline",
    label: "Inline",
    tip: "Removed and added lines in one column",
  },
  {
    id: "split",
    label: "Side by side",
    tip: "The old file on the left, the new one on the right",
  },
];
function LayoutSwitch() {
  const choose = (value: Layout): void => {
    setLayout(value);
    try {
      localStorage.setItem(layoutKey, value);
    } catch {
      /* Kept for this page. */
    }
  };
  return (
    <div class="view-switch diff-layout" role="group" aria-label="Diff layout">
      <For each={layouts} key={(option) => option.id}>
        {(option) => (
          <button
            type="button"
            class={option.id === layout() ? "active" : ""}
            aria-pressed={option.id === layout() ? "true" : "false"}
            title={option.tip}
            onClick={() => choose(option.id)}
          >
            {option.label}
          </button>
        )}
      </For>
    </div>
  );
}

function FileHead(props: { file: DiffFile; source: DiffSource; narrow: boolean }) {
  const noLines = memo(() => props.file.binary || props.file.status === "?");
  return (
    <>
      <div class="viewer-title">
        <h2 title={props.file.path}>
          <Show when={props.file.path.includes("/")}>
            <span class="dir">{props.file.path.slice(0, props.file.path.lastIndexOf("/") + 1)}</span>
          </Show>
          {props.file.path.slice(props.file.path.lastIndexOf("/") + 1)}
        </h2>
        <p>
          {statusName[props.file.status] ?? props.file.status}
          {props.file.old_path ? ` from ${props.file.old_path}` : ""}
          <Show when={!noLines()}>
            {" · "}
            <Counts add={props.file.add} del={props.file.del} />
          </Show>
        </p>
      </div>
      <Show when={!noLines() && !props.narrow}>
        <LayoutSwitch />
      </Show>
      <button
        class="icon-btn"
        title="Copy the file path"
        aria-label="Copy the file path"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(
              props.source.dir ? `${props.source.dir}/${props.file.path}` : props.file.path,
            );
            toast("Copied the file path.");
          } catch {
            toast("The browser blocked the clipboard.", "error");
          }
        }}
      >
        <Icon paths={icons.copy} />
      </button>
    </>
  );
}

const cutNote =
  "The diff reached its size cut in this file, so lines past this point are not shown. Open the file in the worktree to see all of it.";

export function DiffView(props: { state: DiffState }) {
  const snapshot = props.state.state;
  const source = memo(() => sourceOf(snapshot()));
  const files = memo(() =>
    [...(source()?.diff?.files ?? [])].sort((left, right) => left.path.localeCompare(right.path)),
  );
  const selected = memo(() => files().find((file) => file.path === snapshot().file) ?? files()[0] ?? null);
  const query = memo(() => snapshot().query.trim());
  const matches = memo(() =>
    files()
      .map((file) => ({
        file,
        match: query() ? fuzzy(query(), file.path) : { hits: [], score: 0 },
      }))
      .filter(
        (
          entry,
        ): entry is {
          file: DiffFile;
          match: NonNullable<ReturnType<typeof fuzzy>>;
        } => !!entry.match,
      )
      .sort((left, right) => right.match.score - left.match.score),
  );
  const sources = map(
    () => snapshot().sources ?? [],
    (source) => source,
    { keyed: (source) => source.id },
  );
  const rows = map(matches, (entry) => entry, {
    keyed: (entry) => entry.file.path,
  });
  const patch = memo(() => source()?.diff?.patch ?? "");
  const sections = memo(() => splitPatch(patch()));
  const section = memo(() => sections().get(selected()?.path ?? ""));
  const lines = memo(() => {
    const value = section();
    return value ? parseSection(value) : [];
  });
  const cut = memo(() => !!source()?.diff?.truncated && section() === [...sections().values()].at(-1));
  let search!: HTMLInputElement;
  let observer: ResizeObserver | undefined;
  onCleanup(() => {
    observer?.disconnect();
    props.state.search(null);
  });
  const noLines = memo(() => {
    const file = selected();
    if (!section() && source()?.diff?.truncated)
      return "This file is past the size cut of the diff; open it in the worktree to see its lines.";
    if (file?.binary) return "Binary file; no lines to show.";
    if (file?.old_path) return `Moved from ${file.old_path} with no line changes.`;
    return "No line changes; only the file mode changed.";
  });
  return (
    <div class="files diff">
      <div class="file-pane">
        <div class="diff-sources" role="listbox" aria-label="Changes">
          <Show
            when={snapshot().sources}
            fallback={<p class="file-empty">{snapshot().error || "Reading the changes…"}</p>}
          >
            <For each={sources()} key={(source) => source().id}>
              {(item) => <SourceRow source={item()} current={source()?.id ?? ""} />}
            </For>
          </Show>
        </div>
        <label class="file-search">
          <Icon paths={icons.search} />
          <input
            type="search"
            placeholder="Search changed files…"
            aria-label="Search changed files"
            autocomplete="off"
            spellcheck="false"
            value={snapshot().query}
            ref={(element) => {
              search = element;
              props.state.search(element);
            }}
            onInput={() => props.state.query(search.value)}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                if (search.value) {
                  event.preventDefault();
                  props.state.query("");
                } else search.blur();
              } else if (event.key === "Enter") {
                const first = matches()[0]?.file;
                if (first) store.go({ diffFile: first.path, source: source()?.id ?? "" }, false);
              }
            }}
          />
          <span class="files-count">
            {!source() || source()?.error || !files().length
              ? ""
              : query()
                ? `${matches().length}/${files().length}`
                : String(files().length)}
          </span>
        </label>
        <div class="file-groups" role="listbox" aria-label="Changed files">
          <Show when={source() && !source()?.error}>
            <For
              each={rows()}
              key={(entry) => entry().file.path}
              fallback={
                <p class="file-empty">
                  {query() && files().length ? `No changed file matches “${query()}”.` : "No changed files."}
                </p>
              }
            >
              {(entry) => (
                <FileRow
                  file={entry().file}
                  hits={entry().match.hits}
                  current={selected()?.path ?? ""}
                  source={source()?.id ?? ""}
                />
              )}
            </For>
          </Show>
        </div>
      </div>
      <section
        class="viewer"
        aria-live="polite"
        ref={(viewer) => {
          observer = new ResizeObserver(([entry]) => props.state.narrow((entry?.contentRect.width ?? 0) < splitMin));
          observer.observe(viewer);
        }}
      >
        <div class="viewer-head">
          <Show when={source()}>
            {(source) => (
              <Show
                when={!source().error && source().diff ? selected() : null}
                fallback={
                  <div class="viewer-title">
                    <h2 class="plain">{source().label}</h2>
                  </div>
                }
              >
                {(file) => <FileHead file={file()} source={source()} narrow={snapshot().narrow} />}
              </Show>
            )}
          </Show>
        </div>
        <div class="viewer-body diff-body">
          <Switch
            fallback={
              <>
                <Show when={cut()}>
                  <p class="diff-cut" role="note">
                    {cutNote}
                  </p>
                </Show>
                <DiffTable
                  lines={lines()}
                  path={selected()?.path ?? ""}
                  split={!snapshot().narrow && layout() === "split"}
                  all={snapshot().showAll}
                  showAll={() => props.state.all()}
                />
              </>
            }
          >
            <Match when={!source()}>
              <p class="muted">{snapshot().error || "Reading the changes…"}</p>
            </Match>
            <Match when={source()?.error || !source()?.diff}>
              <p class="error">{source()?.error ?? "No diff."}</p>
              <Show when={source()?.dir}>
                <p class="muted">{`Folder: ${source()?.dir}`}</p>
              </Show>
            </Match>
            <Match when={!selected()}>
              <p class="muted">{`No changes against ${source()?.diff?.base}.`}</p>
            </Match>
            <Match when={selected()?.status === "?"}>
              <p class="muted">Untracked file: git has no diff for it until it is added.</p>
            </Match>
            <Match when={!lines().length}>
              <p class="muted">{noLines()}</p>
            </Match>
          </Switch>
        </div>
      </section>
    </div>
  );
}
