import { beforeEach, describe, expect, test } from "bun:test";

import { signal } from "@reactre/core";

import { applyCodeTheme } from "./appearance.ts";
import { installFakeDocument, ops, resetOps } from "./herdsman-blocks.testdom.ts";
import { languageOf, tokenizeLines } from "./highlight.ts";
import { CodeBlock, Markdown } from "./markdown.tsx";
import { mount } from "./test-mount.ts";
import { dump, markup } from "./testdom.ts";

// What markdown needs beyond the fake DOM: the page's highlighting switch
// (documentElement's data-code).
const g = globalThis as unknown as { document: Record<string, unknown> };
const page = { dataset: {} as Record<string, string>, style: { setProperty() {}, removeProperty() {} } };
beforeEach(() => {
  installFakeDocument();
  g.document["documentElement"] = page;
  page.dataset = {};
  applyCodeTheme("match", false);
});

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
    applyCodeTheme("off", false);
    expect(dump(mount(() => <Markdown source={sample} />))).toBe(`<div class="md">${blocks.join("")}</div>`);
    const parts = sample.split("\n\n");
    expect(parts.length).toBe(blocks.length);
    parts.forEach((block, i) =>
      expect(dump(mount(() => <Markdown source={block} />))).toBe(`<div class="md">${blocks[i]}</div>`),
    );
  });

  test("text that looks like markup stays text", () => {
    const md = mount(() => <Markdown source="<script>x</script> **<i>b</i>**" />);
    expect(dump(md)).toBe('<div class="md"><p><script>x</script> <strong><i>b</i></strong></p></div>');
    const p = md.childNodes.find((node) => node.nodeType === 1)!;
    expect(p.childNodes.filter((n) => n.nodeType === 1).length).toBe(1); // only the strong
  });

  test("highlighted code: a span per token kind, text between, as the pieces say", () => {
    const lines = ["const x = 1; // one", 'let s = "two";'];
    const lang = languageOf("ts");
    const want = tokenizeLines(lang, lines)
      .map((pieces) => pieces.map(([k, t]) => (k ? `<span class="hl-${k}">${t}</span>` : t)).join(""))
      .join("\n");
    expect(
      dump(
        mount(() => (
          <code>
            <CodeBlock lines={lines} lang={lang} />
          </code>
        )),
      ),
    ).toBe(`<code>${want}</code>`);
    expect(want).toContain("hl-");
    applyCodeTheme("off", false);
    expect(
      mount(() => (
        <code>
          <CodeBlock lines={lines} lang={lang} />
        </code>
      )).textContent,
    ).toBe(lines.join("\n"));
  });

  test("the same document rendered again writes nothing new", () => {
    const [value, setValue] = signal({ source: sample });
    const doc = mount(() => <Markdown source={value().source} />);
    const first = doc.childNodes.find((node) => node.nodeType === 1);
    resetOps();
    setValue({ source: sample });
    expect(doc.childNodes.find((node) => node.nodeType === 1)).toBe(first);
    expect(Object.values(ops).every((count) => count === 0)).toBe(true);
  });

  test("editing a document updates positional blocks and inline text in place", () => {
    const [source, setSource] = signal("# Before\n\nA **bold** word\n\n- [ ] open");
    const doc = mount(() => <Markdown source={source()} />);
    const before = doc.childNodes.filter((node) => node.nodeType === 1);
    setSource("# After\n\nA **new** word\n\n- [x] done");
    const after = doc.childNodes.filter((node) => node.nodeType === 1);
    expect(after.length).toBe(before.length);
    expect(after.map((node, index) => node === before[index])).toEqual([true, true, true]);
    expect(dump(doc)).toBe(
      '<div class="md"><h2>After</h2><p>A <strong>new</strong> word</p><ul><li class="task"><input checked="" disabled="" type="checkbox"></input> done</li></ul></div>',
    );
  });
});
