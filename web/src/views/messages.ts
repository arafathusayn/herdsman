// Messages: the herdsman message channel between the adjutant and the
// orchestrator, read the way `message status` reads it. The view shows the
// pair, the courier's health, alerts, and the conversation; a body loads
// only when its message is opened, and never for unverified entries.
import { messageBody } from "../api.ts";
import { describeFlag, kindLabel, stateLabel, stateTip } from "../channel.ts";
import { h, icon, icons, mount } from "../dom.ts";
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

/** A body as the parties write it: versioned JSON with a text field, or plain text. */
function renderBody(body: string): HTMLElement {
  let v: unknown;
  try {
    v = JSON.parse(body);
  } catch {
    return h("pre", { class: "msg-text" }, body);
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return h("pre", { class: "msg-text" }, body);
  const o = v as Record<string, unknown>;
  const rest = Object.entries(o).filter(([k]) => k !== "text" && k !== "version");
  return h(
    "div",
    null,
    typeof o["text"] === "string" ? h("pre", { class: "msg-text" }, o["text"]) : null,
    rest.length
      ? h(
          "dl",
          { class: "msg-fields" },
          rest.map(([k, val]) => [
            h("dt", null, k),
            h("dd", null, typeof val === "string" ? val : JSON.stringify(val)),
          ]),
        )
      : null,
  );
}

export class MessagesView {
  readonly el = h("div", { class: "messages" });
  private readonly bodies = new Map<string, { text?: string; error?: string }>();
  private readonly expanded = new Set<string>();
  private lastKey = "";

  render(ch: Channel | null, panes: { adjutant: string; orchestrator: string }): void {
    const key = JSON.stringify([ch, panes, [...this.expanded], [...this.bodies.keys()]]);
    if (key === this.lastKey) return;
    this.lastKey = key;
    if (!ch) {
      mount(this.el, h("p", { class: "muted" }, "Reading the message channel…"));
      return;
    }
    if (!ch.configured) {
      mount(
        this.el,
        h(
          "div",
          { class: "empty" },
          icon(icons.message),
          h("h2", null, "No message channel"),
          h(
            "p",
            null,
            "With an adjutant, the user talks to it and it relays to the orchestrator through herdsman messages. Start ",
            h("code", null, "herdsman web --inbox <private inbox folder>"),
            " (or set ",
            h("code", null, "HERDSMAN_INBOX"),
            ") to show them here.",
          ),
        ),
      );
      return;
    }
    if (ch.error) {
      mount(
        this.el,
        h("div", { class: "empty" }, h("h2", null, "Cannot read the message channel"), h("p", null, ch.error)),
      );
      return;
    }
    mount(this.el, this.header(ch, panes), this.alerts(ch), this.thread(ch));
  }

  private header(ch: Channel, panes: { adjutant: string; orchestrator: string }): HTMLElement {
    const peer = (role: "adjutant" | "orchestrator") => {
      const p = ch.peers.find((x) => x.role === role);
      const pane = p?.pane || panes[role];
      return h(
        "button",
        {
          type: "button",
          class: `msg-peer ${role}`,
          disabled: !pane,
          title: pane ? `Open ${role} pane ${pane}` : `No ${role} enrolled`,
          onclick: () => pane && store.go({ pane }),
        },
        h("span", { class: "msg-role" }, role === "adjutant" ? "Adjutant" : "Orchestrator"),
        h("span", { class: "msg-name" }, p?.name ?? "—"),
      );
    };
    const boxes = ch.boxes.filter((b) => b.unacked || b.claim !== "none" || b.counts["quarantine"] || b.orphan);
    return h(
      "div",
      { class: "msg-head" },
      h("div", { class: "msg-pair" }, peer("adjutant"), icon(icons.arrow), peer("orchestrator")),
      h(
        "div",
        { class: "msg-chips" },
        h(
          "span",
          {
            class: ch.approved ? "chip ok" : "chip warn",
            "data-tip": ch.approved
              ? "The human approved this pair; delivery runs"
              : "A registry change waits for the human's approval; delivery is held",
          },
          ch.approved ? "Pair approved" : "Approval pending",
        ),
        ch.health.map((s) =>
          h(
            "span",
            {
              class: s.ok ? "chip ok" : "chip bad",
              "data-tip": `Heartbeat ${seconds(s.age)}; down after ${ch.health_after}s`,
            },
            `${s.name === "courier" ? "Courier" : "Watcher"} ${s.ok ? "up" : "down"}`,
          ),
        ),
        (ch.flags ?? []).map((f) => {
          const [text, tip] = describeFlag(f);
          return h("span", { class: "chip bad", "data-tip": tip }, text);
        }),
        (ch.holds ?? []).map((x) =>
          h(
            "span",
            {
              class: "chip warn",
              "data-tip": x.owners?.length
                ? `Held by ${x.owners.map((o) => (o.reason ? `${o.by}: ${o.reason}` : o.by)).join("; ")}`
                : "A hold or resume intent in the channel's state folder",
            },
            x.name,
          ),
        ),
        boxes.map((b) =>
          h(
            "span",
            { class: b.counts["quarantine"] || b.orphan ? "chip warn" : "chip" },
            `${b.name}: ${b.unacked} unread`,
            b.claim !== "none" ? ` · claim ${b.claim}` : "",
            b.counts["quarantine"] ? ` · ${b.counts["quarantine"]} quarantined` : "",
            b.orphan ? " · not enrolled" : "",
          ),
        ),
      ),
    );
  }

  private alerts(ch: Channel): HTMLElement | null {
    if (!ch.alerts.length) return null;
    return h(
      "div",
      { class: "msg-alerts", role: "alert" },
      ch.alerts.map((a) => h("div", { class: "msg-alert" }, h("strong", null, a.title), h("span", null, a.body))),
    );
  }

  private thread(ch: Channel): HTMLElement {
    if (!ch.messages.length)
      return h("p", { class: "muted msg-none" }, "No messages yet. The pair's first message is the handshake.");
    const roles = new Map(ch.peers.map((p) => [p.name, p.role]));
    const ids = new Set(ch.messages.map((m) => m.id));
    return h(
      "ol",
      { class: "msg-thread" },
      [...ch.messages].reverse().map((m) => {
        const side = roles.get(m.from) === "orchestrator" ? "right" : "left";
        const label = stateLabel(m);
        const open = this.expanded.has(m.id);
        const body = this.bodies.get(m.id);
        const readable = m.state === "ready" || m.state === "cur";
        return h(
          "li",
          { class: `msg ${side} k-${m.kind}${m.state === "quarantine" ? " quarantined" : ""}`, id: `msg-${m.id}` },
          h(
            "div",
            { class: "msg-meta" },
            h("span", { class: `msg-kind k-${m.kind}` }, kindLabel[m.kind] ?? m.kind),
            h("span", { class: "msg-route" }, `${m.from} → ${m.to}`),
            h(
              "span",
              { class: `msg-state s-${label.replace(" ", "-").toLowerCase()}`, "data-tip": stateTip[label] },
              label,
            ),
            h("time", { title: new Date(m.time * 1000).toLocaleString() }, when(m.time)),
          ),
          h(
            "div",
            { class: "msg-sub" },
            h("code", null, m.id),
            m.reply_to
              ? h(
                  "a",
                  {
                    class: "msg-reply",
                    href: `#msg-${m.reply_to}`,
                    onclick: (e: Event) => {
                      e.preventDefault();
                      if (ids.has(m.reply_to))
                        document
                          .getElementById(`msg-${m.reply_to}`)
                          ?.scrollIntoView({ block: "center", behavior: "smooth" });
                    },
                  },
                  `↩ ${m.reply_to}`,
                )
              : null,
          ),
          readable
            ? h(
                "button",
                {
                  type: "button",
                  class: "btn ghost msg-toggle",
                  "aria-expanded": String(open),
                  onclick: () => this.toggle(m),
                },
                icon(icons.chevronDown),
                open ? "Hide" : "Show message",
              )
            : h(
                "p",
                { class: "msg-note" },
                m.state === "quarantine"
                  ? "Failed the courier's checks. The body is not shown; the sender must send a fresh message."
                  : "Waiting for the courier to verify it.",
              ),
          open && readable
            ? h(
                "div",
                { class: "msg-body" },
                body?.text !== undefined
                  ? renderBody(body.text)
                  : body?.error
                    ? h("p", { class: "error" }, body.error)
                    : h("p", { class: "muted" }, "Loading…"),
              )
            : null,
        );
      }),
    );
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
