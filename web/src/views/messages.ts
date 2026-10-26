// Messages: the herdsman message channel between the adjutant and the
// orchestrator, read the way `message status` reads it. The view shows the
// pair, the courier's health, alerts, and the conversation; a body loads
// only when its message is opened, and never for unverified entries.
import { messageBody } from "../api.ts";
import { describeFlag, kindLabel, stateLabel, stateTip } from "../channel.ts";
import { icon, type IconNode, icons } from "../dom.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  render,
  text,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
import { store } from "../store.ts";
import type { Channel, ChannelMessage } from "../types.ts";

function when(t: number): string {
  const d = new Date(t * 1000);
  const today = new Date().toDateString() === d.toDateString();
  return today
    ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : d.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function seconds(n: number): string {
  if (n < 0) return "no heartbeat";
  if (n < 90) return `${n}s ago`;
  if (n < 5400) return `${Math.round(n / 60)}m ago`;
  return `${Math.round(n / 3600)}h ago`;
}

const MsgText = html`<pre class="msg-text">${text()}</pre>`;
// A field is a term and its value, siblings in the list: two items.
const Term = html`<dt>${text()}</dt>`;
const Value = html`<dd>${text()}</dd>`;
type FieldPart = ViewNodeOf<typeof Term> | ViewNodeOf<typeof Value>;
const Fields = html`<dl class="msg-fields">${child<ListNode<FieldPart, string>>()}</dl>`;
const JsonBody = html`<div>
  ${child<ViewNodeOf<typeof MsgText> | null>()}${child<ViewNodeOf<typeof Fields> | null>()}
</div>`;

type BodyNode = ViewNodeOf<typeof MsgText> | ViewNodeOf<typeof JsonBody>;

/** A body as the parties write it: versioned JSON with a text field, or plain text. */
function renderBody(body: string): BodyNode {
  let v: unknown;
  try {
    v = JSON.parse(body);
  } catch {
    return MsgText([body]);
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return MsgText([body]);
  const said: unknown = "text" in v ? v.text : undefined;
  // A JSON object's keys are unique, so each keys its term and its value.
  const parts = Object.entries(v)
    .filter(([k]) => k !== "text" && k !== "version")
    .flatMap(([k, val]: [string, unknown]) => [
      { key: `${k}|dt`, text: k, term: true },
      { key: `${k}|dd`, text: typeof val === "string" ? val : JSON.stringify(val), term: false },
    ]);
  return JsonBody([
    typeof said === "string" ? MsgText([said]) : null,
    parts.length
      ? Fields([
          list(
            parts,
            (p) => p.key,
            (p): FieldPart => (p.term ? Term([p.text]) : Value([p.text])),
          ),
        ])
      : null,
  ]);
}

const Loading = html`<p class="muted">Reading the message channel…</p>`;
const NoChannel = html`<div class="empty">
  ${child<IconNode>()}
  <h2>No message channel</h2>
  <p>${text()}<code>${text()}</code>${text()}<code>HERDSMAN_INBOX</code>${text()}</p>
</div>`;
const ChannelError = html`<div class="empty">
  <h2>Cannot read the message channel</h2>
  <p>${text()}</p>
</div>`;

const Peer = html`<button
  type="button"
  class=${cls()}
  disabled=${attr<boolean>()}
  title=${attr()}
  onclick=${on<"click">()}
>
  <span class="msg-role">${text()}</span><span class="msg-name">${text()}</span>
</button>`;
const Chip = html`<span class=${cls()} data-tip=${attr<string | null>()}>${text()}</span>`;
type ChipList = ListNode<ViewNodeOf<typeof Chip>, string>;
const Head = html`<div class="msg-head">
  <div class="msg-pair">${child<ViewNodeOf<typeof Peer>>()}${child<IconNode>()}${child<ViewNodeOf<typeof Peer>>()}</div>
  <div class="msg-chips">
    ${child<ViewNodeOf<typeof Chip>>()}${child<ChipList>()}${child<ChipList>()}${child<ChipList>()}${child<ChipList>()}
  </div>
</div>`;

const Alert = html`<div class="msg-alert"><strong>${text()}</strong><span>${text()}</span></div>`;
const Alerts = html`<div class="msg-alerts" role="alert">${child<ListNode<ViewNodeOf<typeof Alert>, string>>()}</div>`;

const Reply = html`<a class="msg-reply" href=${attr()} onclick=${on<"click">()}>${text()}</a>`;
const Toggle = html`<button
  type="button"
  class="btn ghost msg-toggle"
  aria-expanded=${attr<"true" | "false">()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}${text()}
</button>`;
const Note = html`<p class="msg-note">${text()}</p>`;
const BodyError = html`<p class="error">${text()}</p>`;
const BodyLoading = html`<p class="muted">Loading…</p>`;
const Body = html`<div class="msg-body">
  ${child<BodyNode | ViewNodeOf<typeof BodyError> | ViewNodeOf<typeof BodyLoading>>()}
</div>`;
const Message = html`<li class=${cls()} id=${attr()}>
  <div class="msg-meta">
    <span class=${cls()}>${text()}</span><span class="msg-route">${text()}</span
    ><span class=${cls()} data-tip=${attr<string | null>()}>${text()}</span><time title=${attr()}>${text()}</time>
  </div>
  <div class="msg-sub"><code>${text()}</code>${child<ViewNodeOf<typeof Reply> | null>()}</div>
  ${child<ViewNodeOf<typeof Toggle> | ViewNodeOf<typeof Note>>()}${child<ViewNodeOf<typeof Body> | null>()}
</li>`;
const Thread = html`<ol class="msg-thread">
  ${child<ListNode<ViewNodeOf<typeof Message>, string>>()}
</ol>`;
const NoMessages = html`<p class="muted msg-none">No messages yet. The pair's first message is the handshake.</p>`;

const Messages = html`<div class="messages">
  ${child<
    | ViewNodeOf<typeof Loading>
    | ViewNodeOf<typeof NoChannel>
    | ViewNodeOf<typeof ChannelError>
    | ViewNodeOf<typeof Head>
    | null
  >()}${child<ViewNodeOf<typeof Alerts> | null>()}${child<
    ViewNodeOf<typeof Thread> | ViewNodeOf<typeof NoMessages> | null
  >()}
</div>`;

type Peers = { adjutant: string; orchestrator: string };
type Loaded = { text?: string; error?: string };

// A checked narrowing, not a cast: the view's root is an HTML element.
const isHTML = (n: Node | null): n is HTMLElement => n !== null && n.nodeType === 1;

export class MessagesView {
  /** The view's root, made once; render() patches what is in it. */
  readonly el: HTMLElement;
  // render() keeps the view's instance on box; the root moves to wherever main.ts puts el.
  private readonly box = document.createElement("div");
  private readonly bodies = new Map<string, Loaded>();
  private readonly expanded = new Set<string>();
  private lastKey = "";

  constructor() {
    render(this.box, Messages([null, null, null]));
    const root = this.box.firstChild;
    if (!isHTML(root)) throw new Error("MessagesView: the view rendered no element");
    this.el = root;
  }

  render(ch: Channel | null, panes: Peers): void {
    const key = JSON.stringify([ch, panes, [...this.expanded], [...this.bodies.keys()]]);
    if (key === this.lastKey) return;
    this.lastKey = key;
    render(
      this.box,
      !ch
        ? Messages([Loading([]), null, null])
        : !ch.configured
          ? Messages([
              NoChannel([
                icon(icons.message),
                "With an adjutant, the user talks to it and it relays to the orchestrator through herdsman messages. Start ",
                "herdsman web --inbox <private inbox folder>",
                " (or set ",
                ") to show them here.",
              ]),
              null,
              null,
            ])
          : ch.error
            ? Messages([ChannelError([ch.error]), null, null])
            : Messages([this.header(ch, panes), this.alerts(ch), this.thread(ch)]),
    );
  }

  private header(ch: Channel, panes: Peers): ViewNodeOf<typeof Head> {
    const peer = (role: "adjutant" | "orchestrator") => {
      const p = ch.peers.find((x) => x.role === role);
      const pane = p?.pane || panes[role];
      return Peer([
        `msg-peer ${role}`,
        !pane,
        pane ? `Open ${role} pane ${pane}` : `No ${role} enrolled`,
        () => pane && store.go({ pane }),
        role === "adjutant" ? "Adjutant" : "Orchestrator",
        p?.name ?? "—",
      ]);
    };
    const boxes = ch.boxes.filter((b) => b.unacked || b.claim !== "none" || b.counts["quarantine"] || b.orphan);
    return Head([
      peer("adjutant"),
      icon(icons.arrow),
      peer("orchestrator"),
      Chip([
        ch.approved ? "chip ok" : "chip warn",
        ch.approved
          ? "The human approved this pair; delivery runs"
          : "A registry change waits for the human's approval; delivery is held",
        ch.approved ? "Pair approved" : "Approval pending",
      ]),
      list(
        ch.health,
        (s) => s.name,
        (s) =>
          Chip([
            s.ok ? "chip ok" : "chip bad",
            `Heartbeat ${seconds(s.age)}; down after ${ch.health_after}s`,
            `${s.name === "courier" ? "Courier" : "Watcher"} ${s.ok ? "up" : "down"}`,
          ]),
      ),
      list(
        [...new Set(ch.flags ?? [])], // a flag is keyed by its name, so repeats go
        (f) => f,
        (f) => {
          const [label, tip] = describeFlag(f);
          return Chip(["chip bad", tip, label]);
        },
      ),
      list(
        ch.holds ?? [],
        (x) => x.name,
        (x) =>
          Chip([
            "chip warn",
            x.owners?.length
              ? `Held by ${x.owners.map((o) => (o.reason ? `${o.by}: ${o.reason}` : o.by)).join("; ")}`
              : "A hold or resume intent in the channel's state folder",
            x.name,
          ]),
      ),
      list(
        boxes,
        (b) => b.name,
        (b) =>
          Chip([
            b.counts["quarantine"] || b.orphan ? "chip warn" : "chip",
            null,
            `${b.name}: ${b.unacked} unread` +
              (b.claim !== "none" ? ` · claim ${b.claim}` : "") +
              (b.counts["quarantine"] ? ` · ${b.counts["quarantine"]} quarantined` : "") +
              (b.orphan ? " · not enrolled" : ""),
          ]),
      ),
    ]);
  }

  private alerts(ch: Channel): ViewNodeOf<typeof Alerts> | null {
    if (!ch.alerts.length) return null;
    // An alert is keyed by its title and body, so an exact repeat shows once.
    const byKey = new Map(ch.alerts.map((a) => [`${a.title}|${a.body}`, a]));
    return Alerts([
      list(
        [...byKey],
        ([k]) => k,
        ([, a]) => Alert([a.title, a.body]),
      ),
    ]);
  }

  private thread(ch: Channel): ViewNodeOf<typeof Thread> | ViewNodeOf<typeof NoMessages> {
    if (!ch.messages.length) return NoMessages([]);
    const roles = new Map(ch.peers.map((p) => [p.name, p.role]));
    const ids = new Set(ch.messages.map((m) => m.id));
    return Thread([
      list(
        [...ch.messages].reverse(),
        (m) => m.id,
        (m) => {
          const side = roles.get(m.from) === "orchestrator" ? "right" : "left";
          const label = stateLabel(m);
          const open = this.expanded.has(m.id);
          const readable = m.state === "ready" || m.state === "cur";
          return Message([
            `msg ${side} k-${m.kind}${m.state === "quarantine" ? " quarantined" : ""}`,
            `msg-${m.id}`,
            `msg-kind k-${m.kind}`,
            kindLabel[m.kind] ?? m.kind,
            `${m.from} → ${m.to}`,
            `msg-state s-${label.replace(" ", "-").toLowerCase()}`,
            stateTip[label] ?? null,
            label,
            new Date(m.time * 1000).toLocaleString(),
            when(m.time),
            m.id,
            m.reply_to
              ? Reply([
                  `#msg-${m.reply_to}`,
                  (e) => {
                    e.preventDefault();
                    if (ids.has(m.reply_to))
                      document
                        .getElementById(`msg-${m.reply_to}`)
                        ?.scrollIntoView({ block: "center", behavior: "smooth" });
                  },
                  `↩ ${m.reply_to}`,
                ])
              : null,
            readable
              ? Toggle([
                  open ? "true" : "false",
                  () => this.toggle(m),
                  icon(icons.chevronDown),
                  open ? "Hide" : "Show message",
                ])
              : Note([
                  m.state === "quarantine"
                    ? "Failed the courier's checks. The body is not shown; the sender must send a fresh message."
                    : "Waiting for the courier to verify it.",
                ]),
            open && readable ? Body([this.body(m.id)]) : null,
          ]);
        },
      ),
    ]);
  }

  private body(id: string): BodyNode | ViewNodeOf<typeof BodyError> | ViewNodeOf<typeof BodyLoading> {
    const b = this.bodies.get(id);
    if (b?.text !== undefined) {
      return renderBody(b.text);
    }
    return b?.error ? BodyError([b.error]) : BodyLoading([]);
  }
  private toggle(m: ChannelMessage): void {
    if (this.expanded.has(m.id)) this.expanded.delete(m.id);
    else {
      this.expanded.add(m.id);
      // A body that failed to load is fetched again when it is shown again.
      if (!this.bodies.has(m.id) || this.bodies.get(m.id)?.error) {
        this.bodies.set(m.id, {});
        messageBody(m.to, m.id)
          .then((text) => this.bodies.set(m.id, { text }))
          .catch((e: unknown) => this.bodies.set(m.id, { error: e instanceof Error ? e.message : String(e) }))
          .finally(() => {
            // The body changed but not the set of loaded ids, so force the redraw.
            this.lastKey = "";
            store.emit();
          });
      }
    }
    this.lastKey = "";
    store.emit();
  }
}
