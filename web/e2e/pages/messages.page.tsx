import { attach, signal } from "@reactre/core";

import type { Channel, ChannelMessage } from "../../src/types.ts";
import { createMessagesState, MessagesView } from "../../src/views/messages.tsx";
import { check, equal, flush, q, run } from "./lib.ts";

await run(async () => {
  const root = q(document, "#app");
  const reads: string[] = [];
  const state = createMessagesState(async (_to, id) => {
    reads.push(id);
    if (id === "retry" && reads.filter((read) => read === id).length === 1) throw new Error("Temporary failure");
    return id === "first"
      ? JSON.stringify({ version: 1, text: "hello <img src=x onerror=alert(1)>", count: 2 })
      : "Recovered & safe";
  });
  const message = (id: string, extra: Partial<ChannelMessage> = {}): ChannelMessage => ({
    id,
    from: "adj",
    to: "orch",
    kind: "brief",
    reply_to: "",
    time: 1700000000,
    state: "ready",
    acknowledged: false,
    ...extra,
  });
  const messages = [
    message("first"),
    message("reply", { from: "orch", to: "adj", kind: "result", reply_to: "first" }),
    message("quarantine", { state: "quarantine" }),
    message("unverified", { state: "new" }),
    message("retry"),
  ];
  const channel: Channel = {
    configured: true,
    approved: true,
    pending: false,
    peers: [
      { name: "adj", role: "adjutant", pane: "p1" },
      { name: "orch", role: "orchestrator", pane: "p2" },
    ],
    health: [{ name: "courier", age: 1, ok: true }],
    health_after: 120,
    boxes: [],
    holds: [],
    flags: [],
    alerts: [{ title: "Notice", body: "<script>unsafe()</script>" }],
    messages,
  };
  const [current, setCurrent] = signal<Channel | null>(null);
  const view = () => <MessagesView channel={current()} panes={{ adjutant: "p1", orchestrator: "p2" }} state={state} />;
  let dispose = attach(view, root);
  try {
    check("loading channel state", root.textContent?.includes("Reading the message channel") === true);
    setCurrent({ ...channel, configured: false });
    check("unconfigured channel explains setup", root.textContent?.includes("No message channel") === true);
    setCurrent(channel);
    equal("configured channel renders all messages", root.querySelectorAll(".msg-thread > li").length, 5);
    check(
      "channel alert text stays safe",
      q(root, ".msg-alert").textContent?.includes("<script>unsafe()</script>") === true &&
        root.querySelector("script") === null,
    );
    q<HTMLButtonElement>(root, "#msg-first .msg-toggle").click();
    await flush();
    const first = q(root, "#msg-first");
    equal("opening reads only that body", reads, ["first"]);
    check(
      "message JSON text stays safe",
      q(first, ".msg-text").textContent?.includes("<img src=x") === true && first.querySelector("img") === null,
    );
    check("JSON metadata renders separately", q(first, ".msg-fields").textContent?.includes("count2") === true);
    setCurrent({ ...channel, messages: [...messages.map((entry) => ({ ...entry })), message("appended")] });
    check(
      "metadata snapshots preserve expanded row nodes",
      q(root, "#msg-first") === first && q(first, ".msg-toggle").getAttribute("aria-expanded") === "true",
    );
    q<HTMLButtonElement>(first, ".msg-toggle").click();
    q<HTMLButtonElement>(first, ".msg-toggle").click();
    await flush();
    equal("reopening uses the cached body", reads, ["first"]);
    for (const id of ["quarantine", "unverified"]) {
      check(`${id} has no read control`, q(root, `#msg-${id}`).querySelector(".msg-toggle") === null);
      state.toggle(messages.find((entry) => entry.id === id)!);
    }
    equal("unreadable messages never request a body", reads, ["first"]);
    const retry = q(root, "#msg-retry");
    q<HTMLButtonElement>(retry, ".msg-toggle").click();
    await flush();
    equal("read failure is visible", q(retry, ".error").textContent, "Temporary failure");
    q<HTMLButtonElement>(retry, ".msg-toggle").click();
    q<HTMLButtonElement>(retry, ".msg-toggle").click();
    await flush();
    equal("reopening retries a failed body", q(retry, ".msg-text").textContent, "Recovered & safe");
    dispose();
    dispose = attach(view, root);
    check(
      "tab remount restores expanded bodies",
      q(root, "#msg-first .msg-body").textContent?.includes("hello") === true,
    );
    equal("tab remount does not refetch bodies", reads, ["first", "retry", "retry"]);
  } finally {
    dispose();
  }
});
