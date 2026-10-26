// A mock herdsman API that serves the built app: two workspaces, two agents,
// a live route with a checklist, a run log and two task panes, and a stream
// the checks can push state changes and activity events into.
import { join } from "node:path";

type Status = "idle" | "working" | "blocked" | "done" | "unknown";

const ws = (workspace_id: string, number: number, label: string) => ({
  workspace_id,
  number,
  label,
  focused: false,
  active_tab_id: "",
  agent_status: "working",
});
const pane = (pane_id: string, workspace_id: string, tab_id: string, cwd: string | null) => ({
  pane_id,
  workspace_id,
  tab_id,
  focused: false,
  label: null,
  cwd,
  foreground_cwd: null,
  terminal_title_stripped: null,
  agent: null,
  agent_status: "idle",
  revision: 0,
});
const agent = (pane_id: string, name: string, agent_status: Status) => ({
  pane_id,
  tab_id: "t1",
  workspace_id: "w1",
  name,
  agent: "claude",
  display_agent: "Claude",
  agent_status,
  focused: false,
  cwd: null,
  terminal_title_stripped: null,
  interactive_ready: true,
  launch_pending: false,
  state_change_seq: 0,
});
const task = (pane: string, name: string) => ({
  task: `${name}'s task`,
  branch: `b-${name}`,
  worktree: "",
  agent: name,
  pane,
  contract: "",
});
const route = (id: string, live: boolean, extra: Record<string, unknown> = {}) => ({
  id,
  dir: `/code/proj/.herdsman/${id}`,
  name: id,
  title: "",
  orchestrator_pane: live ? "p1" : "",
  tasks: null,
  checklist: null,
  run_state: null,
  files: null,
  modified: "2026-10-27T00:00:00Z",
  live,
  ...extra,
});

/** One activity event, `n` minutes after 09:00 on 2026-10-27. */
export function entry(n: number, text = `note ${n}`) {
  const at = new Date(Date.UTC(2026, 9, 27, 9, n)).toISOString();
  return { at, source: "web" as const, kind: "web.note", text };
}

const catalog = {
  today: "2026-10-27",
  home: "/Users/me",
  checkpointer_ok: true,
  kinds: [
    {
      kind: "claude",
      label: "Claude",
      models: [{ id: "fable", label: "fable", efforts: ["low", "medium", "high"], default_effort: "medium" }],
      default_model: "fable",
      other_model: true,
      free_model: false,
      checkpointer: true,
      skill_prefix: "/",
      skill_installed: true,
      offline: false,
    },
  ],
};

export interface Mock {
  base: string;
  /** What GET /api/pair answers. */
  mode: "paired" | "pairing";
  /** Whether GET /api/catalog answers (else 404, and the New route dialog shows its failure). */
  catalog: boolean;
  /** Changes an agent's status and pushes the new state to every open stream. */
  setStatus(pane: string, status: Status): void;
  /** Pushes one activity event to every open stream. */
  push(e: ReturnType<typeof entry>): void;
  stop(): void;
}

export function startMock(dist: string): Mock {
  const statuses: Record<string, Status> = { p1: "working", p2: "working" };
  const state = () => ({
    herdr: { version: "0.9", protocol: 1 },
    snapshot: {
      version: "0.9",
      protocol: 1,
      focused_workspace_id: null,
      focused_tab_id: null,
      focused_pane_id: null,
      workspaces: [ws("w1", 1, "main"), ws("w2", 2, "")],
      tabs: [
        {
          tab_id: "t1",
          workspace_id: "w1",
          number: 1,
          label: "work",
          focused: false,
          pane_count: 3,
          agent_status: "working",
        },
        {
          tab_id: "t2",
          workspace_id: "w2",
          number: 1,
          label: "shell",
          focused: false,
          pane_count: 1,
          agent_status: "idle",
        },
      ],
      panes: [pane("p1", "w1", "t1", null), pane("p2", "w1", "t1", null), pane("p3", "w2", "t2", "/tmp")],
      layouts: null,
      agents: [agent("p1", "orch", statuses["p1"]!), agent("p2", "im-1", statuses["p2"]!)],
    },
    routes: [
      route("auth", true, {
        title: "Auth work",
        tasks: [task("p2", "im-1")],
        checklist: [
          { done: true, id: "A1", text: "login" },
          { done: false, skipped: true, id: "", text: "sso" },
          { done: false, id: "A3", text: "logout" },
        ],
        run_state: [{ time: "10:00", text: "started" }],
      }),
      route("old", false),
    ],
  });
  // Enough activity to scroll the Activity tab.
  const entries = Array.from({ length: 120 }, (_, i) => entry(i));
  const streams = new Set<ReadableStreamDefaultController<Uint8Array>>();
  const enc = new TextEncoder();
  const send = (event: string, data: unknown) => {
    const chunk = enc.encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
    for (const c of streams) {
      try {
        c.enqueue(chunk);
      } catch {
        streams.delete(c);
      }
    }
  };

  const mock: Mock = {
    base: "",
    mode: "paired",
    catalog: false,
    setStatus(p, status) {
      statuses[p] = status;
      send("state", state());
    },
    push(e) {
      entries.push(e);
      send("entry", e);
    },
    stop: () => server.stop(true),
  };

  const server = Bun.serve({
    port: 0,
    idleTimeout: 0,
    async fetch(req) {
      const url = new URL(req.url);
      const p = url.pathname;
      if (p === "/api/pair" && req.method === "POST")
        return Response.json({ status: "pairing", error: "That code is wrong.", left: 2 });
      if (p === "/api/pair") return Response.json({ status: mock.mode });
      if (p === "/api/messages") return Response.json({ configured: false, messages: [] });
      if (p === "/api/catalog") return mock.catalog ? Response.json(catalog) : new Response("{}", { status: 404 });
      if (p === "/api/routes/inbox")
        return Response.json({
          path: url.searchParams.get("path") ?? "",
          exists: false,
          private: true,
          setup: false,
          proposed: [],
          approved: [],
          pair_proposed: false,
          pair_approved: false,
        });
      if (p === "/api/stream") {
        let self: ReadableStreamDefaultController<Uint8Array>;
        const body = new ReadableStream<Uint8Array>({
          start(c) {
            self = c;
            streams.add(c);
            c.enqueue(
              enc.encode(
                `event: state\ndata: ${JSON.stringify(state())}\n\nevent: entries\ndata: ${JSON.stringify(entries)}\n\n`,
              ),
            );
          },
          cancel() {
            streams.delete(self);
          },
        });
        return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
      }
      if (p.startsWith("/api/")) return new Response("{}", { status: 404 });
      const file = Bun.file(join(dist, p.endsWith("/") ? p + "index.html" : p));
      return (await file.exists()) ? new Response(file) : new Response("no", { status: 404 });
    },
  });
  mock.base = `http://localhost:${server.port}`;
  return mock;
}
