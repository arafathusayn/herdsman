"use pure";
import type { Input } from "../terminal.ts";
import { decodeValue, encodeValue } from "./wire.ts";

export function mergeInput(queue: readonly string[], json: string): string[] {
  const next = decodeValue<Input>(json);
  const lastJson = queue.at(-1);
  if (lastJson === undefined) return [json];
  const last = decodeValue<Input>(lastJson);
  let combined: Input;
  if ("text" in last && "text" in next) combined = { text: last.text + next.text };
  else if ("keys" in last && "keys" in next) combined = { keys: [...last.keys, ...next.keys] };
  else if ("lines" in last && "lines" in next)
    combined = { lines: last.lines + next.lines, col: next.col, row: next.row };
  else return [...queue, json];
  return [...queue.slice(0, -1), encodeValue(combined)];
}
