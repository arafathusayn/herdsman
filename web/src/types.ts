// Mirrors the JSON of internal/herdr and internal/web.

export type Status = "idle" | "working" | "blocked" | "done" | "unknown";

export interface Workspace {
  workspace_id: string;
  number: number;
  label: string;
  focused: boolean;
  active_tab_id: string;
  agent_status: Status;
  /** The git checkout herdr found for the workspace. */
  worktree?: WorkspaceWorktree;
}

export interface WorkspaceWorktree {
  repo_key: string;
  repo_name: string;
  repo_root: string;
  checkout_path: string;
  is_linked_worktree: boolean;
}

export interface Tab {
  tab_id: string;
  workspace_id: string;
  number: number;
  label: string;
  focused: boolean;
  pane_count: number;
  agent_status: Status;
}

export interface Pane {
  pane_id: string;
  workspace_id: string;
  tab_id: string;
  focused: boolean;
  label: string | null;
  cwd: string | null;
  foreground_cwd: string | null;
  terminal_title_stripped: string | null;
  agent: string | null;
  agent_status: Status;
  revision: number;
}

export interface Agent {
  pane_id: string;
  tab_id: string;
  workspace_id: string;
  name: string | null;
  agent: string | null;
  display_agent: string | null;
  agent_status: Status;
  focused: boolean;
  cwd: string | null;
  terminal_title_stripped: string | null;
  interactive_ready: boolean;
  launch_pending: boolean;
  state_change_seq: number;
}

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface Layout {
  workspace_id: string;
  tab_id: string;
  zoomed: boolean;
  area: Rect;
  panes: { pane_id: string; focused: boolean; rect: Rect }[] | null;
}

export interface Snapshot {
  version: string;
  protocol: number;
  focused_workspace_id: string | null;
  focused_tab_id: string | null;
  focused_pane_id: string | null;
  workspaces: Workspace[] | null;
  tabs: Tab[] | null;
  panes: Pane[] | null;
  layouts: Layout[] | null;
  agents: Agent[] | null;
}

export interface Task {
  task: string;
  branch: string;
  worktree: string;
  agent: string;
  pane: string;
  contract: string;
}

export interface CheckItem {
  done: boolean;
  /** A `- [-]` line: a requirement that stopped applying, with its reason in the text. */
  skipped?: boolean;
  id: string;
  text: string;
}

export interface LogLine {
  time: string;
  text: string;
}

export interface RouteFile {
  path: string;
  size: number;
  modified: string;
}

export interface Route {
  /** Names the route in links and requests: the folder name, or with a short hash when two projects share it. */
  id: string;
  dir: string;
  name: string;
  title: string;
  orchestrator_pane: string;
  /** The optional adjutant: the user's front, which relays through herdsman messages. */
  adjutant_pane?: string;
  tasks: Task[] | null;
  checklist: CheckItem[] | null;
  run_state: LogLine[] | null;
  /** Run-state lines before run_state, which holds the newest only; paged by routeRunState. */
  older_run_state?: number;
  files: RouteFile[] | null;
  modified: string;
  live: boolean;
}

export interface State {
  herdr: { version: string; protocol: number };
  snapshot: Snapshot;
  routes: Route[] | null;
  error?: string;
}

export interface Entry {
  at: string;
  source: "herdr" | "web";
  kind: string;
  pane_id?: string;
  /** The status an agent moved to, on status events. */
  status?: Status;
  text?: string;
}

export interface PaneRead {
  pane_id: string;
  text: string;
  revision: number;
  truncated: boolean;
}

export type Action =
  | { type: "prompt"; pane: string; text: string }
  | { type: "text"; pane: string; text: string }
  | { type: "keys"; pane: string; keys: string[]; force?: boolean }
  | { type: "start"; pane: string; name: string; kind: string; args: string[] }
  | { type: "rename_agent"; pane: string; name: string }
  | { type: "rename_pane"; pane: string; label: string }
  | { type: "split"; pane: string; direction: "right" | "down"; cwd?: string }
  | { type: "close"; pane: string; force?: boolean }
  | { type: "focus"; pane: string }
  /** Terminal mode typing: keys or text, never both; never logged. */
  | { type: "input"; pane: string; keys?: string[]; text?: string }
  /** Terminal mode wheel: lines > 0 scrolls back; col and row are the 1-based cell under the pointer. */
  | { type: "scroll"; pane: string; lines: number; col: number; row: number };

export interface ActionResult {
  ok: boolean;
  error?: string;
  code?: string;
  summary?: string;
}

export interface DiffFile {
  path: string;
  old_path?: string;
  /** A, M, D, R, C or T from git, or "?" for an untracked file. */
  status: string;
  add: number;
  del: number;
  binary?: boolean;
}

export interface Diff {
  base: string;
  merge_base: string;
  head: string;
  files: DiffFile[] | null;
  patch: string;
  truncated?: boolean;
}

