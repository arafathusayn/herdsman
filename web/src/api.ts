import { createActor } from "@reactre/state";

import type { Host } from "./hosts.ts";
import { registerRequest } from "./state/request-service.ts";
import { streamMachine } from "./state/stream.machine.ts";
import { valueState } from "./state/value.ts";
import type {
  Action,
  ActionResult,
  ArtifactList,
  Catalog,
  Channel,
  ChoicePick,
  DiffSource,
  Entry,
  GitHubAccess,
  InboxState,
  LogLine,
  PaneRead,
  RouteRequest,
  RouteResult,
  State,
} from "./types.ts";

export { AuthError } from "./state/errors.ts";
import { AuthError } from "./state/errors.ts";

export type PairStatus = "paired" | "pairing" | "none";

export interface PairState {
  status: PairStatus;
  error?: string;
  left?: number;
  /** A new pairing request's secret, or a newly paired browser's device secret. */
  pair?: string;
  device?: string;
}

// The host every call goes to. Calls carry secrets in headers, never
// cookies, so the app can talk to a server on another origin.
const [session, setSession] = valueState({ base: "", device: "", pairing: "", generation: 0 });

/** Points every call at a host; its device secret goes with each call. */
export function useHost(h: Host): void {
  let pairing = "";
  try {
    pairing = sessionStorage.getItem(`herdsman.pair ${h.url}`) ?? "";
  } catch {
    /* storage is optional */
  }
  setSession({ base: h.url, device: h.device ?? "", pairing, generation: session().generation + 1 });
}

/** The full URL of a path on the current host. */
export function hostUrl(path: string): string {
  return session().base + path;
}

function setPairing(sec: string): void {
  setSession({ ...session(), pairing: sec });
  try {
    if (sec) sessionStorage.setItem(`herdsman.pair ${session().base}`, sec);
    else sessionStorage.removeItem(`herdsman.pair ${session().base}`);
  } catch {
    // Kept for this page only.
  }
}

function headers(extra: Record<string, string> = {}): Record<string, string> {
  const h: Record<string, string> = { ...extra };
  if (session().device) h["Authorization"] = `Bearer ${session().device}`;
  return h;
}

function call(path: string, init: RequestInit = {}): Promise<Response> {
  return fetch(session().base + path, { ...init, credentials: "omit", cache: "no-store" });
}

/** A host that does not answer in this long counts as unreachable at startup. */
const answerWithin = 8000;

/**
 * Opens a pairing request with the token from a pairing link: the host
 * prints a code in its terminal. A browser the host already knows is told
 * so instead.
 */
export async function startSession(token: string, signal?: AbortSignal): Promise<PairState> {
  const generation = session().generation;
  const res = await call("/api/session", {
    method: "POST",
    headers: headers({ "X-Herdsman-Token": token }),
    signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(answerWithin)]) : AbortSignal.timeout(answerWithin),
  });
  const st = (await res.json()) as PairState;
  if (generation !== session().generation) throw new Error("Host changed during pairing");
  if (st.pair) setPairing(st.pair);
  return st;
}

/** Where this browser stands with the host: paired, pairing, or neither. */
export async function pairStatus(signal?: AbortSignal): Promise<PairState> {
  const h = headers();
  if (session().pairing) h["X-Herdsman-Pair"] = session().pairing;
  const res = await call("/api/pair", {
    headers: h,
    signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(answerWithin)]) : AbortSignal.timeout(answerWithin),
  });
  return (await res.json()) as PairState;
}

/** Sends the code from the terminal; on success the state carries the device secret. */
export async function pair(code: string, signal?: AbortSignal): Promise<PairState> {
  const generation = session().generation;
  const res = await call("/api/pair", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Herdsman-Pair": session().pairing },
    body: JSON.stringify({ code }),
    ...(signal ? { signal } : {}),
  });
  const st = (await res.json()) as PairState;
  signal?.throwIfAborted();
  if (generation !== session().generation) throw new Error("Host changed during pairing");
  if (st.status !== "pairing") setPairing("");
  if (st.device) setSession({ ...session(), device: st.device });
  return st;
}

export interface StreamHandlers {
  state(s: State): void;
  entries(e: Entry[]): void;
  entry(e: Entry): void;
  connection(up: boolean): void;
  unauthorized(): void;
}

