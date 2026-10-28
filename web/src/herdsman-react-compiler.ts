// herdsman-react-compiler: a Bun plugin that compiles JSX trees of host
// elements into herdsman-react templates.
//
// Each .jsx and .tsx file of the app is transpiled by Bun (TypeScript
// stripped, JSX to the automatic runtime's `jsx("tr", { ... }, key)` calls,
// text already cleaned by JSX's whitespace rules and entities decoded). The
// plugin then finds every `jsx`/`jsxs` call whose type is a string and turns
// the largest tree of host elements under it into
//
//   __block(template, key, [hole values])
//
// with the template, a description of the static structure and where each
// hole sits, hoisted to module scope. Holes are attribute, style, event and
// child expressions; components, keyed or bailed elements, fragments and
// arrays stay ordinary jsx calls inside child holes (compiled in turn). The
// runtime (herdsman-react.ts) builds each template once and clones it, and
// writes only the holes whose values changed, with React's semantics.
//
// A component element whose props are an object literal (no spread, key or
// ref) is built inline, as the object the runtime's jsx() would return:
//
//   jsx(Row, { row }, row.id)
//     -> (__hrP = { row }, { $$typeof: __hrE, type: Row, key: row.id === undefined ? null : "" + row.id,
//          ref: null, props: Row?.defaultProps == null ? __hrP : __hrDefaults(Row, __hrP) })
//
// Each call site then sees one type and one props shape, where jsx() sees
// every element of the app; a list's row elements cost an allocation each,
// not a polymorphic call (as Babel's inline-elements transform once did for
// React).
//
// A list in a child hole, `items.map((item, i) => <Row ... />)`, becomes an
// indexed loop in place, with map's results (holes skipped, non-arrays left
// to their own map): the loop runs in the render function, which the engine
// optimizes as the loop runs, where a callback stays unoptimized until it
// has been called thousands of times.
//
// A host element is left to the plain runtime when its behavior depends on more
// than its attributes: spread props, refs, form fields and forms, document
// metadata, custom elements, dangerouslySetInnerHTML, autoFocus, and events
// that do not bubble or that React derives (enter and leave). Its children
// are still compiled where they qualify.
//
//   Bun.build({ plugins: [herdsmanReactCompiler()], ... })
//
// The output imports `react` and `react/jsx-runtime`, which the app aliases
// to herdsman-react; with React itself the compiled code would not run.

import type { BunPlugin } from "bun";

/** Options for the plugin. */
export interface CompilerOptions {
  /** Files to compile; default .jsx and .tsx outside node_modules. */
  readonly filter?: RegExp;
  /** Skip a file (by path) even if the filter matches. */
  readonly exclude?: (path: string) => boolean;
  /** Build component elements inline (default true). */
  readonly inlineElements?: boolean;
}

/** The Bun plugin: compiles the app's JSX files. */
export function herdsmanReactCompiler(options: CompilerOptions = {}): BunPlugin {
  const filter = options.filter ?? /\.[jt]sx$/;
  return {
    name: "herdsman-react-compiler",
    setup(build) {
      build.onLoad({ filter }, async ({ path }) => {
        if (path.includes("/node_modules/") || options.exclude?.(path)) return undefined;
        const source = await Bun.file(path).text();
        return { contents: compileSource(source, path.endsWith(".tsx") ? "tsx" : "jsx", options), loader: "js" };
      });
    },
  };
}

/** Transpile one JSX or TSX source with Bun and compile its host element trees. */
export function compileSource(source: string, loader: "jsx" | "tsx" = "tsx", options: CompilerOptions = {}): string {
  const t = new Bun.Transpiler({
    loader,
    tsconfig: { compilerOptions: { jsx: "react-jsx", jsxImportSource: "react" } },
  } as ConstructorParameters<typeof Bun.Transpiler>[0]);
  return compileJs(t.transformSync(source), options);
}

// ---------------------------------------------------------------------------
// Scanning JavaScript
// ---------------------------------------------------------------------------

const OPEN = "([{";
const CLOSE = ")]}";
// After these characters, or these words, a slash starts a regular expression.
const REGEX_AFTER = new Set("(,=:[!&|?{};+-*%<>~^".split(""));
const REGEX_WORDS = new Set([
  "return",
  "typeof",
  "case",
  "do",
  "else",
  "in",
  "of",
  "new",
  "delete",
  "void",
  "throw",
  "instanceof",
  "yield",
  "await",
]);
const isIdent = (c: string | undefined): boolean => c !== undefined && /[\w$]/.test(c);

