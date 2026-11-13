import { attach, signal } from "@reactre/core";

import type { LogLine, Route } from "../../src/types.ts";
import { LogView } from "../../src/views/log-view.tsx";
import { check, equal, flush, q, run } from "./lib.ts";

await run(async () => {
  const span = (from: number, to: number): LogLine[] =>
    Array.from({ length: to - from }, (_, index) => ({ time: "09:00", text: `line${from + index}` }));
  const snapshot = (at: number): Route => ({
    dir: "",
    name: "",
    title: "",
    orchestrator_pane: "",
    tasks: null,
    checklist: null,
    files: null,
    modified: "",
    live: true,
    id: "log-browser",
    history: "first",
    run_state: span(at, at + 4),
    older_run_state: at,
  });
  const [route, setRoute] = signal(snapshot(300));
  const root = q(document, "#app");
  const page = async () => ({ lines: span(200, 300), older: 200, history: "first" });
  let dispose = attach(() => <LogView route={route()} page={page} />, root);
  try {
    q<HTMLButtonElement>(root, "button").click();
    await flush();
    const list = q(root, "ol");
    const earlier = [...root.querySelectorAll("li")].find((element) => element.textContent?.endsWith("line200"));
    check("earlier page loads", earlier !== undefined);
    for (let at = 301; at <= 310; at++) setRoute(snapshot(at));
    equal("successive appends retain every loaded line", root.querySelectorAll("li").length, 114);
    check(
      "live updates preserve list and earlier row nodes",
      q(root, "ol") === list && earlier?.parentElement === list,
    );
    dispose();
    dispose = attach(() => <LogView route={snapshot(311)} />, root);
    equal("remount keeps the advanced cache", root.querySelectorAll("li").length, 115);
    dispose();
    dispose = attach(() => <LogView route={route()} />, root);
    setRoute({ ...snapshot(311), history: "second" });
    equal("a new history drops loaded pages", root.querySelectorAll("li").length, 4);
    setRoute(snapshot(311));
    equal("returning to old history does not resurrect pages", root.querySelectorAll("li").length, 4);
    dispose();
    const pendingSnapshot = (at: number): Route => ({ ...snapshot(at), id: "log-browser-pending" });
    const [pending, setPending] = signal(pendingSnapshot(300));
    let resolvePage!: (value: { lines: LogLine[]; older: number; history: string }) => void;
    const readPage = async (_id: string, before: number) => {
      if (before === 300) return page();
      return new Promise<{ lines: LogLine[]; older: number; history: string }>((resolve) => {
        resolvePage = resolve;
      });
    };
    dispose = attach(() => <LogView route={pending()} page={readPage} />, root);
    q<HTMLButtonElement>(root, "button").click();
    await flush();
    const loaded = [...root.querySelectorAll("li")].find((element) => element.textContent?.endsWith("line200"));
    q<HTMLButtonElement>(root, "button").click();
    for (let at = 301; at <= 310; at++) setPending(pendingSnapshot(at));
    resolvePage({ lines: span(100, 200), older: 100, history: "first" });
    await flush();
    equal("delayed second page preserves all live appends", root.querySelectorAll("li").length, 214);
    check("delayed page preserves existing row nodes", loaded?.isConnected === true);
    check("delayed page includes the latest line", root.textContent?.includes("line313") === true);
  } finally {
    dispose();
  }
});
