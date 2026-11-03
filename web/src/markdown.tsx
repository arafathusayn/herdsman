// Markdown is parsed into data and rendered as native components. File text never becomes HTML.
import { Dynamic, For, Match, memo, Show, Switch } from "@reactre/core";

import { highlighting } from "./appearance.ts";
import { languageOf, type Piece, tokenizeLines } from "./highlight.ts";

interface InlinePart {
  kind: "text" | "code" | "strong" | "web" | "file";
  text: string;
  href: string;
}
const inlinePattern = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\([^)\s]+\))/g;

function inlineParts(source: string): InlinePart[] {
  const parts: InlinePart[] = [];
  let last = 0;
  for (const match of source.matchAll(inlinePattern)) {
    const at = match.index;
    if (at > last) parts.push({ kind: "text", text: source.slice(last, at), href: "" });
    const token = match[0];
    if (match[1]) parts.push({ kind: "code", text: token.slice(1, -1), href: "" });
    else if (match[2]) parts.push({ kind: "strong", text: token.slice(2, -2), href: "" });
    else {
      const link = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(token);
      const href = link?.[2] ?? "";
      parts.push({ kind: /^https?:\/\//.test(href) ? "web" : "file", text: link?.[1] ?? token, href });
    }
    last = at + token.length;
  }
  if (last < source.length) parts.push({ kind: "text", text: source.slice(last), href: "" });
  return parts;
}

function Inline(props: { source: string }) {
  const source = memo(() => props.source);
  const parts = memo(() => inlineParts(source()));
  return (
    <For each={parts()} keyed={false}>
      {(part) => (
        <Switch fallback={part().text}>
          <Match when={part().kind === "code"}>
            <code>{part().text}</code>
          </Match>
          <Match when={part().kind === "strong"}>
            <strong>{part().text}</strong>
          </Match>
          <Match when={part().kind === "web"}>
            <a href={part().href} title={part().href} target="_blank" rel="noopener noreferrer">
              {part().text}
            </a>
          </Match>
          <Match when={part().kind === "file"}>
            <span class="md-link" title={part().href}>
              {part().text}
            </span>
          </Match>
        </Switch>
      )}
    </For>
  );
}

export function CodeBlock(props: { lines: string[]; lang: string }) {
  const pieces = memo(() => {
    if (!props.lang || !highlighting()) return [["", props.lines.join("\n")]] as Piece[];
    const pieces: Piece[] = [];
    tokenizeLines(props.lang, props.lines).forEach((line, index) => {
      if (index) pieces.push(["", "\n"]);
      pieces.push(...line);
    });
    return pieces;
  });
  return (
    <For each={pieces()} keyed={false}>
      {(piece) => (
        <Show when={piece()[0]} fallback={piece()[1]}>
          <span class={`hl-${piece()[0]}`}>{piece()[1]}</span>
        </Show>
      )}
    </For>
  );
}

interface ListItem {
  text: string;
  checked: boolean | null;
}
type Block =
  | { kind: "code"; lines: string[]; lang: string }
  | { kind: "heading"; text: string; level: number }
  | { kind: "table"; head: string[]; rows: string[][] }
  | { kind: "unordered" | "ordered"; items: ListItem[] }
  | { kind: "quote" | "paragraph"; text: string };

function cells(line: string): string[] {
  return line
    .trim()
    .replace(/^\||\|$/g, "")
    .split("|")
    .map((cell) => cell.trim());
}

function parseMarkdown(source: string): Block[] {
  const blocks: Block[] = [];
  const lines = source.replace(/\r\n/g, "\n").split("\n");
  let index = 0;
  while (index < lines.length) {
    const line = lines[index] ?? "";
    if (/^```/.test(line)) {
      const body: string[] = [];
      const lang = languageOf(line.slice(3));
      index++;
      while (index < lines.length && !/^```/.test(lines[index] ?? "")) body.push(lines[index++] ?? "");
      index++;
      blocks.push({ kind: "code", lines: body, lang });
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      blocks.push({ kind: "heading", text: heading[2] ?? "", level: Math.min(4, (heading[1]?.length ?? 1) - 1) });
      index++;
      continue;
    }
    if (/^\s*\|/.test(line) && /^\s*\|[\s:|-]+\|\s*$/.test(lines[index + 1] ?? "")) {
      const head = cells(line);
      index += 2;
      const rows: string[][] = [];
      while (index < lines.length && /^\s*\|/.test(lines[index] ?? "")) rows.push(cells(lines[index++] ?? ""));
      blocks.push({ kind: "table", head, rows });
      continue;
    }
    if (/^\s*[-*]\s+/.test(line)) {
      const items: ListItem[] = [];
      while (index < lines.length && /^\s*[-*]\s+/.test(lines[index] ?? "")) {
        const item = (lines[index++] ?? "").replace(/^\s*[-*]\s+/, "");
        const task = /^\[( |x|X)\]\s+(.*)$/.exec(item);
        items.push({ checked: task ? task[1] !== " " : null, text: task ? (task[2] ?? "") : item });
      }
      blocks.push({ kind: "unordered", items });
      continue;
    }
    if (/^\s*\d+\.\s+/.test(line)) {
      const items: ListItem[] = [];
      while (index < lines.length && /^\s*\d+\.\s+/.test(lines[index] ?? ""))
        items.push({ checked: null, text: (lines[index++] ?? "").replace(/^\s*\d+\.\s+/, "") });
      blocks.push({ kind: "ordered", items });
      continue;
    }
    if (/^>\s?/.test(line)) {
      const body: string[] = [];
      while (index < lines.length && /^>\s?/.test(lines[index] ?? ""))
        body.push((lines[index++] ?? "").replace(/^>\s?/, ""));
      blocks.push({ kind: "quote", text: body.join(" ") });
      continue;
    }
    if (line.trim() === "") {
      index++;
      continue;
    }
    const paragraph: string[] = [];
    while (
      index < lines.length &&
      (lines[index] ?? "").trim() !== "" &&
      !/^(#|```|\s*[-*]\s|\s*\d+\.\s|>|\s*\|)/.test(lines[index] ?? "")
    )
      paragraph.push(lines[index++] ?? "");
    if (!paragraph.length) paragraph.push(lines[index++] ?? "");
    blocks.push({ kind: "paragraph", text: paragraph.join(" ") });
  }
  return blocks;
}

const headings = ["h2", "h3", "h4", "h5", "h6"] as const;
function BlockView(props: { block: Block }) {
  const headingTag = memo(() => (props.block.kind === "heading" ? (headings[props.block.level] ?? "h6") : "h6"));
  const listTag = memo(() => (props.block.kind === "ordered" ? "ol" : "ul"));
  const textTag = memo(() => (props.block.kind === "quote" ? "blockquote" : "p"));
  return (
    <Switch>
      <Match when={props.block.kind === "code" ? props.block : null}>
        {(block) => (
          <pre data-lang={block().lang || null}>
            <code>
              <CodeBlock lines={block().lines} lang={block().lang} />
            </code>
          </pre>
        )}
      </Match>
      <Match when={props.block.kind === "heading" ? props.block : null}>
        {(block) => (
          <Dynamic component={headingTag()}>
            <Inline source={block().text} />
          </Dynamic>
        )}
      </Match>
      <Match when={props.block.kind === "table" ? props.block : null}>
        {(block) => (
          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <For each={block().head} keyed={false}>
                    {(cell) => (
                      <th>
                        <Inline source={cell()} />
                      </th>
                    )}
                  </For>
                </tr>
              </thead>
              <tbody>
                <For each={block().rows} keyed={false}>
                  {(row) => (
                    <tr>
                      <For each={row()} keyed={false}>
                        {(cell) => (
                          <td>
                            <Inline source={cell()} />
                          </td>
                        )}
                      </For>
                    </tr>
                  )}
                </For>
              </tbody>
            </table>
          </div>
        )}
      </Match>
      <Match when={props.block.kind === "unordered" || props.block.kind === "ordered" ? props.block : null}>
        {(block) => (
          <Dynamic component={listTag()}>
            <For each={block().items} keyed={false}>
              {(item) => (
                <li class={item().checked !== null ? "task" : ""}>
                  <Show when={item().checked !== null}>
                    <input type="checkbox" checked={item().checked ?? false} disabled />{" "}
                  </Show>
                  <Inline source={item().text} />
                </li>
              )}
            </For>
          </Dynamic>
        )}
      </Match>
      <Match when={props.block.kind === "quote" || props.block.kind === "paragraph" ? props.block : null}>
        {(block) => (
          <Dynamic component={textTag()}>
            <Inline source={block().text} />
          </Dynamic>
        )}
      </Match>
    </Switch>
  );
}

export function Markdown(props: { source: string }) {
  const source = memo(() => props.source);
  const blocks = memo(() => parseMarkdown(source()));
  return (
    <div class="md">
      <For each={blocks()} keyed={false}>
        {(block) => <BlockView block={block()} />}
      </For>
    </div>
  );
}
