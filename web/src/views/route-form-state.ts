import { batch, signal } from "@reactre/core";

import {
  accountOptions,
  formField,
  freshFromLists,
  type HarnessChange,
  inboxKey,
  missingPicks,
  newForm,
  type RouteForm,
  setHarness,
  setRecommended,
  setRole,
  toRequest,
  update,
  usePrevious,
  validate,
} from "../new-route.ts";
import type { Catalog, GitHubAccess, InboxState, RouteRequest, RouteResult, State } from "../types.ts";

export interface Deps {
  createRoute(request: RouteRequest): Promise<RouteResult>;
  inboxState(path: string, adjutant: string, orchestrator: string): Promise<InboxState>;
  state(): State | null;
  copy(text: string, what: string): void;
  openPane(pane: string): void;
  close(): void;
  failed(error: unknown): void;
  every(ms: number, fn: () => void): () => void;
  later(ms: number, fn: () => void): () => void;
  github?(folder: string): Promise<GitHubAccess>;
}
export interface FormOptions {
  usePrevious?: boolean;
  recommended?: boolean;
  needLists?: (() => void) | undefined;
}
export const INBOX_POLL_MS = 5000;
export const INBOX_DEBOUNCE_MS = 400;
export type TextKey = "folder" | "name" | "orchName" | "routeName" | "inbox" | "minutes" | "limit";

interface FormSnapshot {
  catalog: Catalog;
  draft: RouteForm;
  errors: Record<string, string>;
  busy: boolean;
  github: GitHubAccess | null;
  githubPending: boolean;
  submitted: RouteForm | null;
  result: RouteResult | null;
  inbox: { key: string; state: InboxState } | null;
}

/** Drafts and asynchronous results are immutable signal snapshots, independent of the DOM. */
export function createRouteFormState(initial: Catalog, session: State | null, deps: Deps, options: FormOptions = {}) {
  let draft = newForm(initial, session);
  if (options.recommended === false) draft = setRecommended(draft, initial, false);
  if (options.usePrevious) draft = usePrevious(draft, initial);
  const [state, setState] = signal<FormSnapshot>({
    catalog: initial,
    draft,
    errors: options.usePrevious ? missingPicks(draft, initial) : {},
    busy: false,
    github: null,
    githubPending: false,
    submitted: null,
    result: null,
    inbox: null,
  });
  const set = (patch: Partial<FormSnapshot>): void => setState((current) => ({ ...current, ...patch }));
  const active = (): RouteForm => state().submitted ?? state().draft;
  let stopped = false,
    started = false,
    githubFolder = "",
    sequence = 0,
    githubSequence = 0;
  let stopPoll = (): void => {},
    cancelDebounce = (): void => {};
  const probeGitHub = (): void => {
    const folder = state().draft.folder.trim();
    if (stopped || !deps.github || folder === githubFolder) return;
    githubFolder = folder;
    const request = ++githubSequence;
    const pending = folder.startsWith("/");
    set({ github: null, githubPending: pending, draft: { ...state().draft, ghAccount: "" } });
    if (!pending) return;
    void deps.github(folder).then(
      (github) => {
        if (!stopped && request === githubSequence) set({ github, githubPending: false });
      },
      () => {
        if (!stopped && request === githubSequence) set({ githubPending: false });
      },
    );
  };
  const readInbox = (): void => {
    if (stopped) return;
    probeGitHub();
    const draft = active();
    if (draft.role !== "adjutant") return;
    const key = inboxKey(draft),
      request = ++sequence;
    void deps.inboxState(draft.inbox, draft.name, draft.orchName).then(
      (inbox) => {
        if (!stopped && request === sequence && key === inboxKey(active())) set({ inbox: { key, state: inbox } });
      },
      () => {
        if (!stopped && request === sequence) set({ inbox: null });
      },
    );
  };
  const clearErrors = (...names: string[]): Record<string, string> =>
    Object.fromEntries(Object.entries(state().errors).filter(([name]) => !names.includes(name)));
  const blocked = (): string => {
    if (!deps.github) return "";
    if (state().githubPending) return "Checking which GitHub account can read this repository…";
    const github = state().github;
    return github && accountOptions(github).length && !state().draft.ghAccount ? "Confirm the GitHub account." : "";
  };
  return {
    state,
    active,
    blocked,
    session,
    deps,
    options,
    start(): void {
      if (started || stopped) return;
      started = true;
      stopPoll = deps.every(INBOX_POLL_MS, readInbox);
      readInbox();
    },
    stop(): void {
      if (stopped) return;
      stopped = true;
      sequence++;
      githubSequence++;
      stopPoll();
      cancelDebounce();
    },
    change(change: Partial<RouteForm>): void {
      if (state().busy || stopped) return;
      const before = inboxKey(state().draft);
      batch(() => {
        set({ draft: update(state().draft, change), errors: clearErrors(...Object.keys(change)) });
        if (change.folder !== undefined) probeGitHub();
      });
      if (inboxKey(state().draft) !== before) {
        cancelDebounce();
        cancelDebounce = deps.later(INBOX_DEBOUNCE_MS, readInbox);
      }
    },
    harness(which: "main" | "orch", change: HarnessChange): void {
      if (state().busy || stopped) return;
      set({
        draft: setHarness(state().draft, state().catalog, which, change),
        errors: clearErrors(which, ...(which === "main" ? ["orch"] : [])),
      });
    },
    role(role: RouteForm["role"]): void {
      if (state().busy || stopped) return;
      set({ draft: setRole(state().draft, role, state().catalog) });
      if (state().catalog.from_picks && Object.keys(missingPicks(state().draft, state().catalog)).length)
        options.needLists?.();
      readInbox();
    },
    recommended(value: boolean): void {
      if (state().busy || stopped) return;
      set({ draft: setRecommended(state().draft, state().catalog, value) });
      readInbox();
    },
    previous(): void {
      if (state().busy || stopped) return;
      const draft = usePrevious(state().draft, state().catalog);
      set({ draft, errors: { ...clearErrors("main", "orch"), ...missingPicks(draft, state().catalog) } });
    },
    setCatalog(next: Catalog): void {
      if (stopped) return;
      const catalog = { ...next, previous: next.previous ?? state().catalog.previous ?? [] };
      const draft = freshFromLists(state().draft, catalog);
      set({ catalog, draft, errors: { ...clearErrors("main", "orch"), ...missingPicks(draft, catalog) } });
    },
    back(): void {
      if (!stopped) set({ busy: false, submitted: null, result: null });
    },
    submit(event: SubmitEvent): void {
      const answer = event.submitter?.getAttribute("value");
      if (answer === "cancel" || answer === "ok") return;
      event.preventDefault();
      if (state().busy || stopped || blocked()) return;
      const errors = validate(state().draft, state().catalog, deps.state());
      if (Object.keys(errors).length) {
        set({ errors });
        return;
      }
      const draft = state().draft;
      set({ busy: true, submitted: draft, errors });
      void deps.createRoute(toRequest(draft, state().catalog)).then(
        (result) => {
          if (stopped) return;
          if (result.code === "invalid_route" || result.code === "bad_request") {
            set({
              busy: false,
              submitted: null,
              errors: { [formField(result.field) || "request"]: result.error ?? "Refused." },
            });
            return;
          }
          set({ busy: false, result });
          readInbox();
        },
        (error: unknown) => {
          if (stopped) return;
          set({ busy: false, submitted: null });
          deps.failed(error);
        },
      );
    },
  };
}
export type FormController = ReturnType<typeof createRouteFormState>;