export interface DiffSource {
  id: string;
  /** A route task, a worktree an agent works in, or the project checkout. */
  kind: "task" | "worktree" | "checkout";
  label: string;
  task?: string;
  agent?: string;
  branch?: string;
  dir?: string;
  /** Agent panes working in this folder now. */
  agents?: string[];
  diff?: Diff;
  error?: string;
}

export interface Artifact {
  /** The session folder it was found under. */
  root: string;
  /** Slash-separated, relative to root. */
  path: string;
  size: number;
  modified: string;
}

export interface ArtifactList {
  roots: string[];
  artifacts: Artifact[];
}

export type MessageKind =
  "brief" | "observation" | "question" | "result" | "cancel" | "user_answer" | "receipt" | "side_agent_data";

/** One herdsman message's envelope header; the body comes on request. */
export interface ChannelMessage {
  id: string;
  from: string;
  to: string;
  kind: MessageKind;
  reply_to: string;
  /** Unix seconds. */
  time: number;
  /** new: waits for the courier; ready: verified; cur: claimed by its reader; quarantine: failed checks. */
  state: "new" | "ready" | "cur" | "quarantine";
  /** The reader recorded its intake into the route. */
  acknowledged: boolean;
}

export interface Channel {
  configured: boolean;
  error?: string;
  approved: boolean;
  pending: boolean;
  peers: { name: string; role: "adjutant" | "orchestrator"; pane: string }[];
  health: { name: string; age: number; ok: boolean }[];
  health_after: number;
  /** Health problems beyond the heartbeats, named as `message status` names them. */
  flags: string[];
  /** Hold and resume intents in the channel's state folder; a named hold lists each owner and why. */
  holds: { name: string; owners?: { by: string; reason: string }[] }[];
  boxes: {
    name: string;
    orphan: boolean;
    counts: Record<string, number>;
    unacked: number;
    claim: "held" | "incomplete" | "none";
    suspicious: number;
  }[];
  messages: ChannelMessage[];
  alerts: { title: string; body: string }[];
}

export interface CatalogModel {
  id: string;
  label: string;
  efforts: string[];
  default_effort: string;
}

/** One harness the New route form can start, as GET /api/catalog lists it. */
export interface CatalogKind {
  kind: string;
  label: string;
  models: CatalogModel[];
  default_model: string;
  other_model: boolean;
  free_model: boolean;
  checkpointer: boolean;
  skill_prefix: string;
  skill_installed: boolean;
  offline: boolean;
  /** The harness maker's page of its models. */
  docs_url?: string;
}

export interface Catalog {
  kinds: CatalogKind[];
  today: string;
  home: string;
  checkpointer_ok: boolean;
  checkpointer_why?: string;
  /** The last pick of each role, saved when a route started (A30). */
  previous?: ChoicePick[];
}

/** The gh accounts that can read a folder's GitHub repository (A32). */
export interface GitHubAccess {
  repo: string;
  accounts: { name: string; active: boolean; can_read: boolean }[];
  suggested: string;
  error?: string;
}

/** One role's saved pick: where its agents ran and the harness, model and effort. */
export interface ChoicePick {
  role: string;
  runner: string;
  harness: string;
  model: string;
  effort: string;
  route: string;
  at: string;
}

export type RouteRole = "orchestrator" | "adjutant";

export interface RouteRequest {
  role: RouteRole;
  workspace_id: string;
  folder: string;
  kind: string;
  model: string;
  effort: string;
  name: string;
  request: string;
  checkpointer: { on: boolean; minutes: number; limit: number };
  orch_name?: string;
  orch_kind?: string;
  orch_model?: string;
  orch_effort?: string;
  route_name?: string;
  inbox?: string;
  /** The chosen setup (A32), sent to the orchestrator as text after the request. */
  setup?: RouteSetup;
}

/** The setup the form chose (A32): the server renders it into the orchestrator's prompt. */
export interface RouteSetup {
  recommended: boolean;
  /** The checkpointer as the route starts it: false when it is unavailable. */
  checkpointer: boolean;
  github_account?: string;
  /** The coordinators' picks. */
  roles: { role: string; runner: string; harness: string; model: string; effort: string }[];
  /** One answer per setup item: the recommended sentence, or the user's (empty: the route asks). */
  answers: { item: string; answer: string }[];
}

export interface RouteStep {
  step: string;
  method: string;
  status: "ok" | "failed" | "skipped";
  reason?: string;
  code?: string;
  id?: string;
}

export interface RouteResult {
  ok: boolean;
  code?: string;
  field?: string;
  error?: string;
  steps: RouteStep[];
  tabs: string[];
  orchestrator_pane?: string;
  adjutant_pane?: string;
  checkpointer_pane?: string;
  attach_command?: string;
}

/** What the setup checklist can read from a route's private inbox. */
export interface InboxState {
  path: string;
  exists: boolean;
  private: boolean;
  setup: boolean;
  proposed: string[];
  approved: string[];
  /** The pending registry is exactly this route's adjutant and orchestrator. */
  pair_proposed: boolean;
  /** The approved copy equals that registry, as the channel requires. */
  pair_approved: boolean;
  error?: string;
}
