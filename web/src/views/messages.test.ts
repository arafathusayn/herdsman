import { afterEach, beforeEach, expect, test } from "bun:test";

import { describeFlag, kindLabel, stateLabel, stateTip } from "../channel.ts";
import { each, h, icon, icons, mount, unkeyed } from "../dom.ts";
import type { FakeElement, FakeNode } from "../herdsman-blocks.testdom.ts";
import { dispatch, FakeElement as FormElement, installDom } from "../testdom.ts";
import type { Channel, ChannelMessage } from "../types.ts";

// The store reads the URL and listens for history changes when it loads,
// writes the URL on go() and batches renders in animation frames.
const g = globalThis as unknown as Record<string, unknown>;
g["location"] ??= { search: "", pathname: "/" };
g["window"] ??= { addEventListener: () => {} };
g["history"] ??= { pushState: () => {}, replaceState: () => {} };
g["requestAnimationFrame"] ??= () => 0;
const { MessagesView } = await import("./messages.ts");
const { store } = await import("../store.ts");

const realFetch = globalThis.fetch;
let scrolled: string[] = [];
beforeEach(() => {
  installDom();
  scrolled = [];
  (g["document"] as Record<string, unknown>)["getElementById"] = (id: string) => ({
    scrollIntoView: () => scrolled.push(id),
  });
});
afterEach(() => {
  globalThis.fetch = realFetch;
});

const fake = (n: unknown): FakeElement => n as FakeElement;
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const flush = () => new Promise((r) => setTimeout(r, 0));

// The attributes a browser reflects from the properties h() assigns
// ("k in el"), which the test DOM's elements keep as plain fields.
const reflected = (e: FakeElement): { name: string; value: string }[] => {
  if (!(e instanceof FormElement)) return [];
  const out: { name: string; value: string }[] = [];
  if (e.id && e.getAttribute("id") === null) out.push({ name: "id", value: e.id });
  if (e.type && e.getAttribute("type") === null) out.push({ name: "type", value: e.type });
  if (e.disabled && e.getAttribute("disabled") === null) out.push({ name: "disabled", value: "" });
  return out;
};

// Elements and text with attributes sorted and each()'s data-key left out;
// empty text (a slot's anchor) is not content, and adjacent text joins.
const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = [...e.attributes, ...reflected(e)]
    .filter((a) => a.name !== "data-key")
    .map((a) => `${a.name}="${a.value}"`)
    .sort()
    .join(" ");
  return `<${e.tagName.toLowerCase()} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName.toLowerCase()}>`;
};

const panes = { adjutant: "p1", orchestrator: "" };
const msg = (id: string, over: Partial<ChannelMessage> = {}): ChannelMessage => ({
  id,
  from: "adj",
  to: "orch",
  kind: "brief",
  reply_to: "",
  time: 1700000000,
  state: "ready",
  acknowledged: false,
  ...over,
});
const channel = (messages: ChannelMessage[]): Channel => ({
  configured: true,
  approved: false,
  pending: true,
  peers: [
    { name: "adj", role: "adjutant", pane: "" },
    { name: "orch", role: "orchestrator", pane: "" },
  ],
  health: [
    { name: "courier", age: 12, ok: true },
    { name: "watcher", age: -1, ok: false },
  ],
  health_after: 120,
  flags: ["automation-held", "odd-flag"],
  holds: [
    {
      name: "deploy",
      owners: [
        { by: "ann", reason: "freeze" },
        { by: "bob", reason: "" },
      ],
    },
    { name: "x" },
  ],
  boxes: [
    { name: "orch", orphan: false, counts: { quarantine: 2 }, unacked: 3, claim: "held", suspicious: 0 },
    { name: "adj", orphan: true, counts: {}, unacked: 0, claim: "none", suspicious: 0 },
    { name: "quiet", orphan: false, counts: {}, unacked: 0, claim: "none", suspicious: 0 },
  ],
  messages,
  alerts: [
    { title: "Stuck", body: "The courier <b>stopped</b>" },
    { title: "Late", body: "A reply is overdue" },
  ],
});
const messages = [
  msg("m1"),
  msg("m2", { from: "orch", to: "adj", kind: "result", reply_to: "m1", state: "cur", acknowledged: true }),
  msg("m3", { state: "quarantine", reply_to: "gone" }),
  msg("m4", { state: "new", kind: "question" }),
  msg("m5", { state: "cur" }),
  msg("m6"),
];
// What each open message's body holds: JSON with fields, plain text, an error, still loading.
const bodies: [string, { text?: string; error?: string }][] = [
  ["m1", { text: JSON.stringify({ version: 1, text: "hello <i>", n: 2, why: "because" }) }],
  ["m2", { text: "plain & simple" }],
  ["m5", { error: "cannot read m5 (500)" }],
  ["m6", {}],
];

type Internals = { expanded: Set<string>; bodies: Map<string, { text?: string; error?: string }>; lastKey: string };
// Opens messages and loads bodies as toggle() and the fetch would.
const open = (v: object, ids: string[], loaded: typeof bodies): void => {
  const i = v as Internals;
  for (const id of ids) i.expanded.add(id);
  for (const [id, b] of loaded) i.bodies.set(id, b);
  i.lastKey = "";
};