// The index just past the literal, comment or template that starts at i, or i itself.
function skipToken(src: string, i: number, prev: string, word: string): number {
  const c = src[i];
  if (c === '"' || c === "'") {
    for (let j = i + 1; j < src.length; j++) {
      if (src[j] === "\\") j++;
      else if (src[j] === c) return j + 1;
    }
    return src.length;
  }
  if (c === "`") {
    for (let j = i + 1; j < src.length; j++) {
      if (src[j] === "\\") j++;
      else if (src[j] === "`") return j + 1;
      else if (src[j] === "$" && src[j + 1] === "{") j = until(src, j + 2, "}");
    }
    return src.length;
  }
  if (c === "/") {
    if (src[i + 1] === "/") {
      const e = src.indexOf("\n", i);
      return e < 0 ? src.length : e;
    }
    if (src[i + 1] === "*") {
      const e = src.indexOf("*/", i + 2);
      return e < 0 ? src.length : e + 2;
    }
    if (prev === "" || REGEX_AFTER.has(prev) || REGEX_WORDS.has(word)) {
      let inClass = false;
      for (let j = i + 1; j < src.length; j++) {
        const d = src[j];
        if (d === "\\") j++;
        else if (d === "[") inClass = true;
        else if (d === "]") inClass = false;
        else if (d === "/" && !inClass) {
          j++;
          while (isIdent(src[j])) j++;
          return j;
        }
      }
    }
  }
  return i;
}

