// Runs every scenario in ./cases.tsx in order and leaves a transcript per
// scenario in window.__results for ../react-compat.ts to compare.
import { cases, type T } from "./cases.tsx";
import { libraryCases } from "./libraries.tsx";

declare global {
  interface Window {
    __ssr: Record<string, string>;
    __results: Record<string, string[]>;
    __done: boolean;
  }
}

const settle = async (): Promise<void> => {
  for (let i = 0; i < 6; i++) await new Promise((r) => setTimeout(r, 0));
};

async function main(): Promise<void> {
  const results: Record<string, string[]> = {};
  for (const [name, run] of Object.entries({ ...cases, ...libraryCases })) {
    const out: string[] = (results[name] = []);
    // useId values follow each runtime's own scheme (they only have to be
    // unique and stable), so they are numbered by first appearance.
    const ids = new Map<string, string>();
    const norm = (s: string) =>
      s.replace(/_[rR]_[0-9a-z]+_|«[rR][0-9a-z]+»|:[rR][0-9a-z]+:/g, (id) => {
        let v = ids.get(id);
        if (v === undefined) ids.set(id, (v = "<id" + ids.size + ">"));
        return v;
      });
    console.log("scenario", name);
    document.body.innerHTML = '<div id="root"></div><div id="other"></div>';
    const root = document.getElementById("root")!;
    const other = document.getElementById("other")!;
    const t: T = {
      root,
      other,
      ssr: window.__ssr ?? {},
      log: (...parts) =>
        out.push(norm("log: " + parts.map((p) => (typeof p === "string" ? p : (JSON.stringify(p) ?? String(p)))).join(" "))),
      async step(label) {
        await settle();
        out.push(norm(`step ${label}: ${root.innerHTML}` + (other.innerHTML ? ` | other: ${other.innerHTML}` : "")));
      },
      $(sel) {
        const el = document.querySelector(sel);
        if (!el) throw new Error("no element " + sel);
        return el as HTMLElement;
      },
    };
    const onError = (e: ErrorEvent) => out.push("uncaught: " + (e.error?.message ?? e.message));
    window.addEventListener("error", onError);
    try {
      await run(t);
      await settle();
    } catch (e) {
      out.push("threw: " + (e instanceof Error ? e.message : String(e)));
    }
    window.removeEventListener("error", onError);
  }
  window.__results = results;
  window.__done = true;
}

void main();