test("every state renders what the h() version did", () => {
  const cases: [Channel | null, string[]][] = [
    [null, []],
    [{ ...channel([]), configured: false }, []],
    [{ ...channel([]), error: "no inbox <here>" }, []],
    [{ ...channel([]), alerts: [], approved: true }, []],
    [channel(messages), []],
    [channel(messages), ["m1", "m2", "m3", "m5", "m6"]],
  ];
  for (const [ch, ids] of cases) {
    const now = new MessagesView();
    const then = new OldMessagesView();
    open(now, ids, bodies);
    open(then, ids, bodies);
    now.render(ch, panes);
    then.render(ch, panes);
    expect(dump(fake(now.el))).toBe(dump(fake(then.el)));
  }
});

test("one view moves between states and still renders what the h() version did", () => {
  const now = new MessagesView();
  const el = now.el;
  const states: (Channel | null)[] = [
    null,
    channel(messages),
    { ...channel([]), configured: false },
    channel(messages.slice(2)),
    null,
  ];
  for (const ch of states) {
    const then = new OldMessagesView();
    now.render(ch, panes);
    then.render(ch, panes);
    expect(now.el).toBe(el);
    expect(dump(fake(now.el))).toBe(dump(fake(then.el)));
  }
});

test("the view keeps its messages' nodes across renders", () => {
  const v = new MessagesView();
  v.render(channel(messages.slice(0, 2)), panes);
  const thread = elements(fake(v.el)).find((e) => e.className === "msg-thread")!;
  const [m2, m1] = elements(thread);
  v.render(channel(messages.slice(0, 3)), panes);
  const after = elements(thread);
  expect(after.map((e) => e.getAttribute("id"))).toEqual(["msg-m3", "msg-m2", "msg-m1"]);
  expect(after[1]).toBe(m2!);
  expect(after[2]).toBe(m1!);
});

test("a message opens, loads its body once, and its reply link scrolls to the message it answers", async () => {
  let fetched = 0;
  globalThis.fetch = (async () => {
    fetched++;
    return new Response(JSON.stringify({ body: JSON.stringify({ text: "hi", k: "v" }) }));
  }) as unknown as typeof fetch;
  const v = new MessagesView();
  const ch = channel(messages.slice(0, 2));
  v.render(ch, panes);
  const thread = elements(fake(v.el)).find((e) => e.className === "msg-thread")!;
  const m1 = elements(thread)[1]!;
  const toggle = elements(m1).find((e) => e.className === "btn ghost msg-toggle")!;
  dispatch(toggle, "click");
  v.render(ch, panes);
  expect(toggle.getAttribute("aria-expanded")).toBe("true");
  expect(dump(elements(m1).at(-1)!)).toBe('<div class="msg-body"><p class="muted">Loading…</p></div>');
  await flush();
  v.render(ch, panes);
  expect(fetched).toBe(1);
  const body = elements(m1).at(-1)!;
  expect(dump(body)).toBe(
    '<div class="msg-body"><div ><pre class="msg-text">hi</pre><dl class="msg-fields"><dt >k</dt><dd >v</dd></dl></div></div>',
  );
  // A render that changes nothing keeps the fields' nodes.
  const dl = elements(elements(body)[0]!)[1]!;
  (v as unknown as Internals).lastKey = "";
  v.render(ch, panes);
  expect(elements(elements(elements(m1).at(-1)!)[0]!)[1]).toBe(dl);

  const m2 = elements(thread)[0]!;
  const reply = elements(elements(m2)[1]!)[1]!;
  const e = dispatch(reply, "click");
  expect(e.defaultPrevented).toBe(true);
  expect(scrolled).toEqual(["msg-m1"]);

  dispatch(toggle, "click");
  v.render(ch, panes);
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  expect(elements(m1).at(-1)).toBe(toggle);
});

test("a peer opens its pane", () => {
  const v = new MessagesView();
  v.render(channel([]), panes);
  const pair = elements(elements(fake(v.el))[0]!)[0]!;
  const [adj, orch] = elements(pair).filter((e) => e.tagName === "BUTTON");
  expect(orch!.getAttribute("disabled")).toBe("");
  dispatch(adj!, "click");
  expect(store.app.view.pane).toBe("p1");
});

// ---------------------------------------------------------------------------
// The h() version the view replaces, for the comparison: views/messages.ts as
// it was, without toggle() (the tests set its state directly).
// ---------------------------------------------------------------------------

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
          unkeyed(
            rest.map(([k, val]): [HTMLElement, HTMLElement] => [
              h("dt", null, k),
              h("dd", null, typeof val === "string" ? val : JSON.stringify(val)),
            ]),
          ),
        )
      : null,
  );
}
class OldMessagesView {
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
        unkeyed(
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
        ),
        unkeyed(
          (ch.flags ?? []).map((f) => {
            const [text, tip] = describeFlag(f);
            return h("span", { class: "chip bad", "data-tip": tip }, text);
          }),
        ),
        each(
          ch.holds ?? [],
          (x) => x.name,
          (x) =>
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
        each(
          boxes,
          (b) => b.name,
          (b) =>
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
      unkeyed(
        ch.alerts.map((a) => h("div", { class: "msg-alert" }, h("strong", null, a.title), h("span", null, a.body))),
      ),
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
      each(
        [...ch.messages].reverse(),
        (m) => m.id,
        (m) => {
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
        },
      ),
    );
  }
}
