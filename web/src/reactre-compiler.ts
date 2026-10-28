// reactre-compiler: a Bun plugin that compiles reactre's TSX into templates
// and the code that keeps them up to date (see reactre.ts for the model).
//
// Bun transpiles each file (TypeScript stripped, JSX to the automatic
// runtime's `jsx(type, props, key)` calls). Every jsx call is then compiled:
//
//   - A host element tree becomes a herdsman-blocks template, hoisted to
//     module scope, and an expression that clones it with the values of its
//     holes. A hole whose expression calls a function is bound (an effect
//     that rewrites it); an `on*` handler is added as a listener; a child
//     that is a component, a `<For>` or an expression holding JSX is a slot.
//   - `<For each={rows} key={(r) => r.id}>{(row) => <tr .../>}</For>` becomes
//     a keyed list (`__for`) whose row template's holes are split by what
//     they read: the row only (written at creation, compared on update), or
//     a function call (re-run when what it read changes). Row handlers are
//     delegated to the list's parent.
//   - A component element `<Button id="run" />` is the call `Button({ id: "run" })`,
//     made once (components run once in reactre).
//
// Nothing is left to a JSX runtime: a construct the compiler does not handle
// (fragments, spread props, a keyed host element outside a list) is a
// compile error, with its source.
//
//   Bun.build({ plugins: [reactreCompiler()], ... })

import type { BunPlugin } from "bun";

import { isIdent, literal, NOT_LITERAL, skipToken, split, until } from "./herdsman-react-compiler.ts";

/** Options for the plugin. */
export interface ReactreCompilerOptions {
  /** Files to compile; default .jsx and .tsx outside node_modules. */
  readonly filter?: RegExp;
}

/** The Bun plugin: compiles the app's TSX files. */
export function reactreCompiler(options: ReactreCompilerOptions = {}): BunPlugin {
  const filter = options.filter ?? /\.[jt]sx$/;
  return {
    name: "reactre-compiler",
    setup(build) {
      build.onLoad({ filter }, async ({ path }) => {
        if (path.includes("/node_modules/")) return undefined;
        const source = await Bun.file(path).text();
        return { contents: compileSource(source, path.endsWith(".tsx") ? "tsx" : "jsx"), loader: "js" };
      });
    },
  };
}

/** Transpile one TSX (or JSX) source with Bun and compile its JSX. */
export function compileSource(source: string, loader: "jsx" | "tsx" = "tsx"): string {
  const t = new Bun.Transpiler({
    loader,
    tsconfig: { compilerOptions: { jsx: "react-jsx", jsxImportSource: "reactre" } },
  } as ConstructorParameters<typeof Bun.Transpiler>[0]);
  return compileJs(t.transformSync(source));
}

// Hole markers, as herdsman-blocks' text(), cls(), attr() and slot() make them; 0 is untyped (an event).
const M_TEXT = -1,
  M_CLASS = -2,
  M_ATTR = -4,
  M_SLOT = -5,
  M_EVENT = 0;

const VOID = new Set("area base br col embed hr img input link meta source track wbr".split(" "));
const ALIASES: Record<string, string> = { className: "class", htmlFor: "for" };

interface Call {
  readonly type: string;
  readonly props: string;
  readonly key: string | undefined;
}

type Hole =
  | { readonly kind: "value"; readonly marker: number; readonly expr: string }
  | { readonly kind: "event"; readonly type: string; readonly expr: string }
  | {
      readonly kind: "slot";
      readonly slot: "for" | "comp" | "expr";
      readonly expr: string;
      readonly only: boolean;
      /** A <For>'s key function (JSX passes `key` apart from the props). */
      readonly key?: string;
    };

interface Tree {
  readonly strings: string[];
  readonly holes: Hole[];
}

/** A compile error, pointing at the source it could not compile. */
export class ReactreCompileError extends Error {}