/**
 * Splits a server-sent event stream into events; feed it text as it
 * arrives. Comment lines (": ping") are skipped.
 */
export function sseParser(emit: (event: string, data: string) => void): (chunk: string) => void {
  let buf = "";
  return (chunk) => {
    buf += chunk.replace(/\r\n?/g, "\n");
    let end = buf.indexOf("\n\n");
    while (end >= 0) {
      const block = buf.slice(0, end);
      buf = buf.slice(end + 2);
      let event = "message";
      const data: string[] = [];
      for (const line of block.split("\n")) {
        if (line.startsWith(":")) continue;
        const i = line.indexOf(":");
        const field = i < 0 ? line : line.slice(0, i);
        const value = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
        if (field === "event") event = value;
        else if (field === "data") data.push(value);
      }
      if (data.length) emit(event, data.join("\n"));
      end = buf.indexOf("\n\n");
    }
  };
}

/**
 * Opens the server's event stream and reconnects with backoff. It reads
 * the stream with fetch, which can send the bearer header across origins
 * where EventSource cannot.
 */
export function openStream(on: StreamHandlers): { close(): void } {
  const target = session();
  // A response and its reader are transport resources, never application state.
  let response: Response | null = null;
  let connection: AbortController | null = null;
  const dispatch = (event: string, data: string): void => {
    if (event === "state") on.state(JSON.parse(data) as State);
    else if (event === "entries") on.entries((JSON.parse(data) as Entry[] | null) ?? []);
    else if (event === "entry") on.entry(JSON.parse(data) as Entry);
  };
  const service = registerRequest(async (kind, signal) => {
    if (kind === "open") {
      connection?.abort();
      connection = new AbortController();
      const abort = () => connection?.abort();
      signal.addEventListener("abort", abort, { once: true });
      try {
        response = await fetch(target.base + "/api/stream", {
          credentials: "omit",
          cache: "no-store",
          headers: target.device ? { Authorization: `Bearer ${target.device}` } : {},
          signal: connection.signal,
        });
        signal.throwIfAborted();
        if (response.status === 401) return "unauthorized";
        if (!response.ok || !response.body) throw new Error(`stream failed (${response.status})`);
        return "ready";
      } finally {
        signal.removeEventListener("abort", abort);
      }
    }
    if (!response?.body) throw new Error("Stream has no response body");
    const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
    const abort = () => {
      connection?.abort();
      void reader.cancel().catch(() => {});
    };
    signal.addEventListener("abort", abort, { once: true });
    try {
      const feed = sseParser(dispatch);
      for (;;) {
        const { value, done } = await reader.read();
        signal.throwIfAborted();
        if (done) break;
        feed(value);
      }
      return "ended";
    } finally {
      signal.removeEventListener("abort", abort);
      reader.releaseLock();
    }
  });
  const actor = createActor(streamMachine, { input: { service: service.id } });
  const off = actor.subscribe((snapshot) => {
    on.connection(snapshot.state === "streaming");
    if (snapshot.state === "unauthorized") on.unauthorized();
  });
  return {
    close: () => {
      actor.send({ type: "close" });
      off();
      actor.stop();
      service.dispose();
      connection?.abort();
    },
  };
}

export async function act(a: Action, signal?: AbortSignal): Promise<ActionResult> {
  const res = await call("/api/actions", {
    method: "POST",
    headers: headers({ "Content-Type": "application/json" }),
    ...(signal ? { signal } : {}),
    body: JSON.stringify(a),
  });
  if (res.status === 401) throw new AuthError("Session expired: open the pairing link that herdsman web printed.");
  return (await res.json()) as ActionResult;
}

const [etags, setEtags] = valueState(new Map<string, string>());
/** Bumped by forgetPane, so a read still in flight cannot bring back an ETag its panel dropped. */
const [paneGenerations, setPaneGenerations] = valueState(new Map<string, number>());

/**
 * The last lines of a pane with its color codes, or with visible the screen
 * as herdr shows it at its scroll position; null while unchanged since the
 * last read of the same kind.
 */
