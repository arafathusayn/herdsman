import { For, memo, Show } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { highlighting } from "../appearance.ts";
import { type DiffLine, sideBySide, type SplitRow as SideRow } from "../diff.ts";
import { languageOf, type Piece, type State, tokenizeLine } from "../highlight.ts";

const rowCap = 3000;

/** Each side carries its own tokenizer state, reset where a new hunk begins. */
function colorLines(lines: DiffLine[], language: string): Map<number, Piece[] | null> {
  let oldState: State | null = null;
  let newState: State | null = null;
  return new Map(
    lines.map((line) => {
      if (line.kind === "hunk") oldState = newState = null;
      if (!language || line.kind === "hunk" || line.kind === "note") return [line.id, null];
      let pieces: Piece[];
      if (line.kind === "del") ({ pieces, state: oldState } = tokenizeLine(language, line.text, oldState));
      else {
        ({ pieces, state: newState } = tokenizeLine(language, line.text, newState));
        if (line.kind === "ctx") oldState = tokenizeLine(language, line.text, oldState).state;
      }
      return [line.id, pieces.length ? pieces : null];
    }),
  );
}

function ColoredText(props: { text: string; pieces: Piece[] | null }) {
  return (
    <Show when={props.pieces} fallback={props.text || " "}>
      {(pieces) => (
        <For each={pieces()} keyed={false}>
          {(piece) => (
            <Show when={piece()[0]} fallback={piece()[1]}>
              <span class={`hl-${piece()[0]}`}>{piece()[1]}</span>
            </Show>
          )}
        </For>
      )}
    </Show>
  );
}

function InlineRow(props: { line: DiffLine; pieces: Piece[] | null }) {
  const marked = memo(() => props.line.kind === "hunk" || props.line.kind === "note");
  return (
    <tr class={`d-${props.line.kind}`}>
      <Show
        when={marked()}
        fallback={
          <>
            <td class="ln">{props.line.old ?? null}</td>
            <td class="ln">{props.line.new ?? null}</td>
            <td class="code">
              <ColoredText text={props.line.text} pieces={props.pieces} />
            </td>
          </>
        }
      >
        <td colspan="3" class="code">
          {props.line.text}
        </td>
      </Show>
    </tr>
  );
}

function SideCells(props: { line: DiffLine | null; right: boolean; code: Map<number, Piece[] | null> }) {
  return (
    <>
      <td class={props.line ? `ln ${props.line.kind}` : "ln blank"}>
        {(props.right ? props.line?.new : props.line?.old) ?? null}
      </td>
      <td class={props.line ? `code ${props.line.kind}` : "code blank"}>
        <Show when={props.line}>
          {(line) => <ColoredText text={line().text} pieces={props.code.get(line().id) ?? null} />}
        </Show>
      </td>
    </>
  );
}

function SplitRow(props: { row: SideRow; code: Map<number, Piece[] | null> }) {
  const mark = memo(() => ("kind" in props.row ? props.row : null));
  const left = memo(() => ("left" in props.row ? props.row.left : null));
  const right = memo(() => ("right" in props.row ? props.row.right : null));
  return (
    <tr class={mark() ? `d-${mark()!.kind}` : ""}>
      <Show
        when={mark()}
        fallback={
          <>
            <SideCells line={left()} right={false} code={props.code} />
            <SideCells line={right()} right={true} code={props.code} />
          </>
        }
      >
        {(mark) => (
          <td colspan="4" class="code">
            {mark().text}
          </td>
        )}
      </Show>
    </tr>
  );
}

/** Rows are keyed by the parser's stable IDs and keep their DOM through a new patch. */
export function DiffTable(props: {
  lines: DiffLine[];
  path: string;
  split: boolean;
  all: boolean;
  showAll: () => void;
}) {
  const shown = memo(() => (props.all ? props.lines : props.lines.slice(0, rowCap)));
  const code = memo(() => colorLines(shown(), highlighting() ? languageOf(props.path) : ""));
  const inline = map(shown, (line) => line, { keyed: (line) => line.id });
  const split = map(
    () => sideBySide(shown()),
    (row) => row,
    { keyed: (row) => row.id },
  );
  return (
    <div>
      <div class={props.split ? "diff-code split" : "diff-code inline"}>
        <table>
          <Show when={props.split}>
            <colgroup>
              <col class="ln-col" />
              <col />
              <col class="ln-col" />
              <col />
            </colgroup>
          </Show>
          <tbody>
            <Show
              when={props.split}
              fallback={
                <For each={inline()} key={(line) => line().id}>
                  {(line) => <InlineRow line={line()} pieces={code().get(line().id) ?? null} />}
                </For>
              }
            >
              <For each={split()} key={(row) => row().id}>
                {(row) => <SplitRow row={row()} code={code()} />}
              </For>
            </Show>
          </tbody>
        </table>
      </div>
      <Show when={shown().length < props.lines.length}>
        <button
          class="btn ghost diff-more"
          onClick={() => props.showAll()}
        >{`Show all ${props.lines.length} lines`}</button>
      </Show>
    </div>
  );
}
