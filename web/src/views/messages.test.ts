import { afterEach, beforeEach, expect, test } from "bun:test";

import { render } from "../herdsman-blocks-view.ts";
import type { FakeElement, FakeNode } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { dispatch, installDom, markup } from "../testdom.ts";
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
const iconMarkup = (paths: readonly string[], label?: string): string => {
  const box = document.createElement("div");
  render(box, icon(paths, label));
  return dump(box.firstChild as unknown as FakeNode);
};
const flush = () => new Promise((r) => setTimeout(r, 0));

// Elements and text with attributes sorted; empty text (a slot's anchor) is not content, and adjacent text joins.
const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = e.attributes
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

// The markup of each state. A message's time shows month, day and time
// (1700000000 is not today); both it and its title follow the time zone.
const sent = new Date(1700000000 * 1000);
const stamp = sent.toLocaleString();
const shown = sent.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

const loading = '<div class="messages"><p class="muted">Reading the message channel…</p></div>';
const unconfigured = (): string => markup`
  <div class="messages">
    <div class="empty">
      ${iconMarkup(icons.message)}
      <h2 >No message channel</h2>
      <p >With an adjutant, the user talks to it and it relays to the orchestrator through herdsman messages. Start <code >herdsman web --inbox <private inbox folder></code> (or set <code >HERDSMAN_INBOX</code>) to show them here.</p>
    </div>
  </div>
`;
const failed = markup`
  <div class="messages">
    <div class="empty"><h2 >Cannot read the message channel</h2><p >no inbox <here></p></div>
  </div>
`;
const head = (approved: boolean): string => markup`
  <div class="msg-head">
    <div class="msg-pair">
      <button class="msg-peer adjutant" title="Open adjutant pane p1" type="button">
        <span class="msg-role">Adjutant</span>
        <span class="msg-name">adj</span>
      </button>
      ${iconMarkup(icons.arrow)}
      <button class="msg-peer orchestrator" disabled="" title="No orchestrator enrolled" type="button">
        <span class="msg-role">Orchestrator</span>
        <span class="msg-name">orch</span>
      </button>
    </div>
    <div class="msg-chips">
      ${
        approved
          ? '<span class="chip ok" data-tip="The human approved this pair; delivery runs">Pair approved</span>'
          : `<span class="chip warn" data-tip="A registry change waits for the human's approval; delivery is held">Approval pending</span>`
      }
      <span class="chip ok" data-tip="Heartbeat 12s ago; down after 120s">Courier up</span>
      <span class="chip bad" data-tip="Heartbeat no heartbeat; down after 120s">Watcher down</span>
      <span class="chip bad" data-tip="Automation is held until the human releases it">Automation held</span>
      <span class="chip bad" data-tip="Reported by the message channel">odd-flag</span>
      <span class="chip warn" data-tip="Held by ann: freeze; bob">deploy</span>
      <span class="chip warn" data-tip="A hold or resume intent in the channel's state folder">x</span>
      <span class="chip warn">orch: 3 unread · claim held · 2 quarantined</span>
      <span class="chip warn">adj: 0 unread · not enrolled</span>
    </div>
  </div>
`;
const alerts = markup`
  <div class="msg-alerts" role="alert">
    <div class="msg-alert"><strong >Stuck</strong><span >The courier <b>stopped</b></span></div>
    <div class="msg-alert"><strong >Late</strong><span >A reply is overdue</span></div>
  </div>
`;
// A readable message's toggle, and its body when open.
const toggle = (open: boolean): string => markup`
  <button aria-expanded="${String(open)}" class="btn ghost msg-toggle" type="button">
    ${iconMarkup(icons.chevronDown)}${open ? "Hide" : "Show message"}
  </button>
`;
// Each message, newest first, with what follows its meta lines: a toggle and
// body, or a note.
const thread: Record<string, (rest: string) => string> = {
  m6: (rest) => markup`
    <li class="msg left k-brief" id="msg-m6">
      <div class="msg-meta">
        <span class="msg-kind k-brief">Brief</span>
        <span class="msg-route">adj → orch</span>
        <span class="msg-state s-delivered" data-tip="Verified, not yet read">Delivered</span>
        <time title="${stamp}">${shown}</time>
      </div>
      <div class="msg-sub"><code >m6</code></div>
      ${rest}
    </li>
  `,
  m5: (rest) => markup`
    <li class="msg left k-brief" id="msg-m5">
      <div class="msg-meta">
        <span class="msg-kind k-brief">Brief</span>
        <span class="msg-route">adj → orch</span>
        <span class="msg-state s-read" data-tip="Claimed by its reader; intake not recorded yet">Read</span>
        <time title="${stamp}">${shown}</time>
      </div>
      <div class="msg-sub"><code >m5</code></div>
      ${rest}
    </li>
  `,
  m4: (rest) => markup`
    <li class="msg left k-question" id="msg-m4">
      <div class="msg-meta">
        <span class="msg-kind k-question">Question</span>
        <span class="msg-route">adj → orch</span>
        <span class="msg-state s-queued" data-tip="Waiting for the courier to verify it">Queued</span>
        <time title="${stamp}">${shown}</time>
      </div>
      <div class="msg-sub"><code >m4</code></div>
      ${rest}
    </li>
  `,
  m3: (rest) => markup`
    <li class="msg left k-brief quarantined" id="msg-m3">
      <div class="msg-meta">
        <span class="msg-kind k-brief">Brief</span>
        <span class="msg-route">adj → orch</span>
        <span class="msg-state s-quarantined" data-tip="Failed the courier's checks; never trusted">Quarantined</span>
        <time title="${stamp}">${shown}</time>
      </div>
      <div class="msg-sub"><code >m3</code><a class="msg-reply" href="#msg-gone">↩ gone</a></div>
      ${rest}
    </li>
  `,
  m2: (rest) => markup`
    <li class="msg right k-result" id="msg-m2">
      <div class="msg-meta">
        <span class="msg-kind k-result">Result</span>
        <span class="msg-route">orch → adj</span>
        <span class="msg-state s-taken-in" data-tip="The reader recorded it in the route">Taken in</span>
        <time title="${stamp}">${shown}</time>
      </div>
      <div class="msg-sub"><code >m2</code><a class="msg-reply" href="#msg-m1">↩ m1</a></div>
      ${rest}
    </li>
  `,
  m1: (rest) => markup`
    <li class="msg left k-brief" id="msg-m1">
      <div class="msg-meta">
        <span class="msg-kind k-brief">Brief</span>
        <span class="msg-route">adj → orch</span>
        <span class="msg-state s-delivered" data-tip="Verified, not yet read">Delivered</span>
        <time title="${stamp}">${shown}</time>
      </div>
      <div class="msg-sub"><code >m1</code></div>
      ${rest}
    </li>
  `,
};
const queued = '<p class="msg-note">Waiting for the courier to verify it.</p>';
const quarantined =
  '<p class="msg-note">Failed the courier\'s checks. The body is not shown; the sender must send a fresh message.</p>';
