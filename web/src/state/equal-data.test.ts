import { expect, test } from "bun:test";

import { immutableData } from "@reactre/state/data";

import { equalData } from "./equal-data.ts";
import { encodeValue } from "./wire.ts";

test("JSON equality preserves the old wire comparison without serialization", () => {
  type Datum =
    | null
    | boolean
    | number
    | string
    | number[]
    | { a?: number | null; b?: number; nested?: { value: string }[] }
    | Record<string, { safe: boolean }>;
  const cases: Datum[] = [
    null,
    true,
    false,
    0,
    -0,
    1,
    "",
    "value",
    [],
    [1],
    [1, 2],
    [2, 1],
    {},
    { a: 1 },
    { b: 2, a: 1 },
    { a: 1, b: 2 },
    { a: null },
    { nested: [{ value: "same" }] },
    { ["__proto__"]: { safe: true } },
  ];
  for (const left of cases)
    for (const right of cases) {
      const a = immutableData({ value: left });
      const b = immutableData({ value: right });
      expect(equalData(a, b)).toBe(encodeValue(a) === encodeValue(b));
    }
});
