// The channel's metadata and message bodies update independently of the app.
import { batch, For, memo, Show, signal } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { messageBody } from "../api.ts";
import { describeFlag, healthLabel, kindLabel, stateLabel, stateTip } from "../channel.ts";
import { Icon } from "../components/icons.tsx";
import { icons } from "../icons.ts";
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

type Loaded = { text?: string; error?: string };
export type Peers = { adjutant: string; orchestrator: string };
export interface MessagesState {
  expanded: () => Set<string>;
  bodies: () => Map<string, Loaded>;
  toggle: (message: ChannelMessage) => void;
}

/** Body requests and expansion survive a tab switch without a global redraw. */
export function createMessagesState(read: typeof messageBody = messageBody): MessagesState {
  const [expanded, setExpanded] = signal(new Set<string>());
  const [bodies, setBodies] = signal(new Map<string, Loaded>());
  const put = (id: string, value: Loaded): void => setBodies((current) => new Map([...current, [id, value]]));
  const toggle = (message: ChannelMessage): void => {
    if (message.state !== "ready" && message.state !== "cur") return;
    const closing = expanded().has(message.id);
    batch(() => {
      setExpanded((current) => {
        const next = new Set(current);
        if (closing) next.delete(message.id);
        else next.add(message.id);
        return next;
      });
      if (!closing && (!bodies().has(message.id) || bodies().get(message.id)?.error)) {
        put(message.id, {});
        void read(message.to, message.id)
          .then((text) => put(message.id, { text }))
          .catch((error: unknown) =>
            put(message.id, { error: error instanceof Error ? error.message : String(error) }),
          );
      }
    });
  };
  return { expanded, bodies, toggle };
}

function MessageBody(props: { text: string }) {
  const parsed = memo(() => {
    let value: unknown;
    try {
      value = JSON.parse(props.text);
    } catch {
      return null;
    }
    if (!value || typeof value !== "object" || Array.isArray(value)) return null;
    return value as Record<string, unknown>;
  });
  const fields = memo(() =>
    Object.entries(parsed() ?? {})
      .filter(([key]) => key !== "text" && key !== "version")
      .map(([key, value]) => ({ key, value: typeof value === "string" ? value : JSON.stringify(value) })),
  );
  return (
    <Show when={parsed()} fallback={<pre class="msg-text">{props.text}</pre>}>
      {(value) => (
        <div>
          <Show when={typeof value()["text"] === "string"}>
            <pre class="msg-text">{String(value()["text"])}</pre>
          </Show>
          <Show when={fields().length > 0}>
            <dl class="msg-fields">
              <For each={fields()} key={(field) => field.key}>
                {(field) => (
                  <>
                    <dt>{field.key}</dt>
                    <dd>{field.value}</dd>
                  </>
                )}
              </For>
            </dl>
          </Show>
        </div>
      )}
    </Show>
  );
}

function Peer(props: { channel: Channel; panes: Peers; role: "adjutant" | "orchestrator" }) {
  const peer = memo(() => props.channel.peers.find((peer) => peer.role === props.role));
  const pane = memo(() => peer()?.pane || props.panes[props.role]);
  return (
    <button
      type="button"
      class={`msg-peer ${props.role}`}
      disabled={!pane()}
      title={pane() ? `Open ${props.role} pane ${pane()}` : `No ${props.role} enrolled`}
      onClick={() => {
        if (pane()) store.go({ pane: pane() });
      }}
    >
      <span class="msg-role">{props.role === "adjutant" ? "Adjutant" : "Orchestrator"}</span>
      <span class="msg-name">{peer()?.name ?? "—"}</span>
    </button>
  );
}

interface ChannelChip {
  id: string;
  cls: string;
  tip: string | null;
  text: string;
}
function channelChips(channel: Channel): ChannelChip[] {
  const boxes = channel.boxes.filter(
    (box) => box.unacked || box.claim !== "none" || box.counts["quarantine"] || box.orphan,
  );
  return [
    ...channel.health.map((health) => ({
      id: `health:${health.name}`,
      cls: health.ok ? "chip ok" : "chip bad",
      tip: `Heartbeat ${seconds(health.age)}; down after ${channel.health_after}s`,
      text: `${healthLabel(health.name)} ${health.ok ? "up" : "down"}`,
    })),
    ...[...new Set(channel.flags ?? [])].map((flag) => {
      const [text, tip] = describeFlag(flag);
      return { id: `flag:${flag}`, cls: "chip bad", tip, text };
    }),
    ...(channel.holds ?? []).map((hold) => ({
      id: `hold:${hold.name}`,
      cls: "chip warn",
      tip: hold.owners?.length
        ? `Held by ${hold.owners.map((owner) => (owner.reason ? `${owner.by}: ${owner.reason}` : owner.by)).join("; ")}`
        : "A hold or resume intent in the channel's state folder",
      text: hold.name,
    })),
    ...boxes.map((box) => ({
      id: `box:${box.name}`,
      cls: box.counts["quarantine"] || box.orphan ? "chip warn" : "chip",
      tip: null,
      text:
        `${box.name}: ${box.unacked} unread` +
        (box.claim !== "none" ? ` · claim ${box.claim}` : "") +
        (box.counts["quarantine"] ? ` · ${box.counts["quarantine"]} quarantined` : "") +
        (box.orphan ? " · not enrolled" : ""),
    })),
  ];
}

