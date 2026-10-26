// A small Markdown renderer for route files: headings, lists, task lists,
// tables, fenced code, quotes, links and inline code. It builds views, so
// file text only ever fills text and attribute holes: it is never parsed as
// HTML.
import { highlighting } from "./appearance.ts";
import { attr, child, cls, html, prop, runs, type RunsNode, text, type ViewNodeOf } from "./herdsman-blocks-view.ts";
import { languageOf, type Piece, tokenizeLines } from "./highlight.ts";

// Inline content: text runs between code, bold and link views.
const Code = html`<code>${text()}</code>`;
const Strong = html`<strong>${text()}</strong>`;
const WebLink = html`<a href=${attr()} title=${attr()} target="_blank" rel="noopener noreferrer">${text()}</a>`;
// Only web links open; file links in route files point at the local disk.
const FileLink = html`<span class="md-link" title=${attr()}>${text()}</span>`;

type InlineView =
  ViewNodeOf<typeof Code> | ViewNodeOf<typeof Strong> | ViewNodeOf<typeof WebLink> | ViewNodeOf<typeof FileLink>;
/** Inline markdown: text and inline elements side by side. */
export type InlineNode = RunsNode<InlineView>;

const inlinePattern = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\([^)\s]+\))/g;

function inline(src: string): InlineNode {
  const out: (string | InlineView)[] = [];
  let last = 0;
  for (const m of src.matchAll(inlinePattern)) {
    const i = m.index ?? 0;
    if (i > last) out.push(src.slice(last, i));
    const tok = m[0];
    if (m[1]) out.push(Code([tok.slice(1, -1)]));
    else if (m[2]) out.push(Strong([tok.slice(2, -2)]));
    else {
      const lm = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(tok);
      const href = lm?.[2] ?? "";
      const label = lm?.[1] ?? tok;
      out.push(/^https?:\/\//.test(href) ? WebLink([href, href, label]) : FileLink([href, label]));
    }
    last = i + tok.length;
  }
  if (last < src.length) out.push(src.slice(last));
  return runs(out);
}

function cells(line: string): string[] {
  return line
    .trim()
    .replace(/^\||\|$/g, "")
    .split("|")
    .map((c) => c.trim());
}

// Highlighted code: a span per token with a kind, text between them.
const Token = html`<span class=${cls()}>${text()}</span>`;

/** Code lines: plain text, or runs of highlighted tokens. */
export type CodeNode = string | RunsNode<ViewNodeOf<typeof Token>>;

/** Code lines, highlighted when the language is known and highlighting is on. */
export function codeBlock(lines: string[], lang: string): CodeNode {
  if (!lang || !highlighting()) return lines.join("\n");
  const out: (string | ViewNodeOf<typeof Token>)[] = [];
  tokenizeLines(lang, lines).forEach((pieces: Piece[], i) => {
    if (i) out.push("\n");
    for (const [k, t] of pieces) out.push(k ? Token([`hl-${k}`, t]) : t);
  });
  return runs(out);
}

// Blocks. Markdown blocks have no identity, so a document is positional runs
// of block views (the counterpart of the unkeyed() list this replaced).
const Pre = html`<pre data-lang=${attr<string | null>()}><code>${child<CodeNode>()}</code></pre>`;
const H2 = html`<h2>${child<InlineNode>()}</h2>`;
const H3 = html`<h3>${child<InlineNode>()}</h3>`;
const H4 = html`<h4>${child<InlineNode>()}</h4>`;
const H5 = html`<h5>${child<InlineNode>()}</h5>`;
const H6 = html`<h6>${child<InlineNode>()}</h6>`;
const headings = [H2, H3, H4, H5, H6] as const;
const Th = html`<th>${child<InlineNode>()}</th>`;
const Td = html`<td>${child<InlineNode>()}</td>`;
const Row = html`<tr>
  ${child<RunsNode<ViewNodeOf<typeof Td>>>()}
</tr>`;
const Table = html`<div class="table-wrap">
  <table>
    <thead>
      <tr>
        ${child<RunsNode<ViewNodeOf<typeof Th>>>()}
      </tr>
    </thead>
    <tbody>
      ${child<RunsNode<ViewNodeOf<typeof Row>>>()}
    </tbody>
  </table>
</div>`;
const Item = html`<li>${child<InlineNode>()}</li>`;
// The space after the box is a text hole: a literal one could be reformatted away.
const Task = html`<li class="task">
  <input type="checkbox" .checked=${prop<HTMLInputElement, "checked">()} disabled />${text()}${child<InlineNode>()}
</li>`;
const Ul = html`<ul>
  ${child<RunsNode<ViewNodeOf<typeof Item> | ViewNodeOf<typeof Task>>>()}
</ul>`;
const Ol = html`<ol>
  ${child<RunsNode<ViewNodeOf<typeof Item>>>()}
</ol>`;
const Quote = html`<blockquote>${child<InlineNode>()}</blockquote>`;
const Para = html`<p>${child<InlineNode>()}</p>`;

type Block =
  | ViewNodeOf<typeof Pre>
  | ViewNodeOf<typeof H2>
  | ViewNodeOf<typeof Table>
  | ViewNodeOf<typeof Ul>
  | ViewNodeOf<typeof Ol>
  | ViewNodeOf<typeof Quote>
  | ViewNodeOf<typeof Para>;

const Doc = html`<div class="md">${child<RunsNode<Block>>()}</div>`;

/** What `renderMarkdown` returns. */
export type MarkdownNode = ViewNodeOf<typeof Doc>;

export function renderMarkdown(src: string): MarkdownNode {
  const blocks: Block[] = [];
  const lines = src.replace(/\r\n/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i] ?? "";
    if (/^```/.test(line)) {
      const body: string[] = [];
      const lang = languageOf(line.slice(3));
      i++;
      while (i < lines.length && !/^```/.test(lines[i] ?? "")) body.push(lines[i++] ?? "");
      i++;
      blocks.push(Pre([lang || null, codeBlock(body, lang)]));
      continue;
    }
    const hm = /^(#{1,6})\s+(.*)$/.exec(line);
    if (hm) {
      // "#" is h2: the page already has its own h1.
      const heading = headings[Math.min(4, (hm[1]?.length ?? 1) - 1)] ?? H6;
      blocks.push(heading([inline(hm[2] ?? "")]));
      i++;
      continue;
    }
    if (/^\s*\|/.test(line) && /^\s*\|[\s:|-]+\|\s*$/.test(lines[i + 1] ?? "")) {
      const head = cells(line);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && /^\s*\|/.test(lines[i] ?? "")) rows.push(cells(lines[i++] ?? ""));
      blocks.push(
        Table([
          runs(head.map((c) => Th([inline(c)]))),
          runs(rows.map((r) => Row([runs(r.map((c) => Td([inline(c)])))]))),
        ]),
      );
      continue;
    }
    if (/^\s*[-*]\s+/.test(line)) {
      const items: (ViewNodeOf<typeof Item> | ViewNodeOf<typeof Task>)[] = [];
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i] ?? "")) {
        const item = (lines[i++] ?? "").replace(/^\s*[-*]\s+/, "");
        const task = /^\[( |x|X)\]\s+(.*)$/.exec(item);
        items.push(task ? Task([task[1] !== " ", " ", inline(task[2] ?? "")]) : Item([inline(item)]));
      }
      blocks.push(Ul([runs(items)]));
      continue;
    }
    if (/^\s*\d+\.\s+/.test(line)) {
      const items: ViewNodeOf<typeof Item>[] = [];
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i] ?? ""))
        items.push(Item([inline((lines[i++] ?? "").replace(/^\s*\d+\.\s+/, ""))]));
      blocks.push(Ol([runs(items)]));
      continue;
    }
    if (/^>\s?/.test(line)) {
      const body: string[] = [];
      while (i < lines.length && /^>\s?/.test(lines[i] ?? "")) body.push((lines[i++] ?? "").replace(/^>\s?/, ""));
      blocks.push(Quote([inline(body.join(" "))]));
      continue;
    }
    if (line.trim() === "") {
      i++;
      continue;
    }
    const para: string[] = [];
    while (
      i < lines.length &&
      (lines[i] ?? "").trim() !== "" &&
      !/^(#|```|\s*[-*]\s|\s*\d+\.\s|>|\s*\|)/.test(lines[i] ?? "")
    )
      para.push(lines[i++] ?? "");
    if (para.length === 0) para.push(lines[i++] ?? "");
    blocks.push(Para([inline(para.join(" "))]));
  }
  return Doc([runs(blocks)]);
}
