import { afterEach, beforeEach, describe, expect, test } from "bun:test";

import { child, html, render, type ViewNode } from "./herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { languageOf, tokenizeLines } from "./highlight.ts";
import { codeBlock, type CodeNode, renderMarkdown } from "./markdown.ts";
import { dump } from "./testdom.ts";
import { h, type Unkeyed, unkeyed } from "./testref.ts";

// What the old builder needs beyond the fake DOM: append, and the page's
// highlighting switch (documentElement's data-code).
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
const g = globalThis as unknown as { document: Record<string, unknown> };
const page = { dataset: {} as Record<string, string> };
beforeEach(() => {
  installFakeDocument();
  g.document["documentElement"] = page;
  page.dataset = {};
  proto["append"] = function (this: FakeElement, ...ns: FakeNode[]) {
    for (const n of ns) this.appendChild(n);
  };
});
afterEach(() => {
  delete proto["append"];
});

const rendered = (v: ViewNode): FakeNode => {
  const box = document.createElement("div");
  render(box, v);
  return box.firstChild as unknown as FakeNode;
};

// The h() renderer renderMarkdown() replaced (with highlighting off), kept to
// prove the views render the same DOM.
const oldPattern = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\([^)\s]+\))/g;

function oldInline(text: string): Unkeyed {
  const out: (Node | string)[] = [];
  let last = 0;
  for (const m of text.matchAll(oldPattern)) {
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

function oldCells(line: string): string[] {
  return line
    .trim()
    .replace(/^\||\|$/g, "")
    .split("|")
    .map((c) => c.trim());
}

// Highlighting is off for the comparison; the highlighted path has its own test.
function oldCodeBlock(lines: string[], _lang: string): string {
  return lines.join("\n");
}

function oldRenderMarkdown(src: string): HTMLElement {
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
      root.append(h("pre", lang ? { dataset: { lang } } : null, h("code", null, oldCodeBlock(body, lang))));
      continue;
    }
    const hm = /^(#{1,6})\s+(.*)$/.exec(line);
    if (hm) {
      const level = Math.min(6, (hm[1]?.length ?? 1) + 1) as 2 | 3 | 4 | 5 | 6;
      root.append(h(`h${level}`, null, oldInline(hm[2] ?? "")));
      i++;
      continue;
    }
    if (/^\s*\|/.test(line) && /^\s*\|[\s:|-]+\|\s*$/.test(lines[i + 1] ?? "")) {
      const head = oldCells(line);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && /^\s*\|/.test(lines[i] ?? "")) rows.push(oldCells(lines[i++] ?? ""));
      root.append(
        h(
          "div",
          { class: "table-wrap" },
          h(
            "table",
            null,
            h("thead", null, h("tr", null, unkeyed(head.map((c) => h("th", null, oldInline(c)))))),
            h(
              "tbody",
              null,
              unkeyed(rows.map((r) => h("tr", null, unkeyed(r.map((c) => h("td", null, oldInline(c))))))),
            ),
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
                oldInline(task[2] ?? ""),
              )
            : h("li", null, oldInline(item)),
        );
      }
      root.append(ul);
      continue;
    }
    if (/^\s*\d+\.\s+/.test(line)) {
      const ol = h("ol");
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i] ?? ""))
        ol.append(h("li", null, oldInline((lines[i++] ?? "").replace(/^\s*\d+\.\s+/, ""))));
      root.append(ol);
      continue;
    }
    if (/^>\s?/.test(line)) {
      const body: string[] = [];
      while (i < lines.length && /^>\s?/.test(lines[i] ?? "")) body.push((lines[i++] ?? "").replace(/^>\s?/, ""));
      root.append(h("blockquote", null, oldInline(body.join(" "))));
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
    root.append(h("p", null, oldInline(para.join(" "))));
  }
  return root;
}

const sample = `# Route: ship the **web** view

Some text with \`code\`, **bold**, a [web link](https://example.com/a?b=1)
and a [file link](reports/x.md) that does not open. <b>not html</b> & co.

## Tasks

- [x] done task with \`code\`
- [ ] open task
* plain item

1. first
2. second with **bold**

> a quote
> over two lines

| Name | State |
| ---- | :---: |
| a \`x\` | **ok** |
| b | [link](https://e.org) |

\`\`\`ts
const x = 1;
\`\`\`

\`\`\`
plain fence
\`\`\`

###### deep
####### too deep

`;

describe("markdown as views", () => {
  test("renders what the h() version did, block by block", () => {
    page.dataset["code"] = "off";
    const old = dump(oldRenderMarkdown(sample) as unknown as FakeNode);
    expect(dump(rendered(renderMarkdown(sample)))).toBe(old);
    for (const block of sample.split("\n\n"))
      expect(dump(rendered(renderMarkdown(block)))).toBe(dump(oldRenderMarkdown(block) as unknown as FakeNode));
  });

  test("text that looks like markup stays text", () => {
    const md = rendered(renderMarkdown("<script>x</script> **<i>b</i>**"));
    expect(dump(md)).toBe('<div class="md"><p><script>x</script> <strong><i>b</i></strong></p></div>');
    const p = (md as FakeElement).firstChild!;
    expect(p.childNodes.filter((n) => n.nodeType === 1).length).toBe(1); // only the strong
  });

  test("highlighted code: a span per token kind, text between, as the pieces say", () => {
    const lines = ["const x = 1; // one", 'let s = "two";'];
    const lang = languageOf("ts");
    const Code = html`<code>${child<CodeNode>()}</code>`;
    const want = tokenizeLines(lang, lines)
      .map((pieces) => pieces.map(([k, t]) => (k ? `<span class="hl-${k}">${t}</span>` : t)).join(""))
      .join("\n");
    expect(dump(rendered(Code([codeBlock(lines, lang)])))).toBe(`<code>${want}</code>`);
    expect(want).toContain("hl-");
    page.dataset["code"] = "off";
    expect(codeBlock(lines, lang)).toBe(lines.join("\n"));
  });

  test("the same document rendered again writes nothing new", () => {
    const box = document.createElement("div");
    render(box, renderMarkdown(sample));
    const first = box.firstChild!.firstChild;
    render(box, renderMarkdown(sample));
    expect(box.firstChild!.firstChild).toBe(first); // positional runs: blocks patched in place
  });
});
