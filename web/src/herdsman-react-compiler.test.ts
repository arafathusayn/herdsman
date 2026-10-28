// Unit tests for herdsman-react-compiler: what it compiles to templates and
// what it leaves to the plain runtime. That compiled code renders what React
// renders is checked in headless Chrome by bench/herdsman-cases/react-compat.ts
// (the herdsman-react+compiler runtimes).

import { describe, expect, test } from "bun:test";

import { compileSource } from "./herdsman-react-compiler.ts";
import { __block, __defaults, __key, __template, Fragment, jsx, jsxs } from "./herdsman-react.ts";

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

  test("components, fragments and keyed children are child holes", () => {
    const out = compileSource(
      `export const L = ({ items }) => <ul><Item /><>{1}</>{items.map((i) => <li key={i}>{i}</li>)}</ul>;`,
    );
    const all = templates(out) as unknown[][];
    expect(all.find((t) => t[0] === "ul")).toEqual(["ul", 0, 0, 1, 2]);
    // The keyed item inside the map is a block of its own, with its key.
    expect(all.find((t) => t[0] === "li")).toEqual(["li", 0, 0]);
    expect(out).toMatch(/__hrBlock\(__hrT\d+, i, \[i\]\)/);
    expect(out).toContain("type: Item, key: null, ref: null");
  });

  test("component elements are built inline, as jsx() builds them", () => {
    const src = `
      function Row({ row }) { return null; }
      function Defaulted({ a, b }) { return null; }
      Defaulted.defaultProps = { a: 1, b: 2 };
      export const make = (rows, extra) => [
        <Row row={rows[0]} />,
        <Defaulted b={3} />,
        rows.map((r) => <Row key={r.id} row={r} label={<Row row={r} />} />),
        <>{rows.length}</>,
        <Row key={extra} row={1} />,
        <Row key="k" row={2} />,
        <Row key={rows[1].id + 0.5} row={3} />,
      ];`;
    const out = compileSource(src, "jsx");
    expect(out).not.toMatch(/jsx_\w+\(Row/);
    const run = (code: string, scope: Record<string, unknown>) => {
      const body = code
        .replace(/^import \{([^}]*)\} from "([^"]*)";$/gm, (_, names: string) =>
          names
            .split(",")
            .map((n) => {
              const [a, b] = n.trim().split(" as ");
              return `const ${b ?? a} = scope.${a};`;
            })
            .join("\n"),
        )
        .replace(/^export /gm, "");
      return new Function("scope", `${body}\nreturn make;`)(scope);
    };
    const scope = { jsx, jsxs, Fragment, __defaults, __key, __block, __template };
    const plain = new Bun.Transpiler({
      loader: "jsx",
      tsconfig: { compilerOptions: { jsx: "react-jsx", jsxImportSource: "react" } },
    } as ConstructorParameters<typeof Bun.Transpiler>[0]).transformSync(src);
    const rows = [{ id: 7 }, { id: 8 }];
    const reference = run(
      `import { jsx, jsxs, Fragment } from "react/jsx-runtime";\n${plain.replace(/\b(jsxs|jsx|Fragment)_[a-z0-9]{8}\b/g, "$1")}`,
      scope,
    )(rows, undefined);
    const compiled = run(out, scope)(rows, undefined);
    // Each evaluation defines its own Row: compare types by name.
    const plainTypes = (x: unknown): unknown =>
      Array.isArray(x)
        ? x.map(plainTypes)
        : typeof x === "function"
          ? `fn ${x.name}`
          : x !== null && typeof x === "object"
            ? Object.fromEntries(Object.entries(x).map(([k, v]) => [k, plainTypes(v)]))
            : x;
    expect(plainTypes(compiled)).toEqual(plainTypes(reference));
    expect(compiled[1].props).toEqual({ a: 1, b: 3 });
    expect(compiled[2][0].key).toBe("7");
    expect(compiled[4].key).toBeNull();
    // Same property order: the engine gives both one hidden class.
    expect(Object.keys(compiled[0])).toEqual(Object.keys(jsx("p", {})));
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

  test("a file with nothing to compile only gets the runtime import", () => {
    const out = compileSource(`export const A = (p) => <B {...p} />;`);
    expect(templates(out)).toEqual([]);
    expect(out).toMatch(/^import \{ jsx as jsx_\w+ \} from "react\/jsx-runtime";\nexport/);
  });
});