// The channel with every message closed, its oldest skip messages left out.
const closed = (skip: number): string => markup`
  <div class="messages">
    ${head(false)}
    ${alerts}
    <ol class="msg-thread">
      ${[
        thread["m6"]!(toggle(false)),
        thread["m5"]!(toggle(false)),
        thread["m4"]!(queued),
        thread["m3"]!(quarantined),
        thread["m2"]!(toggle(false)),
        thread["m1"]!(toggle(false)),
      ]
        .slice(0, 6 - skip)
        .join("")}
    </ol>
  </div>
`;
// The channel with m1, m2, m3, m5 and m6 open: a body each, as loaded (m3 is
// quarantined and stays shut).
const opened = (): string => markup`
  <div class="messages">
    ${head(false)}
    ${alerts}
    <ol class="msg-thread">
      ${thread["m6"]!(toggle(true) + '<div class="msg-body"><p class="muted">Loading…</p></div>')}
      ${thread["m5"]!(toggle(true) + '<div class="msg-body"><p class="error">cannot read m5 (500)</p></div>')}
      ${thread["m4"]!(queued)}
      ${thread["m3"]!(quarantined)}
      ${thread["m2"]!(toggle(true) + '<div class="msg-body"><pre class="msg-text">plain & simple</pre></div>')}
      ${thread["m1"]!(
        toggle(true) +
          markup`
            <div class="msg-body">
              <div >
                <pre class="msg-text">hello <i></pre>
                <dl class="msg-fields"><dt >n</dt><dd >2</dd><dt >why</dt><dd >because</dd></dl>
              </div>
            </div>
          `,
      )}
    </ol>
  </div>
`;

test("every state renders what the h() version did", () => {
  const cases: [Channel | null, string[], () => string][] = [
    [null, [], () => loading],
    [{ ...channel([]), configured: false }, [], unconfigured],
    [{ ...channel([]), error: "no inbox <here>" }, [], () => failed],
    [
      { ...channel([]), alerts: [], approved: true },
      [],
      () => markup`
        <div class="messages">
          ${head(true)}
          <p class="muted msg-none">No messages yet. The pair's first message is the handshake.</p>
        </div>
      `,
    ],
    [channel(messages), [], () => closed(0)],
    [channel(messages), ["m1", "m2", "m3", "m5", "m6"], opened],
  ];
  for (const [ch, ids, want] of cases) {
    const now = new MessagesView();
    open(now, ids, bodies);
    now.render(ch, panes);
    expect(dump(fake(now.el))).toBe(want());
  }
});

test("one view moves between states and still renders what the h() version did", () => {
  const now = new MessagesView();
  const el = now.el;
  const states: [Channel | null, () => string][] = [
    [null, () => loading],
    [channel(messages), () => closed(0)],
    [{ ...channel([]), configured: false }, unconfigured],
    [channel(messages.slice(2)), () => closed(2)],
    [null, () => loading],
  ];
  for (const [ch, want] of states) {
    now.render(ch, panes);
    expect(now.el).toBe(el);
    expect(dump(fake(now.el))).toBe(want());
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