/** Compile Bun's automatic-runtime output. */
export function compileJs(js: string): string {
  const helpers = new Set<string>();
  for (const m of js.matchAll(/\b(jsxs|jsx|jsxDEV)_[a-z0-9]{8}\b/g)) helpers.add(m[0]);
  if (helpers.size === 0) return js;
  const c = new Compiler(helpers);
  const body = c.rewrite(js);
  for (const m of body.matchAll(/\b(jsxs|jsx|jsxDEV|Fragment)_[a-z0-9]{8}\b/g)) {
    throw new ReactreCompileError(`reactre: ${m[1]} left uncompiled (fragments are not supported)`);
  }
  const imports = `import { __template as __rrT, __for as __rrFor, __insert as __rrInsert, __bind as __rrBind, __slot as __rrSlot, untrack as __rrUntrack } from "reactre";\n`;
  return imports + c.templates.join("\n") + "\n" + body;
}

class Compiler {
  readonly helpers: Set<string>;
  readonly templates: string[] = [];
  constructor(helpers: Set<string>) {
    this.helpers = helpers;
  }

  // Rewrite every jsx call in src.
  rewrite(src: string): string {
    let out = "",
      i = 0,
      prev = "",
      word = "";
    while (i < src.length) {
      const c = src[i]!;
      const j = skipToken(src, i, prev, word);
      if (j !== i) {
        out += src.slice(i, j);
        prev = "a";
        word = "";
        i = j;
        continue;
      }
      if (isIdent(c) && !isIdent(src[i - 1])) {
        let e = i;
        while (isIdent(src[e])) e++;
        const name = src.slice(i, e);
        if (this.helpers.has(name) && src[e] === "(" && src[i - 1] !== ".") {
          const close = until(src, e + 1, "");
          out += this.call(this.parse(src.slice(e + 1, close), src.slice(i, close + 1)));
          i = close + 1;
          prev = "a";
          word = "";
          continue;
        }
        out += name;
        word = name;
        prev = "a";
        i = e;
        continue;
      }
      if (!/\s/.test(c)) {
        prev = c;
        word = "";
      }
      out += c;
      i++;
    }
    return out;
  }

  parse(args: string, whole: string): Call {
    const parts = split(args, 0, args.length);
    const [type, props, key] = parts;
    if (type === undefined || props === undefined) throw new ReactreCompileError(`reactre: cannot compile ${whole}`);
    return { type, props, key: key === undefined || key === "undefined" ? undefined : key };
  }

  // A jsx call that is not a child of a host element compiled with it.
  call(c: Call): string {
    if (c.type.startsWith('"')) return this.host(c);
    if (c.type === "For") throw new ReactreCompileError("reactre: <For> must be the child of an element");
    return this.component(c);
  }

  // `<C a={x} />`: C called once with its props, outside any tracking.
  component(c: Call): string {
    return `__rrUntrack(() => ${c.type}(${this.rewrite(c.props)}))`;
  }

  // The entries of an object literal with plain keys.
  object(src: string): [string, string][] {
    const s = src.trim();
    if (!s.startsWith("{") || !s.endsWith("}"))
      throw new ReactreCompileError(`reactre: props must be an object literal: ${s}`);
    const out: [string, string][] = [];
    for (const part of split(s, 1, s.length - 1)) {
      if (part.startsWith("...")) throw new ReactreCompileError(`reactre: spread props are not supported: ${part}`);
      const colon = until(part, 0, ":");
      if (colon >= part.length) {
        out.push([part, part]);
        continue;
      }
      let k = part.slice(0, colon).trim();
      if (k.startsWith('"')) k = JSON.parse(k) as string;
      out.push([k, part.slice(colon + 1).trim()]);
    }
    return out;
  }