export async function readPane(
  pane: string,
  visible = false,
  lines = 400,
  signal?: AbortSignal,
): Promise<PaneRead | null> {
  const extra: Record<string, string> = {};
  const key = `${pane} ${visible}`;
  const generation = paneGenerations().get(pane) ?? 0;
  const tag = etags().get(key);
  if (tag) extra["If-None-Match"] = tag;
  const source = visible ? "&source=visible" : "";
  const res = await call(`/api/pane/${encodeURIComponent(pane)}?lines=${lines}&format=ansi${source}`, {
    headers: headers(extra),
    ...(signal ? { signal } : {}),
  });
  if (res.status === 304) return null;
  if (res.status === 401) throw new AuthError("Session expired.");
  const body = (await res.json()) as PaneRead & { error?: string };
  if (!res.ok) throw new Error(body.error ?? `pane read failed (${res.status})`);
  const et = res.headers.get("ETag");
  if (et && (paneGenerations().get(pane) ?? 0) === generation) setEtags(new Map([...etags(), [key, et]]));
  return body;
}

/** Drops a pane's cached ETags, so the next read gets the text again. */
export function forgetPane(pane: string): void {
  setEtags(new Map([...etags()].filter(([key]) => key !== `${pane} false` && key !== `${pane} true`)));
  setPaneGenerations(new Map([...paneGenerations(), [pane, (paneGenerations().get(pane) ?? 0) + 1]]));
}

export async function routeFile(route: string, path: string, signal?: AbortSignal): Promise<string> {
  const q = new URLSearchParams({ route, path });
  const res = await call(`/api/route/file?${q}`, { headers: headers(), ...(signal ? { signal } : {}) });
  if (!res.ok) throw new Error(`cannot read ${path} (${res.status})`);
  return res.text();
}

/** Up to a page of run-state lines ending before line index `before`, and how many precede them. */
export async function routeRunState(
  route: string,
  before: number,
  signal?: AbortSignal,
): Promise<{ lines: LogLine[]; older: number; history: string | undefined }> {
  const res = await call(`/api/route/runstate?${new URLSearchParams({ route, before: String(before) })}`, {
    headers: headers(),
    ...(signal ? { signal } : {}),
  });
  if (res.status === 401) throw new AuthError("Session expired.");
  const body = (await res.json()) as { lines?: LogLine[]; older?: number; history?: string; error?: string };
  if (!res.ok || !body.lines) throw new Error(body.error ?? `run state failed (${res.status})`);
  return { lines: body.lines, older: body.older ?? 0, history: body.history };
}

const [diffs, setDiffs] = valueState(new Map<string, { etag: string; sources: DiffSource[] }>());

/** The code diffs of a route; a repeat request costs a bodyless 304 while nothing changed. */
export async function routeDiff(route: string, signal?: AbortSignal): Promise<DiffSource[]> {
  const cached = diffs().get(route);
  const extra: Record<string, string> = cached ? { "If-None-Match": cached.etag } : {};
  const res = await call(`/api/route/diff?${new URLSearchParams({ route })}`, {
    headers: headers(extra),
    ...(signal ? { signal } : {}),
  });
  if (res.status === 304 && cached) return cached.sources;
  if (res.status === 401) throw new AuthError("Session expired.");
  const body = (await res.json()) as { sources?: DiffSource[]; error?: string };
  if (!res.ok || !body.sources) throw new Error(body.error ?? `diff failed (${res.status})`);
  const etag = res.headers.get("ETag");
  if (etag) setDiffs(new Map([...diffs(), [route, { etag, sources: body.sources }]]));
  return body.sources;
}

/** Tells the server the paired user is here (A33); a failure is ignored. */
export function sendActivity(): void {
  call("/api/activity", { method: "POST", headers: headers() }).catch(() => {});
}

/** The HTML pages in a route's workspace folders. */
export async function listArtifacts(route: string, signal?: AbortSignal): Promise<ArtifactList> {
  const res = await call(`/api/artifacts?${new URLSearchParams({ route })}`, {
    headers: headers(),
    ...(signal ? { signal } : {}),
  });
  if (res.status === 401) throw new AuthError("Session expired.");
  if (!res.ok) throw new Error(`cannot list artifacts (${res.status})`);
  return (await res.json()) as ArtifactList;
}

