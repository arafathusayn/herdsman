import { batch, memo } from "@reactre/core";

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
import { createRequest } from "../state/request.ts";
import { valueState } from "../state/value.ts";
import type { Catalog, GitHubAccess, InboxState, RouteRequest, RouteResult, State } from "../types.ts";

export interface Deps {
  createRoute(request: RouteRequest, signal?: AbortSignal): Promise<RouteResult>;
  inboxState(path: string, adjutant: string, orchestrator: string, signal?: AbortSignal): Promise<InboxState>;
  state(): State | null;
  copy(text: string, what: string): void;
  openPane(pane: string): void;
  close(): void;
  failed(error: unknown): void;
  every(ms: number, fn: () => void): () => void;
  later(ms: number, fn: () => void): () => void;
  github?(folder: string, signal?: AbortSignal): Promise<GitHubAccess>;
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

/** Draft data belongs to a value actor; request phases and results are derived. */
export function createRouteFormState(initial: Catalog, session: State | null, deps: Deps, options: FormOptions = {}) {
  let draft = newForm(initial, session);
  if (options.recommended === false) draft = setRecommended(draft, initial, false);
  if (options.usePrevious) draft = usePrevious(draft, initial);
  const [draftState, setState] = valueState<
    Omit<FormSnapshot, "busy" | "github" | "githubPending" | "result" | "inbox">
  >({
    catalog: initial,
    draft,
    errors: options.usePrevious ? missingPicks(draft, initial) : {},
    submitted: null,
  });
  const set = (patch: Partial<ReturnType<typeof draftState>>): void =>
    setState((current) => ({ ...current, ...patch }));
  const active = (): RouteForm => state().submitted ?? state().draft;
  const [lifecycle, setLifecycle] = valueState({ stopped: false, started: false, githubFolder: "" });
  const githubRequest = createRequest(async (folder: string, signal) => deps.github!(folder, signal));
  const inboxRequest = createRequest(async (draft: RouteForm, signal) => ({
    key: inboxKey(draft),
    state: await deps.inboxState(draft.inbox, draft.name, draft.orchName, signal),
  }));
  const submission = createRequest(async (request: RouteRequest, signal) => deps.createRoute(request, signal));
  const state = memo<FormSnapshot>(() => {
    const result = submission.data();
    return {
      ...draftState(),
      busy: submission.loading(),
      github: githubRequest.data() ?? null,
      githubPending: githubRequest.loading(),
      inbox: inboxRequest.error() ? null : (inboxRequest.data() ?? null),
      result: result?.code === "invalid_route" || result?.code === "bad_request" ? null : (result ?? null),
    };
  });
  let stopPoll = (): void => {},
    cancelDebounce = (): void => {};
  const probeGitHub = (): void => {
    const folder = state().draft.folder.trim();
    if (lifecycle().stopped || !deps.github || folder === lifecycle().githubFolder) return;
    setLifecycle({ ...lifecycle(), githubFolder: folder });
    githubRequest.clear();
    const pending = folder.startsWith("/");
    set({ draft: { ...state().draft, ghAccount: "" } });
    if (!pending) return;
    void githubRequest.run(folder);
  };
  const readInbox = (): void => {
    if (lifecycle().stopped) return;
    probeGitHub();
    const draft = active();
    if (draft.role !== "adjutant") {
      inboxRequest.clear();
      return;
    }
    void inboxRequest.run(draft);
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
      if (lifecycle().started || lifecycle().stopped) return;
      setLifecycle({ ...lifecycle(), started: true });
      stopPoll = deps.every(INBOX_POLL_MS, readInbox);
      readInbox();
    },
    stop(): void {
      if (lifecycle().stopped) return;
      setLifecycle({ ...lifecycle(), stopped: true });
      githubRequest.stop();
      inboxRequest.stop();
      submission.stop();
      stopPoll();
      cancelDebounce();
    },
    change(change: Partial<RouteForm>): void {
      if (state().busy || lifecycle().stopped) return;
      const before = inboxKey(state().draft);
      batch(() => {
        set({
          draft: update(state().draft, change),
          errors: clearErrors(...Object.keys(change)),
        });
        if (change.folder !== undefined) probeGitHub();
      });
      if (inboxKey(state().draft) !== before) {
        cancelDebounce();
        cancelDebounce = deps.later(INBOX_DEBOUNCE_MS, readInbox);
      }
    },
    harness(which: "main" | "orch", change: HarnessChange): void {
      if (state().busy || lifecycle().stopped) return;
      set({
        draft: setHarness(state().draft, state().catalog, which, change),
        errors: clearErrors(which, ...(which === "main" ? ["orch"] : [])),
      });
    },
    role(role: RouteForm["role"]): void {
      if (state().busy || lifecycle().stopped) return;
      set({ draft: setRole(state().draft, role, state().catalog) });
      if (state().catalog.from_picks && Object.keys(missingPicks(state().draft, state().catalog)).length)
        options.needLists?.();
      readInbox();
    },
    recommended(value: boolean): void {
      if (state().busy || lifecycle().stopped) return;
      set({ draft: setRecommended(state().draft, state().catalog, value) });
      readInbox();
    },
    previous(): void {
      if (state().busy || lifecycle().stopped) return;
      const draft = usePrevious(state().draft, state().catalog);
      set({
        draft,
        errors: {
          ...clearErrors("main", "orch"),
          ...missingPicks(draft, state().catalog),
        },
      });
    },
    setCatalog(next: Catalog): void {
      if (lifecycle().stopped) return;
      const catalog = {
        ...next,
        previous: next.previous ?? state().catalog.previous ?? [],
      };
      const draft = freshFromLists(state().draft, catalog);
      set({
        catalog,
        draft,
        errors: {
          ...clearErrors("main", "orch"),
          ...missingPicks(draft, catalog),
        },
      });
    },
    back(): void {
      if (!lifecycle().stopped) {
        submission.clear();
        set({ submitted: null });
      }
    },
    submit(event: SubmitEvent): void {
      const answer = event.submitter?.getAttribute("value");
      if (answer === "cancel" || answer === "ok") return;
      event.preventDefault();
      if (state().busy || lifecycle().stopped || blocked()) return;
      const errors = validate(state().draft, state().catalog, deps.state());
      if (Object.keys(errors).length) {
        set({ errors });
        return;
      }
      const draft = state().draft;
      set({ submitted: draft, errors });
      void submission.run(toRequest(draft, state().catalog)).then((outcome) => {
        if (outcome.status === "cancelled") return;
        if (outcome.status === "failed") {
          set({ submitted: null });
          deps.failed(outcome.error);
          return;
        }
        const result = outcome.value;
        if (result.code === "invalid_route" || result.code === "bad_request") {
          set({
            submitted: null,
            errors: { [formField(result.field) || "request"]: result.error ?? "Refused." },
          });
          return;
        }
        readInbox();
      });
    },
  };
}
export type FormController = ReturnType<typeof createRouteFormState>;