  // The call in src, when src is exactly one jsx call.
  only(src: string): Call | null {
    const m = /^([\w$]+)\(/.exec(src);
    if (m === null || !this.helpers.has(m[1]!)) return null;
    const close = until(src, m[0].length, "");
    if (close !== src.length - 1) return null;
    return this.parse(src.slice(m[0].length, close), src);
  }

  // The template of host element c and its holes.
  tree(c: Call): Tree {
    const t: Tree = { strings: [""], holes: [] };
    this.element(c, t);
    return t;
  }

  private add(t: Tree, text: string): void {
    t.strings[t.strings.length - 1] += text;
  }
  private hole(t: Tree, h: Hole): void {
    t.holes.push(h);
    t.strings.push("");
  }

  private element(c: Call, t: Tree): void {
    const tag = JSON.parse(c.type) as string;
    if (c.key !== undefined) throw new ReactreCompileError(`reactre: a key on <${tag}> means nothing outside <For>`);
    this.add(t, "<" + tag);
    let children: string[] = [];
    for (const [rawName, value] of this.object(c.props)) {
      if (rawName === "children") {
        children = value.startsWith("[") && value.endsWith("]") ? split(value, 1, value.length - 1) : [value];
        continue;
      }
      const name = ALIASES[rawName] ?? rawName;
      if (/^on[A-Z]/.test(name)) {
        this.add(t, ` on${name.slice(2).toLowerCase()}=`);
        this.hole(t, { kind: "event", type: name.slice(2).toLowerCase(), expr: value });
        continue;
      }
      const lit = literal(value);
      if (lit !== NOT_LITERAL) {
        if (lit === null || lit === undefined || lit === false) continue;
        this.add(t, lit === true ? ` ${name}` : ` ${name}="${attrText("" + lit)}"`);
        continue;
      }
      this.add(t, ` ${name}=`);
      this.hole(t, { kind: "value", marker: name === "class" ? M_CLASS : M_ATTR, expr: value });
    }
    this.add(t, ">");
    if (VOID.has(tag)) {
      if (children.length) throw new ReactreCompileError(`reactre: <${tag}> takes no children`);
      return;
    }
    const kept = children.filter((x) => {
      const lit = literal(x);
      return !(lit === null || lit === undefined || typeof lit === "boolean");
    });
    for (const child of kept) {
      const lit = literal(child);
      if (typeof lit === "string" || typeof lit === "number") {
        this.add(t, text("" + lit));
        continue;
      }
      const inner = this.only(child);
      if (inner !== null && inner.type.startsWith('"')) {
        this.element(inner, t);
        continue;
      }
      const only = kept.length === 1;
      if (inner !== null && inner.type === "For")
        this.hole(t, {
          kind: "slot",
          slot: "for",
          expr: inner.props,
          only,
          ...(inner.key !== undefined ? { key: inner.key } : {}),
        });
      else if (inner !== null) this.hole(t, { kind: "slot", slot: "comp", expr: this.component(inner), only });
      else if (this.hasJsx(child)) this.hole(t, { kind: "slot", slot: "expr", expr: this.rewrite(child), only });
      else this.hole(t, { kind: "value", marker: M_TEXT, expr: child });
    }
    this.add(t, `</${tag}>`);
  }

  private hasJsx(src: string): boolean {
    for (const h of this.helpers) if (src.includes(h + "(")) return true;
    return false;
  }

  private template(t: Tree): string {
    const id = `__rrT${this.templates.length}`;
    const markers = t.holes.map((h) => (h.kind === "value" ? h.marker : h.kind === "event" ? M_EVENT : M_SLOT));
    this.templates.push(`const ${id} = __rrT(${JSON.stringify(t.strings)}, ${JSON.stringify(markers)});`);
    return id;
  }

  // A host element tree outside a list: clone, then bind what changes.
  host(c: Call): string {
    const t = this.tree(c);
    const id = this.template(t);
    const vals: string[] = [];
    const after: string[] = [];
    t.holes.forEach((h, i) => {
      if (h.kind === "value") {
        if (reactive(h.expr)) {
          vals.push('""');
          after.push(`__rrBind(${id}, ${i}, __n[${i}], () => (${this.rewrite(h.expr)}));`);
        } else vals.push(`(${this.rewrite(h.expr)})`);
        return;
      }
      vals.push('""');
      if (h.kind === "event") {
        after.push(`__n[${i}].addEventListener(${JSON.stringify(h.type)}, ${this.rewrite(h.expr)});`);
        return;
      }
      if (h.slot === "comp") after.push(`__rrInsert(__n[${i}], ${h.expr}, null);`);
      else if (h.slot === "expr") after.push(`__rrSlot(__n[${i}], () => (${h.expr}));`);
      else after.push(this.forList(h.expr, h.key, `__n[${i}]`, h.only));
    });
    const locate = after.length ? `const __n = []; ${id}.locate(__r, __n); ` : "";
    return `(() => { const __r = ${id}.clone([${vals.join(", ")}]); ${locate}${after.join(" ")} return __r; })()`;
  }

  // `<For each key>{(row) => <tr/>}</For>` in the slot at `anchor`.
  forList(props: string, keyArg: string | undefined, anchor: string, only: boolean): string {
    const p = new Map(this.object(props));
    const each = p.get("each"),
      key = keyArg ?? p.get("key"),
      children = p.get("children");
    if (each === undefined || key === undefined || children === undefined) {
      throw new ReactreCompileError("reactre: <For> needs each, key and a row function as its child");
    }
    const fn = /^\(?\s*([A-Za-z_$][\w$]*)\s*\)?\s*=>\s*([\s\S]*)$/.exec(children.trim());
    if (fn === null) throw new ReactreCompileError(`reactre: a <For> row must be (row) => <element />: ${children}`);
    const param = fn[1]!;
    let body = fn[2]!.trim();
    while (body.startsWith("(") && until(body, 1, "") === body.length - 1) body = body.slice(1, -1).trim();
    const row = this.only(body);
    if (row === null || !row.type.startsWith('"'))
      throw new ReactreCompileError(`reactre: a <For> row must be one element: ${body}`);
    const t = this.tree(row);
    const id = this.template(t);
    const fill: string[] = [],
      holes: number[] = [],
      dyn: string[] = [],
      dynHoles: number[] = [],
      events: string[] = [];
    t.holes.forEach((h, i) => {
      if (h.kind === "event") {
        events.push(`[${JSON.stringify(h.type)}, ${i}, (${param}) => (${this.rewrite(h.expr)})]`);
        return;
      }
      if (h.kind === "slot")
        throw new ReactreCompileError("reactre: a <For> row cannot hold components, lists or JSX expressions yet");
      const e = this.rewrite(h.expr);
      if (reactive(h.expr)) {
        dyn.push(`__d[${dynHoles.length}] = (${e});`);
        dynHoles.push(i);
      } else {
        fill.push(`__v[${i}] = (${e});`);
        holes.push(i);
      }
    });
    const spec =
      `{ each: () => (${this.rewrite(each)}), key: ${this.rewrite(key)}, t: ${id}, ` +
      `fill: (${param}, __v) => { ${fill.join(" ")} }, holes: ${JSON.stringify(holes)}` +
      (dyn.length ? `, dyn: (${param}, __d) => { ${dyn.join(" ")} }, dynHoles: ${JSON.stringify(dynHoles)}` : "") +
      (events.length ? `, events: [${events.join(", ")}]` : "") +
      " }";
    return only
      ? `{ const __a = ${anchor}, __p = __a.parentNode; __p.removeChild(__a); __rrFor(__p, null, ${spec}); }`
      : `__rrFor(${anchor}.parentNode, ${anchor}, ${spec});`;
  }
}

// Whether an expression may change: it calls something (a signal read).
function reactive(expr: string): boolean {
  let prev = "",
    word = "";
  for (let i = 0; i < expr.length;) {
    const j = skipToken(expr, i, prev, word);
    if (j !== i) {
      i = j;
      prev = "a";
      continue;
    }
    const c = expr[i]!;
    if (c === "(" && (isIdent(prev) || prev === ")" || prev === "]" || prev === "a")) return true;
    if (isIdent(c)) {
      let e = i;
      while (isIdent(expr[e])) e++;
      word = expr.slice(i, e);
      prev = "a";
      i = e;
      continue;
    }
    if (!/\s/.test(c)) {
      prev = c;
      word = "";
    }
    i++;
  }
  return false;
}

const text = (s: string): string => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const attrText = (s: string): string => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;");
