// Fuzzy matching for the file search and the command palette.
import { html, runs, type RunsNode, text, type ViewNodeOf } from "./herdsman-blocks-view.ts";

const separators = "/-_. :";

function boundary(t: string, i: number): boolean {
  return i === 0 || separators.includes(t[i - 1] ?? "");
}

/**
 * Fuzzy match in the manner of VS Code's quick open: the query is a
 * substring, or its characters appear in order where each new run of
 * characters starts at a word boundary (after / - _ . : or a space), with at
 * most one run allowed to start inside a word. So "rvc" finds "rv-codex" and
 * "spr" finds "Split right", but "split" does not match letters scattered
 * across a long path.
 */
export function fuzzy(query: string, text: string): { score: number; hits: number[] } | null {
  const q = query.toLowerCase().replace(/\s+/g, "");
  const t = text.toLowerCase();
  if (!q) return { score: 0, hits: [] };
  const base = t.lastIndexOf("/") + 1;
  const finish = (hits: number[], score: number) => ({
    // Prefer matches in the file name over its folder, and shorter texts.
    score: score + hits.filter((i) => i >= base).length * 0.5 - t.length / 100,
    hits,
  });

  // A substring, best at a word boundary.
  let sub = -1;
  for (let at = t.indexOf(q); at >= 0; at = t.indexOf(q, at + 1)) {
    if (sub < 0) sub = at;
    if (boundary(t, at)) {
      sub = at;
      break;
    }
  }
  if (sub >= 0) {
    const hits = Array.from({ length: q.length }, (_, k) => sub + k);
    return finish(hits, 10 + q.length * 4 + (boundary(t, sub) ? 6 : 0) - sub / 50);
  }

  const hits: number[] = [];
  let score = 0;
  let midRuns = 0;
  let from = 0;
  for (const ch of q) {
    const prev = hits.at(-1);
    let at = -1;
    if (prev !== undefined && t[prev + 1] === ch) at = prev + 1;
    else {
      for (let k = t.indexOf(ch, from); k >= 0; k = t.indexOf(ch, k + 1)) {
        if (boundary(t, k)) {
          at = k;
          break;
        }
      }
      if (at < 0) {
        at = t.indexOf(ch, from);
        if (at < 0 || ++midRuns > 1) return null;
      }
    }
    score += prev !== undefined && at === prev + 1 ? 4 : boundary(t, at) ? 3 : 1;
    hits.push(at);
    from = at + 1;
  }
  return finish(hits, score);
}

const Mark = html`<mark>${text()}</mark>`;

/** What `highlight` returns: text runs with the matched ones in `<mark>`, for a child hole. */
export type HighlightNode = RunsNode<ViewNodeOf<typeof Mark>>;

/** The text with the characters at `hits` (from `fuzzy`) marked, run by run, with no wrapper. */
export function highlight(value: string, hits: readonly number[]): HighlightNode {
  if (!hits.length) return runs<ViewNodeOf<typeof Mark>>([value]);
  const set = new Set(hits);
  const out: (string | ViewNodeOf<typeof Mark>)[] = [];
  let run = "";
  let inHit = false;
  for (let i = 0; i < value.length; i++) {
    const hit = set.has(i);
    if (hit !== inHit && run) {
      out.push(inHit ? Mark([run]) : run);
      run = "";
    }
    inHit = hit;
    run += value[i];
  }
  if (run) out.push(inHit ? Mark([run]) : run);
  return runs(out);
}
