// Unit tests for herdsman-react-compiler: what it compiles to templates and
// what it leaves to the plain runtime. That compiled code renders what React
// renders is checked in headless Chrome by bench/herdsman-cases/react-compat.ts
// (the herdsman-react+compiler runtimes).

import { describe, expect, test } from "bun:test";

import { compileSource } from "./herdsman-react-compiler.ts";

const templates = (out: string): unknown[] =>
  [...out.matchAll(/__hrTemplate\((.*)\);$/gm)].map((m) => JSON.parse(m[1]!) as unknown);

describe("herdsman-react-compiler", () => {
  test("a host tree becomes one template; its expressions become holes in order", () => {
    const out = compileSource(
      `export const Row = ({ r, sel, pick }) => <tr className={sel ? "danger" : ""}><td className="col">{r.id}</td><td><a onClick={() => pick(r.id)}>{r.label}</a></td></tr>;`,
    );
    expect(templates(out)).toEqual([
      ["tr", { className: { h: 0 } }, ["td", { className: "col" }, 1], ["td", 0, ["a", { onClick: { h: 2 } }, 3]]],
    ]);
    expect(out).toContain(`__hrBlock(__hrT0, null, [sel ? "danger" : "", r.id, () => pick(r.id), r.label])`);
    expect(out).toContain(`import { __template as __hrTemplate, __block as __hrBlock } from "react";`);
  });

  test("literal props and text are static; JSX whitespace rules apply", () => {
    const out = compileSource(`export const A = () => <p id="x" tabIndex={0} hidden>
      Hello &amp; welcome
    </p>;`);
    expect(templates(out)).toEqual([["p", { id: "x", tabIndex: 0, hidden: true }, "Hello & welcome"]]);
  });

  test("components, fragments and keyed children stay jsx calls inside child holes", () => {
    const out = compileSource(
      `export const L = ({ items }) => <ul><Item /><>{1}</>{items.map((i) => <li key={i}>{i}</li>)}</ul>;`,
    );
    const all = templates(out) as unknown[][];
    expect(all.find((t) => t[0] === "ul")).toEqual(["ul", 0, 0, 1, 2]);
    // The keyed item inside the map is a block of its own, with its key.
    expect(all.find((t) => t[0] === "li")).toEqual(["li", 0, 0]);
    expect(out).toMatch(/__hrBlock\(__hrT\d+, i, \[i\]\)/);
    expect(out).toMatch(/jsx_\w+\(Item, \{\}\)/);
  });

  test("elements whose behavior is more than attributes are left alone, their children still compiled", () => {
    for (const src of [
      `export const A = (p) => <div {...p}><b>x</b></div>;`,
      `export const A = ({ r }) => <div ref={r}><b>x</b></div>;`,
      `export const A = ({ v, set }) => <input value={v} onChange={set} />;`,
      `export const A = ({ f }) => <div onMouseEnter={f}><b>x</b></div>;`,
      `export const A = () => <my-element><b>x</b></my-element>;`,
      `export const A = ({ h }) => <div dangerouslySetInnerHTML={{ __html: h }} />;`,
    ]) {
      const out = compileSource(src);
      expect(templates(out)).not.toContainEqual(expect.arrayContaining(["div"]));
      expect(out).not.toMatch(/__hrBlock\(__hrT\d+, null, \[\]\)\s*$/);
    }
    // The bailed div's child is a tree of its own.
    expect(templates(compileSource(`export const A = (p) => <div {...p}><b>x</b></div>;`))).toEqual([["b", 0, "x"]]);
  });

  test("TypeScript, strings, regexes and template literals around JSX scan correctly", () => {
    const out = compileSource(
      `const re = /<b>[)}]/g; const s = "</p>{"; export function A<T,>(p: { n: T }): JSX.Element { return <p title={\`a\${"}"}\`}>{s}{re.source}</p>; }`,
    );
    expect(templates(out)).toEqual([["p", { title: { h: 0 } }, 1, 2]]);
    expect(out).toContain("const re = /<b>[)}]/g;");
  });

  test("a file with no host trees only gets the runtime import", () => {
    const out = compileSource(`export const A = () => <B x={1} />;`);
    expect(templates(out)).toEqual([]);
    expect(out).toMatch(/^import \{ jsx as jsx_\w+ \} from "react\/jsx-runtime";/);
  });
});
