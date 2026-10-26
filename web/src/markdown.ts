// A small Markdown renderer for route files: headings, lists, task lists,
// tables, fenced code, quotes, links and inline code. It builds DOM nodes
// through h(), so file text is never parsed as HTML.
import { highlighting } from "./appearance.ts";
import { h, type Unkeyed, unkeyed } from "./dom.ts";
import { languageOf, renderPieces, tokenizeLines } from "./highlight.ts";

const inlinePattern = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\([^)\s]+\))/g;

function inline(text: string): Unkeyed {
  const out: (Node | string)[] = [];
  let last = 0;
  for (const m of text.matchAll(inlinePattern)) {
    const i = m.index ?? 0;
    if (i > last) out.push(text.slice(last, i));
    const tok = m[0];
    if (m[1]) out.push(h("code", null, tok.slice(1, -1)));
    else if (m[2]) out.push(h("strong", null, tok.slice(2, -2)));
    else {
      const lm = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(tok);
      const href = lm?.[2] ?? "";
      // Only web links open; file links in route files point at the local disk.
      out.push(
        /^https?:\/\//.test(href)
          ? h(
              "a",
              {
                href,
                title: href,
                target: "_blank",
                rel: "noopener noreferrer",
              },
              lm?.[1] ?? tok,
            )
          : h("span", { class: "md-link", title: href }, lm?.[1] ?? tok),
      );
    }
    last = i + tok.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return unkeyed(out);
}

function cells(line: string): string[] {
  return line
    .trim()
    .replace(/^\||\|$/g, "")
    .split("|")
    .map((c) => c.trim());
}

/** Code lines, highlighted when the language is known and highlighting is on. */
export function codeBlock(lines: string[], lang: string): Node | string {
  if (!lang || !highlighting()) return lines.join("\n");
  const frag = document.createDocumentFragment();
  tokenizeLines(lang, lines).forEach((pieces, i) => {
    if (i) frag.append("\n");
    frag.append(renderPieces(pieces));
  });
  return frag;
}

export function renderMarkdown(src: string): HTMLElement {
  const root = h("div", { class: "md" });
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
      root.append(h("pre", lang ? { dataset: { lang } } : null, h("code", null, codeBlock(body, lang))));
      continue;
    }
    const hm = /^(#{1,6})\s+(.*)$/.exec(line);
    if (hm) {
      const level = Math.min(6, (hm[1]?.length ?? 1) + 1) as 2 | 3 | 4 | 5 | 6;
      root.append(h(`h${level}`, null, inline(hm[2] ?? "")));
      i++;
      continue;
    }
    if (/^\s*\|/.test(line) && /^\s*\|[\s:|-]+\|\s*$/.test(lines[i + 1] ?? "")) {
      const head = cells(line);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && /^\s*\|/.test(lines[i] ?? "")) rows.push(cells(lines[i++] ?? ""));
      root.append(
        h(
          "div",
          { class: "table-wrap" },
          h(
            "table",
            null,
            h("thead", null, h("tr", null, unkeyed(head.map((c) => h("th", null, inline(c)))))),
            h("tbody", null, unkeyed(rows.map((r) => h("tr", null, unkeyed(r.map((c) => h("td", null, inline(c)))))))),
          ),
        ),
      );
      continue;
    }
    if (/^\s*[-*]\s+/.test(line)) {
      const ul = h("ul");
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i] ?? "")) {
        const item = (lines[i++] ?? "").replace(/^\s*[-*]\s+/, "");
        const task = /^\[( |x|X)\]\s+(.*)$/.exec(item);
        ul.append(
          task
            ? h(
                "li",
                { class: "task" },
                h("input", {
                  type: "checkbox",
                  checked: task[1] !== " ",
                  disabled: true,
                }),
                " ",
                inline(task[2] ?? ""),
              )
            : h("li", null, inline(item)),
        );
      }
      root.append(ul);
      continue;
    }
    if (/^\s*\d+\.\s+/.test(line)) {
      const ol = h("ol");
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i] ?? ""))
        ol.append(h("li", null, inline((lines[i++] ?? "").replace(/^\s*\d+\.\s+/, ""))));
      root.append(ol);
      continue;
    }
    if (/^>\s?/.test(line)) {
      const body: string[] = [];
      while (i < lines.length && /^>\s?/.test(lines[i] ?? "")) body.push((lines[i++] ?? "").replace(/^>\s?/, ""));
      root.append(h("blockquote", null, inline(body.join(" "))));
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
    root.append(h("p", null, inline(para.join(" "))));
  }
  return root;
}
