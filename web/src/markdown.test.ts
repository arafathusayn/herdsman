import { beforeEach, describe, expect, test } from "bun:test";

import { child, html, render, type ViewNode } from "./herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "./herdsman-blocks.testdom.ts";
import { languageOf, tokenizeLines } from "./highlight.ts";
import { codeBlock, type CodeNode, renderMarkdown } from "./markdown.ts";
import { dump, markup } from "./testdom.ts";

// What markdown needs beyond the fake DOM: the page's highlighting switch
// (documentElement's data-code).
const g = globalThis as unknown as { document: Record<string, unknown> };
const page = { dataset: {} as Record<string, string> };
beforeEach(() => {
  installFakeDocument();
  g.document["documentElement"] = page;
  page.dataset = {};
});

const rendered = (v: ViewNode): FakeNode => {
  const box = document.createElement("div");
  render(box, v);
  return box.firstChild as unknown as FakeNode;
};

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
  // Each block of the sample, as sample.split("\n\n") cuts it, and its markup.
  const blocks = [
    "<h2>Route: ship the <strong>web</strong> view</h2>",
    markup`
      <p>Some text with <code>code</code>, <strong>bold</strong>, a <a href="https://example.com/a?b=1" rel="noopener noreferrer" target="_blank" title="https://example.com/a?b=1">web link</a> and a <span class="md-link" title="reports/x.md">file link</span> that does not open. <b>not html</b> & co.</p>
    `,
    "<h3>Tasks</h3>",
    markup`
      <ul>
        <li class="task"><input checked="" disabled="" type="checkbox"></input> done task with <code>code</code></li>
        <li class="task"><input disabled="" type="checkbox"></input> open task</li>
        <li>plain item</li>
      </ul>
    `,
    "<ol><li>first</li><li>second with <strong>bold</strong></li></ol>",
    "<blockquote>a quote over two lines</blockquote>",
    markup`
      <div class="table-wrap">
        <table>
          <thead><tr><th>Name</th><th>State</th></tr></thead>
          <tbody>
            <tr><td>a <code>x</code></td><td><strong>ok</strong></td></tr>
            <tr>
              <td>b</td>
              <td><a href="https://e.org" rel="noopener noreferrer" target="_blank" title="https://e.org">link</a></td>
            </tr>
          </tbody>
        </table>
      </div>
    `,
    '<pre data-lang="ts"><code>const x = 1;</code></pre>',
    "<pre><code>plain fence</code></pre>",
    "<h6>deep</h6><p>####### too deep</p>",
    "",
  ];

  test("renders every block kind, whole and block by block", () => {
    page.dataset["code"] = "off";
    expect(dump(rendered(renderMarkdown(sample)))).toBe(`<div class="md">${blocks.join("")}</div>`);
    const parts = sample.split("\n\n");
    expect(parts.length).toBe(blocks.length);
    parts.forEach((block, i) =>
      expect(dump(rendered(renderMarkdown(block)))).toBe(`<div class="md">${blocks[i]}</div>`),
    );
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