/** A short-lived URL that shows one of a route's artifacts in a sandboxed frame. */
export async function openArtifact(route: string, root: string, path: string, signal?: AbortSignal): Promise<string> {
  const res = await call("/api/artifacts/open", {
    method: "POST",
    headers: headers({ "Content-Type": "application/json" }),
    ...(signal ? { signal } : {}),
    body: JSON.stringify({ route, root, path }),
  });
  if (res.status === 401) throw new AuthError("Session expired.");
  const body = (await res.json()) as { url?: string; error?: string };
  if (!res.ok || !body.url) throw new Error(body.error ?? `cannot open ${path} (${res.status})`);
  return session().base + body.url;
}

/** The adjutant's message channel, as `message status` sees it. */
export async function readChannel(signal?: AbortSignal): Promise<Channel> {
  const res = await call("/api/messages", { headers: headers(), ...(signal ? { signal } : {}) });
  if (res.status === 401) throw new AuthError("Session expired.");
  if (!res.ok) throw new Error(`cannot read messages (${res.status})`);
  return (await res.json()) as Channel;
}

/** One verified message's body. */
export async function messageBody(box: string, id: string, signal?: AbortSignal): Promise<string> {
  const res = await call(`/api/messages/${encodeURIComponent(box)}/${encodeURIComponent(id)}`, {
    headers: headers(),
    ...(signal ? { signal } : {}),
  });
  if (res.status === 401) throw new AuthError("Session expired.");
  const body = (await res.json()) as { body?: string; error?: string };
  if (!res.ok || body.body === undefined) throw new Error(body.error ?? `cannot read ${id} (${res.status})`);
  return body.body;
}

/** The harnesses a new route can start, with their models and efforts. */
export async function catalog(probe = true, signal?: AbortSignal): Promise<Catalog> {
  const res = await call(probe ? "/api/catalog" : "/api/catalog?probe=0", {
    headers: headers(),
    ...(signal ? { signal } : {}),
  });
  if (res.status === 401) throw new AuthError("Session expired.");
  if (!res.ok) throw new Error(`cannot read the harness list (${res.status})`);
  return (await res.json()) as Catalog;
}

/** Each role's saved pick, read without asking any harness. */
export async function savedChoices(signal?: AbortSignal): Promise<ChoicePick[]> {
  const res = await call("/api/choices", { headers: headers(), ...(signal ? { signal } : {}) });
  if (res.status === 401) throw new AuthError("Session expired.");
  if (!res.ok) throw new Error(`cannot read the saved choices (${res.status})`);
  return ((await res.json()) as { picks: ChoicePick[] }).picks ?? [];
}

/** The gh accounts that can read the folder's GitHub repository, with the one to confirm. */
export async function githubAccess(folder: string, signal?: AbortSignal): Promise<GitHubAccess> {
  const res = await call(`/api/routes/github?folder=${encodeURIComponent(folder)}`, {
    headers: headers(),
    ...(signal ? { signal } : {}),
  });
  if (res.status === 401) throw new AuthError("Session expired.");
  const body = (await res.json()) as Partial<GitHubAccess>;
  if (!res.ok) throw new Error(body.error ?? `cannot probe GitHub (${res.status})`);
  return { repo: "", accounts: [], suggested: "", ...body };
}

/** Creates a route: a refusal or a failed step comes back in the result, not as a throw. */
export async function createRoute(r: RouteRequest, signal?: AbortSignal): Promise<RouteResult> {
  const res = await call("/api/routes", {
    method: "POST",
    headers: headers({ "Content-Type": "application/json" }),
    ...(signal ? { signal } : {}),
    body: JSON.stringify(r),
  });
  if (res.status === 401) throw new AuthError("Session expired: open the pairing link that herdsman web printed.");
  const body = (await res.json()) as Partial<RouteResult>;
  return { ok: false, steps: [], tabs: [], ...body };
}

/** What the setup checklist can read from a route's private inbox. */
export async function inboxState(
  path: string,
  adjutant: string,
  orchestrator: string,
  signal?: AbortSignal,
): Promise<InboxState> {
  const q = new URLSearchParams({ path, adjutant, orchestrator });
  const res = await call(`/api/routes/inbox?${q}`, { headers: headers(), ...(signal ? { signal } : {}) });
  if (res.status === 401) throw new AuthError("Session expired.");
  const body = (await res.json()) as InboxState;
  if (!res.ok) throw new Error(body.error ?? `cannot read the inbox (${res.status})`);
  return body;
}