function Header(props: { channel: Channel; panes: Peers }) {
  const chips = map(
    () => channelChips(props.channel),
    (chip) => chip,
    { keyed: (chip) => chip.id },
  );
  return (
    <div class="msg-head">
      <div class="msg-pair">
        <Peer channel={props.channel} panes={props.panes} role="adjutant" />
        <Icon paths={icons.arrow} />
        <Peer channel={props.channel} panes={props.panes} role="orchestrator" />
      </div>
      <div class="msg-chips">
        <span
          class={props.channel.approved ? "chip ok" : "chip warn"}
          data-tip={
            props.channel.approved
              ? "The human approved this pair; delivery runs"
              : "A registry change waits for the human's approval; delivery is held"
          }
        >
          {props.channel.approved ? "Pair approved" : "Approval pending"}
        </span>
        <For each={chips()} key={(chip) => chip().id}>
          {(chip) => (
            <span class={chip().cls} data-tip={chip().tip}>
              {chip().text}
            </span>
          )}
        </For>
      </div>
    </div>
  );
}

function Message(props: { message: ChannelMessage; channel: Channel; state: MessagesState }) {
  const label = memo(() => stateLabel(props.message));
  const readable = memo(() => props.message.state === "ready" || props.message.state === "cur");
  const open = memo(() => props.state.expanded().has(props.message.id));
  const body = memo(() => props.state.bodies().get(props.message.id));
  const side = memo(() =>
    props.channel.peers.find((peer) => peer.name === props.message.from)?.role === "orchestrator" ? "right" : "left",
  );
  return (
    <li
      class={`msg ${side()} k-${props.message.kind}${props.message.state === "quarantine" ? " quarantined" : ""}`}
      id={`msg-${props.message.id}`}
    >
      <div class="msg-meta">
        <span class={`msg-kind k-${props.message.kind}`}>{kindLabel[props.message.kind] ?? props.message.kind}</span>
        <span class="msg-route">{`${props.message.from} → ${props.message.to}`}</span>
        <span class={`msg-state s-${label().replace(" ", "-").toLowerCase()}`} data-tip={stateTip[label()] ?? null}>
          {label()}
        </span>
        <time title={new Date(props.message.time * 1000).toLocaleString()}>{when(props.message.time)}</time>
      </div>
      <div class="msg-sub">
        <code>{props.message.id}</code>
        <Show when={props.message.reply_to}>
          <a
            class="msg-reply"
            href={`#msg-${props.message.reply_to}`}
            onClick={(event) => {
              event.preventDefault();
              if (props.channel.messages.some((message) => message.id === props.message.reply_to))
                document
                  .getElementById(`msg-${props.message.reply_to}`)
                  ?.scrollIntoView({ block: "center", behavior: "smooth" });
            }}
          >{`↩ ${props.message.reply_to}`}</a>
        </Show>
      </div>
      <Show
        when={readable()}
        fallback={
          <p class="msg-note">
            {props.message.state === "quarantine"
              ? "Failed the courier's checks. The body is not shown; the sender must send a fresh message."
              : "Waiting for the courier to verify it."}
          </p>
        }
      >
        <button
          type="button"
          class="btn ghost msg-toggle"
          aria-expanded={open() ? "true" : "false"}
          onClick={() => props.state.toggle(props.message)}
        >
          <Icon paths={icons.chevronDown} />
          {open() ? "Hide" : "Show message"}
        </button>
      </Show>
      <Show when={open() && readable()}>
        <div class="msg-body">
          <Show
            when={body()?.text !== undefined}
            fallback={
              <Show when={body()?.error} fallback={<p class="muted">Loading…</p>}>
                <p class="error">{body()?.error}</p>
              </Show>
            }
          >
            <MessageBody text={body()?.text ?? ""} />
          </Show>
        </div>
      </Show>
    </li>
  );
}

function Conversation(props: { channel: Channel; panes: Peers; state: MessagesState }) {
  const alerts = map(
    () => [...new Map(props.channel.alerts.map((alert) => [JSON.stringify([alert.title, alert.body]), alert]))],
    (alert) => alert,
    { keyed: ([key]) => key },
  );
  const messages = map(
    () => [...props.channel.messages].reverse(),
    (message) => message,
    { keyed: (message) => message.id },
  );
  return (
    <>
      <Header channel={props.channel} panes={props.panes} />
      <Show when={alerts().length > 0}>
        <div class="msg-alerts" role="alert">
          <For each={alerts()} key={(alert) => alert()[0]}>
            {(alert) => (
              <div class="msg-alert">
                <strong>{alert()[1].title}</strong>
                <span>{alert()[1].body}</span>
              </div>
            )}
          </For>
        </div>
      </Show>
      <Show
        when={messages().length > 0}
        fallback={<p class="muted msg-none">No messages yet. The pair's first message is the handshake.</p>}
      >
        <ol class="msg-thread">
          <For each={messages()} key={(message) => message().id}>
            {(message) => <Message message={message()} channel={props.channel} state={props.state} />}
          </For>
        </ol>
      </Show>
    </>
  );
}

export function MessagesView(props: { channel: Channel | null; panes: Peers; state: MessagesState }) {
  return (
    <div class="messages">
      <Show when={props.channel} fallback={<p class="muted">Reading the message channel…</p>}>
        {(channel) => (
          <Show
            when={channel().configured}
            fallback={
              <div class="empty">
                <Icon paths={icons.message} />
                <h2>No message channel</h2>
                <p>
                  {
                    "With an adjutant, the user talks to it and it relays to the orchestrator through herdsman messages. Start "
                  }
                  <code>{"herdsman web --inbox <private inbox folder>"}</code>
                  {" (or set "}
                  <code>HERDSMAN_INBOX</code>
                  {") to show them here."}
                </p>
              </div>
            }
          >
            <Show
              when={!channel().error}
              fallback={
                <div class="empty">
                  <h2>Cannot read the message channel</h2>
                  <p>{channel().error}</p>
                </div>
              }
            >
              <Conversation channel={channel()} panes={props.panes} state={props.state} />
            </Show>
          </Show>
        )}
      </Show>
    </div>
  );
}