// Walks src from i at bracket depth 0 and returns the index of the first of
// `stops` found there (or of the bracket that closes the enclosing one).
function until(src: string, i: number, stops: string): number {
  let depth = 0,
    prev = "",
    word = "";
  while (i < src.length) {
    const c = src[i]!;
    const j = skipToken(src, i, prev, word);
    if (j !== i) {
      prev = "a"; // a literal ends like an operand
      word = "";
      i = j;
      continue;
    }
    if (depth === 0 && stops.includes(c)) return i;
    if (OPEN.includes(c)) depth++;
    else if (CLOSE.includes(c)) {
      if (depth === 0) return i;
      depth--;
    }
    if (isIdent(c)) {
      let e = i;
      while (isIdent(src[e])) e++;
      word = src.slice(i, e);
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
  return i;
}

// The top-level comma-separated parts of src[start, end).
function split(src: string, start: number, end: number): string[] {
  const out: string[] = [];
  let i = start;
  while (i < end) {
    const j = Math.min(until(src, i, ","), end);
    const part = src.slice(i, j).trim();
    if (part) out.push(part);
    i = j + 1;
  }
  return out;
}

// ---------------------------------------------------------------------------
// Compiling jsx calls
// ---------------------------------------------------------------------------

// Elements left to the plain runtime: form state, document metadata, scripts.
const BAIL_TAGS = new Set([
  "input",
  "textarea",
  "select",
  "option",
  "form",
  "title",
  "meta",
  "link",
  "script",
  "style",
  "video",
  "audio",
]);
const BAIL_PROPS = new Set([
  "ref",
  "dangerouslySetInnerHTML",
  "suppressHydrationWarning",
  "suppressContentEditableWarning",
  "autoFocus",
  "is",
  "defaultValue",
  "defaultChecked",
  "action",
  "formAction",
]);
// Events with no delegated listener in the runtime: they would need one on the element.
const BAIL_EVENTS = new Set(
  (
    "onMouseEnter onMouseLeave onPointerEnter onPointerLeave onScroll onScrollEnd onLoad onError onAbort onToggle " +
    "onBeforeToggle onInvalid onCancel onClose onCanPlay onCanPlayThrough onDurationChange onEmptied onEncrypted " +
    "onEnded onLoadedData onLoadedMetadata onLoadStart onPause onPlay onPlaying onProgress onRateChange onResize " +
    "onSeeked onSeeking onStalled onSuspend onTimeUpdate onVolumeChange onWaiting"
  ).split(" "),
);

// [tag, props (a hole is { h: index }), ...children (text, a hole index, an element)]
type Desc = [string, Record<string, unknown> | 0, ...(string | number | Desc)[]];

interface Call {
  helper: string;
  multi: boolean; // jsxs: children is a static array
  args: string[];
}

class Compiler {
  readonly src: string;
  readonly helpers: Map<string, boolean>; // local name -> is jsxs
  readonly templates: string[] = [];
  /** Hoisted list loops. */
  readonly loops: string[] = [];
  inlined = 0;
  readonly inlineElements: boolean;
  constructor(src: string, helpers: Map<string, boolean>, inlineElements: boolean) {
    this.inlineElements = inlineElements;
    this.src = src;
    this.helpers = helpers;
  }

  // Rewrite src[start, end): every jsx call in it, compiled where it qualifies.
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
          const call: Call = { helper: name, multi: this.helpers.get(name)!, args: split(src, e + 1, close) };
          out += this.call(call);
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

  // A jsx call: a block if it is a host element that compiles, else the call
  // with its arguments rewritten.
  call(c: Call): string {
    const values: string[] = [];
    const desc = this.element(c, true, values);
    if (desc === null)
      return (
        (this.inlineElements ? this.inline(c) : null) ?? `${c.helper}(${c.args.map((a) => this.rewrite(a)).join(", ")})`
      );
    const id = `__hrT${this.templates.length}`;
    this.templates.push(`const ${id} = __hrTemplate(${JSON.stringify(desc)});`);
    const key = c.args[2] !== undefined && c.args[2] !== "undefined" ? c.args[2] : "null";
    return `__hrBlock(${id}, ${key}, [${values.join(", ")}])`;
  }

  // The template of a host element, adding its holes' expressions to
  // values, or null when it must stay a jsx call.
  element(c: Call, root: boolean, values: string[]): Desc | null {
    const [type, props, key] = c.args;
    if (type === undefined || type[0] !== '"') return null;
    const tag = JSON.parse(type) as string;
    if (tag.includes("-") || BAIL_TAGS.has(tag)) return null;
    if (!root && key !== undefined && key !== "undefined") return null;
    const entries = this.object(props ?? "{}");
    if (entries === null) return null;
    const ps: Record<string, unknown> = {};
    let children: string[] = [];
    const mark = values.length;
    for (const [name, value] of entries) {
      if (name === "children") {
        children =
          c.multi && value.startsWith("[") && value.endsWith("]") ? split(value, 1, value.length - 1) : [value];
        continue;
      }
      if (BAIL_PROPS.has(name) || name === "key") return this.undo(values, mark);
      if (/^on[A-Z]/.test(name) && BAIL_EVENTS.has(name.endsWith("Capture") ? name.slice(0, -7) : name))
        return this.undo(values, mark);
      const lit = literal(value);
      if (lit !== NOT_LITERAL) {
        if (lit !== undefined) ps[name] = lit;
      } else {
        ps[name] = { h: values.length };
        values.push(this.rewrite(value));
      }
    }
    const desc: Desc = [tag, Object.keys(ps).length ? ps : 0];
    for (const item of children) {
      const lit = literal(item);
      if (typeof lit === "string" || typeof lit === "number") {
        desc.push("" + lit);
        continue;
      }
      if (lit === null || typeof lit === "boolean" || (lit === undefined && item === "undefined")) continue;
      const inner = this.parseCall(item);
      if (inner !== null) {
        const m = values.length;
        const d = this.element(inner, false, values);
        if (d !== null) {
          desc.push(d);
          continue;
        }
        values.length = m;
      }
      desc.push(values.length);
      values.push(this.loop(item) ?? this.rewrite(item));
    }
    return desc;
  }

  // A component element as an inline object literal, or null when it must
  // stay a jsx call: a computed type, props that are not a plain object
  // literal, or a key or ref in them.
  inline(c: Call): string | null {
    const [type, props, key, ...rest] = c.args;
    if (type === undefined || props === undefined || rest.length) return null;
    if (!PATH.test(type) || type === "undefined" || type === "null") return null;
    const entries = this.object(props);
    if (entries === null || entries.some(([k]) => k === "key" || k === "ref" || k === "__self" || k === "__source"))
      return null;
    let k: string;
    if (key === undefined || key === "undefined") k = "null";
    else {
      const lit = literal(key);
      // A name is read twice; any other expression goes through __hrKey once.
      k =
        typeof lit === "string" || typeof lit === "number"
          ? JSON.stringify("" + lit)
          : PATH.test(key)
            ? `${key} === undefined ? null : "" + ${key}`
            : `__hrKey(${this.rewrite(key)})`;
    }
    this.inlined++;
    return `(__hrP = ${this.rewrite(props)}, { $$typeof: __hrE, type: ${type}, key: ${k}, ref: null, props: ${type}?.defaultProps == null ? __hrP : __hrDefaults(${type}, __hrP) })`;
  }

  // `xs.map(arrow)` with an expression-bodied arrow of plain parameters as a
  // call of a loop hoisted to module scope, or null. The loop takes the
  // body's free names as arguments: a module function keeps its optimized
  // code across garbage collections, where a closure made per render loses
  // it at each one. Bodies that could notice the move are left alone:
  // functions, this, arguments, await, yield, assignments.
  loop(src: string): string | null {
    const m = /\.map\(/g;
    let at = -1;
    for (let x; (x = m.exec(src)) !== null;) {
      const close = until(src, x.index + 5, "");
      if (close === src.length - 1 && until(src.slice(0, x.index), 0, ",;") === x.index) at = x.index;
    }
    if (at <= 0) return null;
    const recv = src.slice(0, at).trim();
    const arrow = src.slice(at + 5, -1).trim();
    const am = /^(?:\(([^()]*)\)|([A-Za-z_$][\w$]*))\s*=>\s*/.exec(arrow);
    if (am === null) return null;
    const body = arrow.slice(am[0].length).trim();
    if (body.startsWith("{") || until(arrow, am[0].length, ",") !== arrow.length) return null;
    const params = am[1] !== undefined ? split(am[1], 0, am[1].length) : [am[2]!];
    if (params.length > 3 || params.some((q) => !/^[A-Za-z_$][\w$]*$/.test(q))) return null;
    const b = this.rewrite(body);
    const free = freeNames(b);
    // Compiled once: left as a map, with the body already compiled.
    if (free === null) return `${this.rewrite(recv)}.map(${arrow.slice(0, am[0].length)}${b})`;
    const args = [...free].filter((x) => !params.includes(x));
    const [p0, p1, p2] = params;
    const decl =
      (p0 !== undefined ? `const ${p0} = __hrV; ` : "") +
      (p1 !== undefined ? `const ${p1} = __hrI; ` : "") +
      (p2 !== undefined ? `const ${p2} = __hrXs; ` : "");
    const id = `__hrL${this.loops.length}`;
    this.loops.push(
      `function ${id}(__hrXs${args.map((x) => ", " + x).join("")}) { ` +
        `const __hrN = __hrXs.length, __hrOut = new Array(__hrN); ` +
        `for (let __hrI = 0; __hrI < __hrN; __hrI++) { const __hrV = __hrXs[__hrI]; ` +
        `if (__hrV === undefined && !(__hrI in __hrXs)) continue; ${decl}__hrOut[__hrI] = ${b}; } ` +
        `return __hrOut; }`,
    );
    return (
      `(Array.isArray(__hrM = ${this.rewrite(recv)}) ? ${id}(__hrM${args.map((x) => ", " + x).join("")}) : ` +
      `__hrM.map((${params.join(", ")}) => ${b}))`
    );
  }

  undo(values: string[], mark: number): null {
    values.length = mark;
    return null;
  }

  // `helper(args)` as a whole expression, or null.
  parseCall(src: string): Call | null {
    const m = /^([\w$]+)\(/.exec(src);
    if (m === null || !this.helpers.has(m[1]!)) return null;
    const close = until(src, m[0].length, "");
    if (close !== src.length - 1) return null;
    return { helper: m[1]!, multi: this.helpers.get(m[1]!)!, args: split(src, m[0].length, close) };
  }

  // The entries of an object literal with plain keys, or null (spreads, computed keys, methods).
  object(src: string): [string, string][] | null {
    const s = src.trim();
    if (!s.startsWith("{") || !s.endsWith("}")) return null;
    const out: [string, string][] = [];
    for (const part of split(s, 1, s.length - 1)) {
      if (part.startsWith("...")) return null;
      const colon = until(part, 0, ":");
      if (colon >= part.length) {
        if (!/^[\w$]+$/.test(part)) return null;
        out.push([part, part]); // shorthand
        continue;
      }
      let k = part.slice(0, colon).trim();
      if (k.startsWith('"')) k = JSON.parse(k) as string;
      else if (!/^[\w$]+$/.test(k)) return null;
      out.push([k, part.slice(colon + 1).trim()]);
    }
    return out;
  }
}

// Words that are not names of variables.
const NOT_NAMES = new Set(
  (
    "true false null undefined typeof void new delete in instanceof of if else return " +
    "NaN Infinity Symbol Object Array String Number Boolean Math JSON Date"
  ).split(" "),
);
const BAIL_WORDS = new Set([
  "this",
  "arguments",
  "await",
  "yield",
  "function",
  "class",
  "super",
  "import",
  "var",
  "let",
  "const",
]);

// The free variable names an expression reads, in first-use order, or null
// when moving it out of its function could change what it does: it defines
// functions, assigns, or uses this, arguments, await or yield. Names the
// compiler made (__hr...) are module-level and left out.
function freeNames(src: string): Set<string> | null {
  if (src.includes("=>")) return null;
  const out = new Set<string>();
  const stack: string[] = [];
  let prev = "",
    word = "";
  for (let i = 0; i < src.length;) {
    const c = src[i]!;
    const j = skipToken(src, i, prev, word);
    if (j !== i) {
      prev = "a";
      word = "";
      i = j;
      continue;
    }
    if (isIdent(c) && !/[0-9]/.test(c)) {
      let e = i;
      while (isIdent(src[e])) e++;
      const name = src.slice(i, e);
      if (BAIL_WORDS.has(name)) return null;
      let k = e;
      while (/\s/.test(src[k] ?? "")) k++;
      const member = prev === ".";
      // An object literal's key: after { or , inside braces, before a colon.
      const key = src[k] === ":" && (prev === "{" || prev === ",") && stack.at(-1) === "{";
      if (!member && !key && !NOT_NAMES.has(name) && !name.startsWith("__hr")) out.add(name);
      prev = "a";
      word = name;
      i = e;
      continue;
    }
    if (isIdent(c)) {
      // A number.
      while (isIdent(src[i]) || src[i] === ".") i++;
      prev = "a";
      continue;
    }
    if (c === "=" && src[i + 1] !== "=" && prev !== "=" && prev !== "!" && prev !== "<" && prev !== ">") {
      // An assignment, unless the compiler's own temporary takes it.
      if (!/__hr[A-Za-z]\s*$/.test(src.slice(0, i))) return null;
    }
    if ((c === "+" || c === "-") && src[i + 1] === c) return null;
    if (OPEN.includes(c)) stack.push(c);
    else if (CLOSE.includes(c)) stack.pop();
    if (!/\s/.test(c)) {
      prev = c;
      word = "";
    }
    i++;
  }
  return out;
}

const NOT_LITERAL = Symbol("not a literal");
// A name or a dotted path of names (no calls, no computed members).
const PATH = /^[A-Za-z_$][\w$]*(\.[A-Za-z_$][\w$]*)*$/;

// The value of a string, number, boolean, null or undefined literal.
function literal(src: string): unknown {
  if (src === "true") return true;
  if (src === "false") return false;
  if (src === "null") return null;
  if (src === "undefined" || src === "void 0") return undefined;
  if (/^-?\d+(\.\d+)?$/.test(src)) return Number(src);
  if (src.startsWith('"') && src.endsWith('"') && until(src, 0, "") === src.length) {
    try {
      return JSON.parse(src);
    } catch {
      return NOT_LITERAL;
    }
  }
  return NOT_LITERAL;
}

/** Compile Bun's automatic-runtime output: host element trees become templates. */
export function compileJs(js: string, options: CompilerOptions = {}): string {
  // Bun's transpiler leaves the runtime's helpers as unbound names with a
  // hash suffix (jsx_w77yafs4, jsxs_..., Fragment_...), bound only when it
  // bundles JSX itself. This output is plain JavaScript, so import them.
  const helpers = new Map<string, boolean>(); // local name -> is jsxs
  const imports = new Map<string, string>(); // local name -> export
  for (const m of js.matchAll(/\b(jsxs|jsx|jsxDEV|Fragment)_[a-z0-9]{8}\b/g)) {
    imports.set(m[0], m[1]!);
    if (m[1] === "jsx" || m[1] === "jsxs") helpers.set(m[0], m[1] === "jsxs");
  }
  if (imports.size === 0) return js;
  const c = new Compiler(js, helpers, options.inlineElements ?? true);
  const body = c.rewrite(js);
  const runtime = `import { ${[...imports].map(([local, name]) => `${name} as ${local}`).join(", ")} } from "react/jsx-runtime";\n`;
  if (c.templates.length === 0 && c.inlined === 0 && c.loops.length === 0) return runtime + body;
  const names = [
    ...(c.templates.length ? ["__template as __hrTemplate", "__block as __hrBlock"] : []),
    ...(c.inlined ? ["__defaults as __hrDefaults", "__key as __hrKey"] : []),
  ];
  const inline =
    (c.inlined ? `let __hrP;\nconst __hrE = Symbol.for("react.transitional.element");\n` : "") +
    (c.loops.length ? `let __hrM;\n${c.loops.join("\n")}\n` : "");
  const fromReact = names.length ? `import { ${names.join(", ")} } from "react";\n` : "";
  return `${runtime}${fromReact}${inline}${c.templates.join("\n")}${c.templates.length ? "\n" : ""}${body}`;
}
