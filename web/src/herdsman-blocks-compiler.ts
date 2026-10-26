// herdsman-blocks-compiler: compiles herdsman-blocks views at build time.
//
// `transform(source)` scans a module once, finds the html`` and svg``
// literals whose tag is imported from herdsman-blocks-view.ts (following the
// import bindings, aliases and namespace imports included), and replaces
// each one it can prove safe with a reference to generated code: a
// prototype parsed once from static markup, firstChild/nextSibling walks to
// the hole nodes, one writer per hole specialized by kind, and a description
// class that keeps the values in fields. Calls `View([a, b])` of a view bound
// by `const View = html\`...\`` in the same module become `new D(a, b)`, so
// no values array is built. Every import of herdsman-blocks-view.ts is pointed
// at herdsman-blocks-compiled.ts, which runs the generated code.
//
// Everything else stays byte for byte, and every rewritten span keeps its
// line count, so line numbers in stack traces and coverage stay right
// (Bun's plugin API takes no source maps). Generated definitions go at the
// start of line 1. A marker in the wrong position, a hole inside a tag that
// is not an attribute value, a hole that cannot be located and a template
// without exactly one root element are build errors with file:line; the same
// checks run on herdsman-blocks' own html``/svg`` literals (which stay as
// they are). A literal whose markup the HTML model cannot be sure of (implied
// end tags, table foster parenting, character references that may be
// whitespace, ...) is left to the run-time compiler, with a warning.
//
// No dependencies and no Babel: one hand-written scanner over the source.

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

export interface Diagnostic {
  readonly line: number;
  readonly column: number;
  readonly message: string;
}

export interface TransformOptions {
  /** Module specifier generated code imports the compiled runtime from. */
  readonly runtime: string;
  /** "error" (the default): template mistakes fail. "runtime": leave such literals to the run-time compiler. */
  readonly mistakes?: "error" | "runtime";
}

export interface TransformResult {
  /** The new source, or the input unchanged. */
  readonly code: string;
  readonly changed: boolean;
  readonly errors: readonly Diagnostic[];
  readonly warnings: readonly Diagnostic[];
  /** View literals compiled. */
  readonly views: number;
  /** View calls turned into direct constructions. */
  readonly calls: number;
}

const LIB = /(?:^|\/)herdsman-blocks(-view)?(?:\.ts|\.js)?$/;

/** Compile the views of one module. Fast path: a module that never names herdsman-blocks is returned as is. */
export function transform(source: string, opt: TransformOptions): TransformResult {
  if (source.indexOf("herdsman-blocks") < 0) return unchanged(source, [], []);
  let lx: Lexed;
  try {
    lx = lex(source);
  } catch (e) {
    const at = e instanceof LexError ? e.at : 0;
    return unchanged(source, [], [diag(source, at, `not compiled: ${(e as Error).message}`)]);
  }
  return rewrite(source, lx, opt);
}

/** What one view literal compiles to. */
export type ViewResult =
  | { readonly ok: true; readonly code: string; readonly n: number }
  | { readonly ok: false; readonly mistake: boolean; readonly message: string; readonly hole: number };

/**
 * Compile one template from its cooked string parts and marker codes (as the
 * markers return them at run time: -1 text, -2 class, -3 property, -4
 * attribute, -5 slot, -6 event; a number >= 0 is untyped). Returns a JS
 * expression that evaluates to the view, given the compiled runtime as `$hb`.
 */
export function compileView(strings: readonly string[], markers: readonly number[], svg: boolean): ViewResult {
  const a = analyze(strings, markers, svg);
  if (!a.ok) return a;
  return { ok: true, code: generate(a), n: a.n };
}

/** Check one herdsman-blocks (Layer 1) template the way its constructor would, at build time. */
export function checkTemplate(strings: readonly string[], markers: readonly number[], svg: boolean): ViewResult {
  const a = analyze(strings, markers, svg);
  return a.ok ? { ok: true, code: "", n: a.n } : a;
}

function unchanged(code: string, errors: Diagnostic[], warnings: Diagnostic[]): TransformResult {
  return { code, changed: false, errors, warnings, views: 0, calls: 0 };
}

function diag(src: string, at: number, message: string): Diagnostic {
  let line = 1,
    ls = 0;
  for (let i = src.indexOf("\n"); i >= 0 && i < at; i = src.indexOf("\n", i + 1)) {
    line++;
    ls = i + 1;
  }
  return { line, column: at - ls + 1, message };
}

// ---------------------------------------------------------------------------
// Scanner: one pass over the source into a flat token list
// ---------------------------------------------------------------------------

const IDENT = 1;
const PUNCT = 2;
const STR = 3;
const NUM = 4;
const REGEX = 5;
const TPL = 6; // `...` without substitutions
const HEAD = 7; // `...${
const MID = 8; // }...${
const TAIL = 9; // }...`
const PRIV = 10;

const DOTS = 1000;
const INC = 1001;
const DEC = 1002;

class LexError extends Error {
  readonly at: number;
  constructor(message: string, at: number) {
    super(message);
    this.at = at;
  }
}

interface Lexed {
  n: number;
  type: Uint8Array;
  start: Int32Array;
  end: Int32Array;
  /** Punctuator code (char code, or DOTS/INC/DEC). */
  val: Int32Array;
}

// Keywords after which a slash starts a regular expression.
const REGEX_AFTER = new Set([
  "return",
  "typeof",
  "instanceof",
  "in",
  "of",
  "new",
  "delete",
  "void",
  "throw",
  "case",
  "do",
  "else",
  "yield",
  "await",
]);

const isIdStart = (c: number): boolean =>
  (c >= 97 && c <= 122) || (c >= 65 && c <= 90) || c === 36 || c === 95 || c === 92 || c > 127;
const isIdPart = (c: number): boolean => isIdStart(c) || (c >= 48 && c <= 57);

function lex(s: string): Lexed {
  const len = s.length;
  let cap = Math.max(64, len >> 2);
  let type = new Uint8Array(cap),
    start = new Int32Array(cap),
    end = new Int32Array(cap),
    val = new Int32Array(cap);
  let n = 0;
  const push = (t: number, a: number, b: number, v: number): void => {
    if (n === cap) {
      cap *= 2;
      const t2 = new Uint8Array(cap),
        s2 = new Int32Array(cap),
        e2 = new Int32Array(cap),
        v2 = new Int32Array(cap);
      t2.set(type);
      s2.set(start);
      e2.set(end);
      v2.set(val);
      type = t2;
      start = s2;
      end = e2;
      val = v2;
    }
    type[n] = t;
    start[n] = a;
    end[n] = b;
    val[n] = v;
    n++;
  };
  // Brace stack: 0 for a plain brace, 1 for a template substitution.
  const braces: number[] = [];
  let i = 0;
  if (s.charCodeAt(0) === 35 && s.charCodeAt(1) === 33) while (i < len && s.charCodeAt(i) !== 10) i++; // #!

  // Scan template characters from i (just after ` or }); push the token; return the new position.
  const template = (from: number, opening: boolean): number => {
    let j = from;
    for (;;) {
      if (j >= len) throw new LexError("unterminated template literal", from);
      const c = s.charCodeAt(j);
      if (c === 92) j += 2;
      else if (c === 96) {
        push(opening ? TPL : TAIL, from - 1, j + 1, 0);
        return j + 1;
      } else if (c === 36 && s.charCodeAt(j + 1) === 123) {
        push(opening ? HEAD : MID, from - 1, j + 2, 0);
        braces.push(1);
        return j + 2;
      } else j++;
    }
  };

  const regexAllowed = (): boolean => {
    if (n === 0) return true;
    const t = type[n - 1]!;
    if (t === PUNCT) {
      const v = val[n - 1]!;
      return v !== 41 && v !== 93 && v !== INC && v !== DEC; // not ) ] ++ --
    }
    if (t === HEAD || t === MID) return true;
    if (t === IDENT) return REGEX_AFTER.has(s.slice(start[n - 1]!, end[n - 1]!));
    return false;
  };

  while (i < len) {
    const c = s.charCodeAt(i);
    // Whitespace and line terminators.
    if (c === 32 || c === 10 || c === 9 || c === 13 || c === 11 || c === 12 || c === 0xa0 || c === 0xfeff) {
      i++;
      continue;
    }
    if (c === 47) {
      const d = s.charCodeAt(i + 1);
      if (d === 47) {
        i += 2;
        while (i < len) {
          const e = s.charCodeAt(i);
          if (e === 10 || e === 13 || e === 0x2028 || e === 0x2029) break;
          i++;
        }
        continue;
      }
      if (d === 42) {
        const e = s.indexOf("*/", i + 2);
        if (e < 0) throw new LexError("unterminated comment", i);
        i = e + 2;
        continue;
      }
      if (regexAllowed()) {
        const from = i;
        let j = i + 1,
          cls = false;
        for (;;) {
          if (j >= len) throw new LexError("unterminated regular expression", from);
          const e = s.charCodeAt(j);
          if (e === 10 || e === 13) throw new LexError("unterminated regular expression", from);
          if (e === 92) j += 2;
          else {
            if (e === 91) cls = true;
            else if (e === 93) cls = false;
            else if (e === 47 && !cls) {
              j++;
              break;
            }
            j++;
          }
        }
        while (j < len && isIdPart(s.charCodeAt(j))) j++;
        push(REGEX, from, j, 0);
        i = j;
        continue;
      }
      push(PUNCT, i, i + 1, 47);
      i++;
      continue;
    }
    if (isIdStart(c)) {
      const from = i;
      i++;
      while (i < len && isIdPart(s.charCodeAt(i))) i++;
      push(IDENT, from, i, 0);
      continue;
    }
    if (c >= 48 && c <= 57) {
      const from = i;
      const hex = c === 48 && (s.charCodeAt(i + 1) | 32) === 120;
      i++;
      for (;;) {
        const e = s.charCodeAt(i);
        if (isIdPart(e) || e === 46) i++;
        else if ((e === 43 || e === 45) && !hex && (s.charCodeAt(i - 1) | 32) === 101) i++;
        else break;
      }
      push(NUM, from, i, 0);
      continue;
    }
    if (c === 34 || c === 39) {
      const from = i;
      let j = i + 1;
      for (;;) {
        if (j >= len) throw new LexError("unterminated string", from);
        const e = s.charCodeAt(j);
        if (e === c) break;
        if (e === 92) j += 2;
        else if (e === 10 || e === 13) throw new LexError("unterminated string", from);
        else j++;
      }
      push(STR, from, j + 1, 0);
      i = j + 1;
      continue;
    }
    if (c === 96) {
      i = template(i + 1, true);
      continue;
    }
    if (c === 123) {
      braces.push(0);
      push(PUNCT, i, i + 1, 123);
      i++;
      continue;
    }
    if (c === 125) {
      if (braces.pop() === 1) {
        i = template(i + 1, false);
        continue;
      }
      push(PUNCT, i, i + 1, 125);
      i++;
      continue;
    }
    if (c === 46) {
      const d = s.charCodeAt(i + 1);
      if (d >= 48 && d <= 57) {
        const from = i;
        i++;
        while (i < len && (isIdPart(s.charCodeAt(i)) || s.charCodeAt(i) === 46)) i++;
        push(NUM, from, i, 0);
        continue;
      }
      if (d === 46 && s.charCodeAt(i + 2) === 46) {
        push(PUNCT, i, i + 3, DOTS);
        i += 3;
        continue;
      }
    }
    if (c === 35) {
      const from = i;
      i++;
      while (i < len && isIdPart(s.charCodeAt(i))) i++;
      push(PRIV, from, i, 0);
      continue;
    }
    if ((c === 43 || c === 45) && s.charCodeAt(i + 1) === c) {
      push(PUNCT, i, i + 2, c === 43 ? INC : DEC);
      i += 2;
      continue;
    }
    push(PUNCT, i, i + 1, c);
    i++;
  }
  if (braces.length > 0 && braces.includes(1)) throw new LexError("unterminated template substitution", len);
  return { n, type, start, end, val };
}

// ---------------------------------------------------------------------------
// Module rewrite
// ---------------------------------------------------------------------------

// What a local name stands for.
const TAG_VIEW_HTML = 1;
const TAG_VIEW_SVG = 2;
const TAG_CORE_HTML = 3;
const TAG_CORE_SVG = 4;

// Marker codes as the markers return them at run time.
const MARKER_CODES: Readonly<Record<string, number>> = {
  text: -1,
  cls: -2,
  prop: -3,
  attr: -4,
  slot: -5,
  child: -5,
  on: -6,
};

interface Bindings {
  /** Local tag name -> tag kind. */
  readonly tags: Map<string, number>;
  /** Local marker name -> marker code. */
  readonly markers: Map<string, number>;
  /** Namespace import name -> true for the view module, false for herdsman-blocks. */
  readonly spaces: Map<string, boolean>;
  /** Token indexes of string tokens naming the view module (import and export sources). */
  readonly viewSources: number[];
}

function strValue(src: string, lx: Lexed, k: number): string {
  // Import specifiers: no escapes worth decoding.
  return src.slice(lx.start[k]! + 1, lx.end[k]! - 1);
}

const tok = (src: string, lx: Lexed, k: number): string => src.slice(lx.start[k]!, lx.end[k]!);
const isP = (lx: Lexed, k: number, code: number): boolean => k < lx.n && lx.type[k] === PUNCT && lx.val[k] === code;
const isId = (src: string, lx: Lexed, k: number, name: string): boolean =>
  k < lx.n && lx.type[k] === IDENT && lx.end[k]! - lx.start[k]! === name.length && src.startsWith(name, lx.start[k]!);

// Read the import and export declarations that name the library.
function bindings(src: string, lx: Lexed): Bindings {
  const b: Bindings = { tags: new Map(), markers: new Map(), spaces: new Map(), viewSources: [] };
  for (let k = 0; k < lx.n; k++) {
    const t = lx.type[k];
    if (t === IDENT && (isId(src, lx, k, "import") || isId(src, lx, k, "export"))) {
      // Dynamic import("...view.ts"): redirect it too.
      if (isP(lx, k + 1, 40) && lx.type[k + 2] === STR) {
        const m = LIB.exec(strValue(src, lx, k + 2));
        if (m && m[1]) b.viewSources.push(k + 2);
        continue;
      }
      // Find the `from "..."` of this declaration (within a bounded window).
      const isImport = isId(src, lx, k, "import");
      let j = k + 1,
        typeOnly = false;
      if (isId(src, lx, j, "type") && !isId(src, lx, j + 1, "from") && !isP(lx, j + 1, 44)) {
        typeOnly = true;
        j++;
      }
      let f = j;
      while (f < lx.n && f < j + 400 && !isP(lx, f, 59) && !isId(src, lx, f, "from")) {
        if (lx.type[f] === STR && f === j) break; // import "side-effect"
        f++;
      }
      if (!isId(src, lx, f, "from") || lx.type[f + 1] !== STR) continue;
      const m = LIB.exec(strValue(src, lx, f + 1));
      if (!m) continue;
      const view = m[1] !== undefined;
      if (view) b.viewSources.push(f + 1);
      if (!isImport || typeOnly) continue;
      // import [default,] { a as b, type c } | * as ns from "..."
      for (let q = j; q < f; q++) {
        if (isP(lx, q, 42) && isId(src, lx, q + 1, "as") && lx.type[q + 2] === IDENT) {
          b.spaces.set(tok(src, lx, q + 2), view);
          q += 2;
          continue;
        }
        if (!isP(lx, q, 123)) continue;
        for (q++; q < f && !isP(lx, q, 125); q++) {
          if (lx.type[q] !== IDENT) continue;
          let name = tok(src, lx, q);
          if (name === "type" && lx.type[q + 1] === IDENT && !isId(src, lx, q + 1, "as")) {
            // type-only specifier: skip it
            q++;
            if (isId(src, lx, q + 1, "as")) q += 2;
            continue;
          }
          let local = name;
          if (isId(src, lx, q + 1, "as") && lx.type[q + 2] === IDENT) {
            local = tok(src, lx, q + 2);
            q += 2;
          }
          if (name === "default") continue;
          if (name === "html") b.tags.set(local, view ? TAG_VIEW_HTML : TAG_CORE_HTML);
          else if (name === "svg") b.tags.set(local, view ? TAG_VIEW_SVG : TAG_CORE_SVG);
          else if (name in MARKER_CODES && (view ? name !== "slot" : name !== "child")) {
            b.markers.set(local, MARKER_CODES[name]!);
          }
        }
      }
    }
  }
  return b;
}

interface Literal {
  /** Token index of the tag (its first token). */
  readonly tagTok: number;
  /** Token index of the template's first token. */
  readonly first: number;
  /** Token index of the template's last token (TPL or TAIL). */
  readonly last: number;
  readonly kind: number;
  /** Token indexes of the HEAD/MID/TAIL (or TPL) tokens, in order. */
  readonly parts: number[];
}

// The tag kind at token k (an identifier or ns.member), or 0.
function tagAt(src: string, lx: Lexed, b: Bindings, k: number): { kind: number; first: number } | null {
  if (lx.type[k] !== IDENT) return null;
  const prevDot = k > 0 && isP(lx, k - 1, 46);
  if (!prevDot) {
    const kind = b.tags.get(tok(src, lx, k));
    return kind === undefined ? null : { kind, first: k };
  }
  // ns.html: the namespace must itself not follow a dot.
  if (k >= 2 && lx.type[k - 2] === IDENT && !(k >= 3 && isP(lx, k - 3, 46))) {
    const view = b.spaces.get(tok(src, lx, k - 2));
    if (view === undefined) return null;
    const name = tok(src, lx, k);
    if (name === "html") return { kind: view ? TAG_VIEW_HTML : TAG_CORE_HTML, first: k - 2 };
    if (name === "svg") return { kind: view ? TAG_VIEW_SVG : TAG_CORE_SVG, first: k - 2 };
  }
  return null;
}

// The marker code of the expression tokens [a, b): a marker call `m()`,
// `m<...>()`, `ns.m<...>()`, or a non-negative number literal; undefined otherwise.
function markerOf(src: string, lx: Lexed, bd: Bindings, a: number, b: number): number | undefined {
  if (b - a === 1 && lx.type[a] === NUM) {
    const v = Number(tok(src, lx, a).replace(/_/g, ""));
    return Number.isInteger(v) && v >= 0 ? v : undefined;
  }
  let k = a,
    code: number | undefined;
  if (lx.type[k] !== IDENT) return undefined;
  if (isP(lx, k + 1, 46)) {
    const view = bd.spaces.get(tok(src, lx, k));
    if (view === undefined || lx.type[k + 2] !== IDENT) return undefined;
    const name = tok(src, lx, k + 2);
    if (!(name in MARKER_CODES) || (view ? name === "slot" : name === "child")) return undefined;
    code = MARKER_CODES[name];
    k += 3;
  } else {
    code = bd.markers.get(tok(src, lx, k));
    k++;
  }
  if (code === undefined) return undefined;
  if (isP(lx, k, 60)) {
    // Type arguments: skip to the matching >.
    let depth = 0;
    for (; k < b; k++) {
      if (isP(lx, k, 60)) depth++;
      else if (isP(lx, k, 62) && --depth === 0) break;
    }
    if (depth !== 0) return undefined;
    k++;
  }
  return isP(lx, k, 40) && isP(lx, k + 1, 41) && k + 2 === b ? code : undefined;
}

// Cook a template part's raw text as the language does; undefined for an invalid escape.
function cook(raw: string): string | undefined {
  if (raw.indexOf("\\") < 0 && raw.indexOf("\r") < 0) return raw;
  let out = "";
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i]!;
    if (c === "\r") {
      out += "\n";
      if (raw[i + 1] === "\n") i++;
      continue;
    }
    if (c !== "\\") {
      out += c;
      continue;
    }
    const d = raw[++i];
    switch (d) {
      case "n":
        out += "\n";
        break;
      case "t":
        out += "\t";
        break;
      case "r":
        out += "\r";
        break;
      case "b":
        out += "\b";
        break;
      case "f":
        out += "\f";
        break;
      case "v":
        out += "\v";
        break;
      case "0":
        if (/[0-9]/.test(raw[i + 1] ?? "")) return undefined;
        out += "\0";
        break;
      case "x": {
        const h = raw.slice(i + 1, i + 3);
        if (!/^[0-9a-fA-F]{2}$/.test(h)) return undefined;
        out += String.fromCharCode(parseInt(h, 16));
        i += 2;
        break;
      }
      case "u": {
        let h: string;
        if (raw[i + 1] === "{") {
          const e = raw.indexOf("}", i);
          if (e < 0) return undefined;
          h = raw.slice(i + 2, e);
          if (!/^[0-9a-fA-F]+$/.test(h) || parseInt(h, 16) > 0x10ffff) return undefined;
          i = e;
        } else {
          h = raw.slice(i + 1, i + 5);
          if (!/^[0-9a-fA-F]{4}$/.test(h)) return undefined;
          i += 4;
        }
        out += String.fromCodePoint(parseInt(h, 16));
        break;
      }
      case "\r":
        if (raw[i + 1] === "\n") i++;
        break;
      case "\n":
      case " ":
      case " ":
        break;
      case undefined:
        return undefined;
      default:
        if (d >= "1" && d <= "9") return undefined;
        out += d;
    }
  }
  return out;
}

const KEYWORD_STARTS = new Set([
  "const",
  "let",
  "var",
  "function",
  "class",
  "export",
  "import",
  "return",
  "if",
  "for",
  "while",
  "do",
  "switch",
  "try",
  "throw",
  "type",
  "interface",
  "declare",
  "async",
]);

function rewrite(src: string, lx: Lexed, opt: TransformOptions): TransformResult {
  const bd = bindings(src, lx);
  if (bd.tags.size === 0 && bd.spaces.size === 0 && bd.viewSources.length === 0) return unchanged(src, [], []);
  const errors: Diagnostic[] = [],
    warnings: Diagnostic[] = [];
  const edits: { a: number; b: number; text: string }[] = [];
  let header = "";
  let views = 0,
    calls = 0;
  // Local const name -> its description class, hole count and declaring token.
  const bound = new Map<string, { d: string; n: number; at: number }>();
  const twice = new Set<string>();

  // 1. Tagged templates.
  for (let k = 0; k < lx.n; k++) {
    const t = lx.type[k];
    if (t !== TPL && t !== HEAD) continue;
    if (k === 0) continue;
    const tag = tagAt(src, lx, bd, k - 1);
    if (tag === null) continue;
    // The template's parts, skipping nested templates inside substitutions.
    const parts = [k];
    let last = k;
    if (t === HEAD) {
      let depth = 0;
      for (let q = k + 1; q < lx.n; q++) {
        const u = lx.type[q];
        if (u === HEAD) depth++;
        else if (u === TAIL) {
          if (depth === 0) {
            parts.push(q);
            last = q;
            break;
          }
          depth--;
        } else if (u === MID && depth === 0) parts.push(q);
      }
      if (last === k) continue;
    }
    const lit: Literal = { tagTok: tag.first, first: k, last, kind: tag.kind, parts };
    const strings: string[] = [];
    let cooked = true;
    for (let p = 0; p < parts.length; p++) {
      const q = parts[p]!;
      const a = lx.start[q]! + 1,
        e = lx.end[q]! - (lx.type[q] === HEAD || lx.type[q] === MID ? 2 : 1);
      const c = cook(src.slice(a, e));
      if (c === undefined) cooked = false;
      strings.push(c ?? "");
    }
    const markers: number[] = [];
    let known = true;
    for (let p = 0; p + 1 < parts.length; p++) {
      const m = markerOf(src, lx, bd, parts[p]! + 1, parts[p + 1]!);
      if (m === undefined) known = false;
      markers.push(m ?? 0);
    }
    const isView = lit.kind === TAG_VIEW_HTML || lit.kind === TAG_VIEW_SVG;
    const isSvg = lit.kind === TAG_VIEW_SVG || lit.kind === TAG_CORE_SVG;
    if (!cooked) {
      warnings.push(diag(src, lx.start[k]!, "not compiled: a template part has an invalid escape"));
      continue;
    }
    const a = analyze(strings, known ? markers : markers.map(() => 0), isSvg);
    if (!a.ok) {
      const at = a.hole >= 0 && a.hole + 1 < parts.length ? lx.end[parts[a.hole]!]! - 2 : lx.start[k]!;
      if (a.mistake && (known || !a.message.includes("typed"))) {
        if (opt.mistakes === "runtime") warnings.push(diag(src, at, a.message + " (left to run time)"));
        else errors.push(diag(src, at, a.message));
      } else if (isView) warnings.push(diag(src, at, `not compiled: ${a.message}`));
      continue;
    }
    if (!isView) continue; // herdsman-blocks templates: checked only
    if (!known) {
      warnings.push(diag(src, lx.start[k]!, "not compiled: a hole is not a marker call (text(), cls(), ...)"));
      continue;
    }
    const id = "$hbv" + views,
      dn = "$hbD" + views;
    views++;
    header += `var ${id}=/*@__PURE__*/${generate(a)},${dn}=${id}.D;`;
    const spanA = lx.start[lit.tagTok]!,
      spanB = lx.end[last]!;
    edits.push({ a: spanA, b: spanB, text: id + ".i()" + newlines(src, spanA, spanB) });
    // const Name = <literal> ;  -> calls of Name may construct directly.
    const decl = lit.tagTok - 2;
    if (
      decl >= 1 &&
      isP(lx, decl + 1, 61) &&
      lx.type[decl] === IDENT &&
      isId(src, lx, decl - 1, "const") &&
      endsExpression(src, lx, last + 1)
    ) {
      const name = tok(src, lx, decl);
      if (bound.has(name)) twice.add(name);
      bound.set(name, { d: dn, n: a.n, at: decl });
    }
    k = last;
  }

  // 2. Calls `Name([a, b, ...])` of views bound above, when Name is never shadowed or reassigned.
  if (bound.size > 0) {
    const safe = new Map<string, boolean>();
    for (const name of bound.keys()) safe.set(name, !twice.has(name));
    for (let k = 0; k < lx.n; k++) {
      if (lx.type[k] !== IDENT) continue;
      const name = tok(src, lx, k);
      if (!safe.get(name)) continue;
      if (k > 0 && isP(lx, k - 1, 46)) continue; // .Name: a property
      if (bound.get(name)!.at === k) continue; // its declaration
      if (isP(lx, k + 1, 40)) continue; // a call
      if (k > 0 && isId(src, lx, k - 1, "typeof")) continue;
      safe.set(name, false);
    }
    for (let k = 0; k + 1 < lx.n; k++) {
      if (lx.type[k] !== IDENT || !isP(lx, k + 1, 40) || !isP(lx, k + 2, 91)) continue;
      if (k > 0 && isP(lx, k - 1, 46)) continue;
      const name = tok(src, lx, k);
      const v = bound.get(name);
      if (v === undefined || !safe.get(name)) continue;
      // Elements of the array literal at depth 0.
      let depth = 0,
        count = 0,
        empty = true,
        ok = true,
        q = k + 3;
      for (; q < lx.n; q++) {
        const u = lx.type[q];
        if (u === PUNCT) {
          const c = lx.val[q]!;
          if (c === 40 || c === 91 || c === 123) depth++;
          else if (c === 41 || c === 125) depth--;
          else if (c === 93) {
            if (depth === 0) break;
            depth--;
          } else if (depth === 0 && c === 44) {
            if (empty) ok = false; // an elision
            count++;
            empty = true;
            continue;
          } else if (depth === 0 && c === DOTS) ok = false; // a spread
        } else if (u === HEAD) depth++;
        else if (u === TAIL) depth--;
        empty = false;
      }
      if (!empty) count++;
      if (!ok || q >= lx.n || count !== v.n || !isP(lx, q + 1, 41)) continue;
      edits.push({ a: lx.start[k]!, b: lx.end[k + 2]!, text: `new ${v.d}(` });
      edits.push({ a: lx.start[q]!, b: lx.end[q + 1]!, text: ")" });
      calls++;
    }
  }

  if (errors.length > 0) return unchanged(src, errors, warnings);
  // 3. Point imports of the view module at the compiled runtime.
  const runtime = JSON.stringify(opt.runtime);
  for (const q of bd.viewSources) edits.push({ a: lx.start[q]!, b: lx.end[q]!, text: runtime });
  if (edits.length === 0) return unchanged(src, errors, warnings);

  edits.sort((x, y) => x.a - y.a);
  let out = "",
    at = 0;
  let insert = 0;
  if (src.charCodeAt(0) === 35 && src.charCodeAt(1) === 33) {
    const nl = src.indexOf("\n");
    insert = nl < 0 ? src.length : nl + 1;
  }
  const head = views > 0 ? `import * as $hb from ${runtime};${header}` : "";
  if (head !== "") {
    out = src.slice(0, insert) + head;
    at = insert;
  }
  for (const e of edits) {
    out += src.slice(at, e.a) + e.text;
    at = e.b;
  }
  out += src.slice(at);
  return { code: out, changed: true, errors, warnings, views, calls };
}

// Whether the expression ends before token k (`;`, `,`, `)`, `}`, end, or a statement keyword).
function endsExpression(src: string, lx: Lexed, k: number): boolean {
  if (k >= lx.n) return true;
  if (lx.type[k] === PUNCT) {
    const c = lx.val[k];
    return c === 59 || c === 44 || c === 125;
  }
  return lx.type[k] === IDENT && KEYWORD_STARTS.has(tok(src, lx, k));
}

function newlines(src: string, a: number, b: number): string {
  let out = "";
  for (let i = src.indexOf("\n", a); i >= 0 && i < b; i = src.indexOf("\n", i + 1)) out += "\n";
  return out;
}

// ---------------------------------------------------------------------------
// Template analysis: the run-time compiler's steps, at build time
// ---------------------------------------------------------------------------

// Kind codes as herdsman-blocks numbers them.
const K_TEXT = 0;
const K_CLASS = 1;
const K_PROP = 2;
const K_ATTR = 3;
const K_SLOT = 4;
const K_EVENT = 5;
const MARKER_NAMES = ["text", "class", "prop", "attr", "slot", "event"];

const ATTR_HOLE = /([^\s"'=<>/]+)\s*=\s*(["']?)$/;
const MARKER = /^bk\d+$/;
const FORMATTING = /^\s*\n\s*$/;

interface Analysis {
  readonly ok: true;
  readonly n: number;
  readonly svg: boolean;
  readonly kinds: number[];
  readonly names: (string | undefined)[];
  readonly paths: number[][];
  /** The static markup the prototype is parsed from: holes removed, text holes as empty comments. */
  readonly markup: string;
}

type Failure = { readonly ok: false; readonly mistake: boolean; readonly message: string; readonly hole: number };
const mistake = (message: string, hole = -1): Failure => ({ ok: false, mistake: true, message, hole });
const hazard = (message: string): Failure => ({ ok: false, mistake: false, message, hole: -1 });

function analyze(strings: readonly string[], holes: readonly number[], svg: boolean): Analysis | Failure {
  // Steps 1 to 3 of compiling, exactly as herdsman-blocks does them.
  const n = strings.length - 1;
  const parts = strings.slice();
  let src = "";
  let inTag = false;
  for (let i = 0; i <= n; i++) {
    const s = parts[i]!;
    for (let c = 0; c < s.length; c++) {
      if (s[c] === "<") inTag = true;
      else if (s[c] === ">") inTag = false;
    }
    if (i === n) {
      src += s;
      break;
    }
    if (inTag) {
      const m = ATTR_HOLE.exec(s);
      if (!m) return mistake(`herdsman-blocks: hole ${i} is inside a tag but not an attribute value`, i);
      src += s.slice(0, m.index) + ` bk${i}="${encodeURIComponent(m[1]!)}"`;
      if (m[2]) parts[i + 1] = parts[i + 1]!.replace(m[2], "");
    } else src += s + `<!--bk${i}-->`;
  }
  // Step 4: parse, as the browser would (or give up).
  const tree = parseHTML(src.trim().replace(/\r\n?/g, "\n"), svg);
  if (typeof tree === "string") return hazard(tree);
  const root = tree.children[0];
  if (!root || tree.children.length !== 1 || root.type !== 1)
    return mistake("herdsman-blocks: a template must have exactly one root element");
  // Step 5: walk.
  const kinds: number[] = new Array(n).fill(-1);
  const names: (string | undefined)[] = new Array(n);
  const paths: (number[] | undefined)[] = new Array(n);
  const problem = walk(root, [], paths, kinds, names);
  if (problem !== null) return hazard(problem);
  for (let i = 0; i < n; i++) {
    if (!paths[i]) return mistake(`herdsman-blocks: hole ${i} could not be located`, i);
    const h = holes[i];
    if (h === undefined || h >= 0) continue;
    const k = kinds[i]!,
      at = k === K_PROP && names[i]!.startsWith("on") ? K_EVENT : k;
    if (h === -1 - K_SLOT && at === K_TEXT) kinds[i] = K_SLOT;
    else if (-1 - h !== at) {
      const want = MARKER_NAMES[-1 - h] ?? "unknown",
        got = MARKER_NAMES[at] ?? "unknown",
        hint =
          at === K_EVENT
            ? ": an on* name takes on()"
            : at === K_ATTR && names[i] === "class"
              ? ": class on an SVG element is an attribute, use attr()"
              : "";
      return mistake(`herdsman-blocks: hole ${i} is typed ${want}, but its position makes it ${got}${hint}`, i);
    }
  }
  return { ok: true, n, svg, kinds, names, paths: paths as number[][], markup: serialize(root) };
}

// --- A conservative model of the HTML parser -------------------------------

interface HNode {
  type: 1 | 3 | 8;
  /** Element: lowercased tag name. */
  name: string;
  /** Element: the tag name as written. Text and comment: their raw text. */
  raw: string;
  svg: boolean;
  attrs: { name: string; raw: string }[];
  children: HNode[];
  selfClose: boolean;
  /** A text-hole placeholder (comment) after the walk. */
  hole: boolean;
}

const VOID = new Set([
  "area",
  "base",
  "br",
  "col",
  "embed",
  "hr",
  "img",
  "input",
  "link",
  "meta",
  "source",
  "track",
  "wbr",
  "param",
  "keygen",
]);
const RAWTEXT = new Set(["script", "style", "xmp", "iframe", "noembed", "noframes", "textarea", "title"]);
const NEVER = new Set([
  "html",
  "head",
  "body",
  "frameset",
  "frame",
  "template",
  "image",
  "isindex",
  "math",
  "plaintext",
  "noscript",
  "ruby",
  "rb",
  "rt",
  "rp",
  "rtc",
  "menuitem",
]);
const CLOSES_P = new Set(
  "address article aside blockquote center details dialog dir div dl fieldset figcaption figure footer form h1 h2 h3 h4 h5 h6 header hgroup hr li dd dt main menu nav ol p pre listing search section summary table ul xmp".split(
    " ",
  ),
);
const SELF_NESTING = new Set(["a", "button", "form", "nobr", "li", "dd", "dt", "select", "option", "optgroup"]);
const HEADINGS = new Set(["h1", "h2", "h3", "h4", "h5", "h6"]);
// Where table parts may appear, and what they may hold.
const TABLE_PARENT: Readonly<Record<string, readonly string[]>> = {
  caption: ["table"],
  colgroup: ["table"],
  thead: ["table"],
  tbody: ["table"],
  tfoot: ["table"],
  tr: ["tbody", "thead", "tfoot"],
  td: ["tr"],
  th: ["tr"],
  col: ["colgroup"],
};
const TABLE_CHILDREN: Readonly<Record<string, readonly string[]>> = {
  table: ["caption", "colgroup", "thead", "tbody", "tfoot", "script", "style"],
  thead: ["tr", "script", "style"],
  tbody: ["tr", "script", "style"],
  tfoot: ["tr", "script", "style"],
  tr: ["td", "th", "script", "style"],
  colgroup: ["col"],
  select: ["option", "optgroup", "hr"],
  optgroup: ["option"],
};
const BREAKOUT = new Set(
  "b big blockquote body br center code dd div dl dt em embed h1 h2 h3 h4 h5 h6 head hr i img li listing menu meta nobr ol p pre ruby s small span strong strike sub sup table tt u ul var font".split(
    " ",
  ),
);
const INTEGRATION = new Set(["foreignobject", "desc", "title"]);

const isWs = (c: string): boolean => c === " " || c === "\n" || c === "\t" || c === "\f";
const isAlpha = (c: string | undefined): boolean => c !== undefined && /[a-zA-Z]/.test(c);

/** Parse markup the way the HTML parser builds a template's content, or return why it might not. */
function parseHTML(s: string, svg: boolean): HNode | string {
  const mk = (type: 1 | 3 | 8, name: string, raw: string, inSvg: boolean): HNode => ({
    type,
    name,
    raw,
    svg: inSvg,
    attrs: [],
    children: [],
    selfClose: false,
    hole: false,
  });
  const top = mk(1, svg ? "svg" : "#fragment", "", svg);
  const stack: HNode[] = [top];
  const cur = (): HNode => stack[stack.length - 1]!;
  // Children of the current node are SVG unless it is HTML or an integration point.
  const svgContent = (): boolean => cur().svg && !INTEGRATION.has(cur().name);
  const open = (name: string): boolean => stack.some((e) => !e.svg && e.name === name);
  let dropLF = false;
  const addText = (raw: string): string | null => {
    if (dropLF) {
      dropLF = false;
      if (raw[0] === "\n") raw = raw.slice(1);
      if (raw === "") return null;
    }
    if (raw.indexOf("\0") >= 0) return "a NUL character";
    const c = cur();
    if (!svgContent() && c !== top && TABLE_CHILDREN[c.name] && c.name !== "select" && c.name !== "optgroup") {
      if (!/^[ \n\t\f]*$/.test(raw)) return `text directly inside <${c.name}> (foster parenting)`;
    }
    c.children.push(mk(3, "#text", raw, false));
    return null;
  };
  let i = 0;
  const len = s.length;
  while (i < len) {
    const c = cur();
    // Raw text and RCDATA elements (HTML only): everything up to the matching end tag is text.
    if (!c.svg && c !== top && RAWTEXT.has(c.name)) {
      const re = new RegExp(`</${c.name}[\\t\\n\\f />]`, "ig");
      re.lastIndex = i;
      const m = re.exec(s + " ");
      const e = m ? m.index : len;
      if (e > i) {
        const err = addText(s.slice(i, e));
        if (err) return err;
      }
      dropLF = false;
      i = e;
      if (i >= len) break;
    }
    const ch = s[i]!;
    if (ch !== "<") {
      let e = s.indexOf("<", i);
      if (e < 0) e = len;
      const err = addText(s.slice(i, e));
      if (err) return err;
      i = e;
      continue;
    }
    dropLF = false;
    const next = s[i + 1];
    if (s.startsWith("<!--", i)) {
      if (s.startsWith("<!-->", i) || s.startsWith("<!--->", i)) return "an abruptly closed comment";
      const e = s.indexOf("-->", i + 4);
      if (e < 0) return "an unterminated comment";
      const data = s.slice(i + 4, e);
      if (data.includes("--!>") || data.endsWith("-") || data.includes("<!--")) return "an unusual comment";
      cur().children.push(mk(8, "#comment", data, false));
      i = e + 3;
      continue;
    }
    if (next === "!" || next === "?") return "a declaration or processing instruction";
    if (next === "/") {
      if (!isAlpha(s[i + 2])) return "a malformed end tag";
      let e = i + 2;
      while (e < len && !isWs(s[e]!) && s[e] !== ">" && s[e] !== "/") e++;
      const name = s.slice(i + 2, e).toLowerCase();
      while (e < len && isWs(s[e]!)) e++;
      if (s[e] !== ">") return "an end tag with attributes";
      if (stack.length === 1 || cur().name !== name) return `a misnested or implied end tag </${name}>`;
      stack.pop();
      i = e + 1;
      continue;
    }
    if (!isAlpha(next)) {
      // A lone "<" is text.
      const err = addText("<");
      if (err) return err;
      i++;
      continue;
    }
    // Start tag.
    let e = i + 1;
    while (e < len && !isWs(s[e]!) && s[e] !== ">" && s[e] !== "/") e++;
    const raw = s.slice(i + 1, e);
    if (!/^[a-zA-Z][a-zA-Z0-9:_.-]*$/.test(raw)) return `an unusual tag name <${raw}>`;
    const name = raw.toLowerCase();
    const inSvg = svgContent() || (name === "svg" && !svgContent());
    const el = mk(1, name, raw, inSvg || name === "svg");
    // Attributes.
    let closed = false;
    for (;;) {
      while (e < len && isWs(s[e]!)) e++;
      if (e >= len) return "an unterminated tag";
      if (s[e] === ">") {
        e++;
        closed = true;
        break;
      }
      if (s[e] === "/") {
        if (s[e + 1] !== ">") return "a stray / in a tag";
        el.selfClose = true;
        e += 2;
        closed = true;
        break;
      }
      const a = e;
      while (e < len && !isWs(s[e]!) && s[e] !== "/" && s[e] !== ">" && s[e] !== "=") e++;
      const an = s.slice(a, e);
      if (an === "" || /["'<]/.test(an)) return "an unusual attribute name";
      while (e < len && isWs(s[e]!)) e++;
      if (s[e] === "=") {
        e++;
        while (e < len && isWs(s[e]!)) e++;
        const q = s[e];
        if (q === '"' || q === "'") {
          const z = s.indexOf(q, e + 1);
          if (z < 0) return "an unterminated attribute value";
          e = z + 1;
          if (e < len && !isWs(s[e]!) && s[e] !== ">" && s[e] !== "/") return "attributes without space between";
        } else {
          const v = e;
          while (e < len && !isWs(s[e]!) && s[e] !== ">") e++;
          if (e === v || /["'<=`]/.test(s.slice(v, e))) return "an unusual unquoted attribute value";
        }
      }
      if (el.attrs.some((x) => x.name === an.toLowerCase())) return "a duplicate attribute";
      el.attrs.push({ name: an.toLowerCase(), raw: s.slice(a, e).replace(/\s+$/, "") });
    }
    if (!closed) return "an unterminated tag";
    i = e;
    // Tree construction.
    const p = cur();
    if (svgContent()) {
      if (BREAKOUT.has(name)) return `<${name}> inside SVG content`;
      p.children.push(el);
      if (!el.selfClose) stack.push(el);
      continue;
    }
    if (NEVER.has(name)) return `<${name}> is parsed specially`;
    if (CLOSES_P.has(name) && open("p")) return `<${name}> inside <p> closes it`;
    if (SELF_NESTING.has(name) && open(name)) return `<${name}> inside <${name}>`;
    if ((name === "dd" || name === "dt") && (open("dd") || open("dt"))) return `<${name}> inside a dd or dt`;
    if (name === "option" && open("option")) return "<option> inside <option>";
    if (HEADINGS.has(name) && HEADINGS.has(p.name)) return "a heading inside a heading";
    const parents = TABLE_PARENT[name];
    if (parents && p !== top && !parents.includes(p.name)) return `<${name}> outside its table parent`;
    const allowed = p !== top ? TABLE_CHILDREN[p.name] : undefined;
    if (allowed && !allowed.includes(name)) return `<${name}> inside <${p.name}>`;
    if (open("select") && name !== "option" && name !== "optgroup" && name !== "hr") return `<${name}> inside <select>`;
    if (
      open("table") &&
      !parents &&
      !TABLE_CHILDREN[name] &&
      stack.every((x) => x.name !== "td" && x.name !== "th" && x.name !== "caption")
    )
      return `<${name}> inside a table outside a cell`;
    p.children.push(el);
    if (VOID.has(name)) continue;
    stack.push(el);
    if (name === "pre" || name === "listing" || name === "textarea") dropLF = true;
  }
  // Elements still open at the end are closed, as the parser does.
  return top;
}

// Remove formatting whitespace, find the markers: herdsman-blocks' walk.
function walk(
  el: HNode,
  path: number[],
  paths: (number[] | undefined)[],
  kinds: number[],
  names: (string | undefined)[],
): string | null {
  for (let a = el.attrs.length - 1; a >= 0; a--) {
    const at = el.attrs[a]!;
    if (!MARKER.test(at.name)) continue;
    const i = +at.name.slice(2);
    if (i >= paths.length || paths[i] !== undefined) return "a marker-like attribute";
    const q = at.raw.indexOf("=");
    if (q < 0) return "a marker-like attribute";
    const value = at.raw.slice(q + 1).replace(/^["']|["']$/g, "");
    const name = decodeURIComponent(value);
    el.attrs.splice(a, 1);
    paths[i] = path;
    if (name === "class" && !el.svg) kinds[i] = K_CLASS;
    else if (name[0] === "." || name.startsWith("on")) {
      kinds[i] = K_PROP;
      names[i] = name[0] === "." ? name.slice(1) : name.toLowerCase();
    } else {
      kinds[i] = K_ATTR;
      names[i] = name;
    }
  }
  const keepSpace = !el.svg && (el.name === "pre" || el.name === "textarea");
  const kids = el.children;
  let k = 0;
  for (let c = 0; c < kids.length; c++) {
    const child = kids[c]!;
    if (!keepSpace && child.type === 3) {
      // A character reference might decode to whitespace that makes this formatting.
      if (child.raw.includes("&") && /^(?:\s|&[^;\s]*;?)*$/.test(child.raw) && /\n|&#|&newline;/i.test(child.raw))
        return "text of only whitespace and character references";
      if (FORMATTING.test(child.raw)) {
        kids.splice(c--, 1);
        continue;
      }
    }
    if (child.type === 8 && MARKER.test(child.raw)) {
      const i = +child.raw.slice(2);
      if (i >= paths.length || paths[i] !== undefined) return "a marker-like comment";
      child.hole = true;
      paths[i] = path.concat(k);
      kinds[i] = K_TEXT;
    } else if (child.type === 1) {
      const r = walk(child, path.concat(k), paths, kinds, names);
      if (r !== null) return r;
    }
    k++;
  }
  return null;
}

function serialize(el: HNode): string {
  let out = "<" + el.raw;
  for (const a of el.attrs) out += " " + a.raw;
  // " />": an unquoted value before a bare "/" would take the slash.
  if ((el.svg && el.selfClose) || (!el.svg && VOID.has(el.name))) return out + " />";
  out += ">";
  const lf = !el.svg && (el.name === "pre" || el.name === "listing" || el.name === "textarea");
  for (let c = 0; c < el.children.length; c++) {
    const ch = el.children[c]!;
    if (ch.type === 1) out += serialize(ch);
    else if (ch.type === 8) out += ch.hole ? "<!---->" : "<!--" + ch.raw + "-->";
    else out += (lf && c === 0 && ch.raw[0] === "\n" ? "\n" : "") + ch.raw;
  }
  return out + "</" + el.raw + ">";
}

// ---------------------------------------------------------------------------
// Code generation
// ---------------------------------------------------------------------------

/** Events delegated to the render root (the same list as the view layer's). */
export const BUBBLING: ReadonlySet<string> = new Set([
  "auxclick",
  "beforeinput",
  "change",
  "click",
  "compositionend",
  "compositionstart",
  "compositionupdate",
  "contextmenu",
  "dblclick",
  "focusin",
  "focusout",
  "input",
  "keydown",
  "keyup",
  "mousedown",
  "mousemove",
  "mouseout",
  "mouseover",
  "mouseup",
  "pointerdown",
  "pointermove",
  "pointerout",
  "pointerover",
  "pointerup",
  "submit",
]);

const IDENT_RE = /^[A-Za-z_$][A-Za-z0-9_$]*$/;
const member = (name: string): string => (IDENT_RE.test(name) ? "." + name : "[" + JSON.stringify(name) + "]");

// A straight-line walk from r to the given holes. Each step starts from the
// nearest node already reached (an earlier sibling, else the parent); a node
// more than one step starts from gets a variable, the rest are inlined.
function walker(paths: readonly number[][], holes: readonly number[]): string {
  const nodes = new Map<string, { base: string; suf: string; uses: number }>();
  const order: string[] = [];
  const get = (p: readonly number[]): string => {
    if (p.length === 0) return "r";
    const key = p.join(",");
    if (nodes.has(key)) return key;
    const k = p[p.length - 1]!,
      up = p.slice(0, -1);
    let j = k - 1;
    while (j >= 0 && !nodes.has(up.concat(j).join(","))) j--;
    let base: string, suf: string;
    if (j >= 0) {
      base = up.concat(j).join(",");
      suf = ".nextSibling".repeat(k - j);
    } else {
      base = get(up);
      suf = ".firstChild" + ".nextSibling".repeat(k);
    }
    if (base !== "r") nodes.get(base)!.uses++;
    nodes.set(key, { base, suf, uses: 0 });
    order.push(key);
    return key;
  };
  const keys = holes.map((i) => {
    const key = get(paths[i]!);
    if (key !== "r") nodes.get(key)!.uses++;
    return key;
  });
  const names = new Map<string, string>();
  const expr = (key: string): string => {
    if (key === "r") return "r";
    const hit = names.get(key);
    if (hit) return hit;
    const nd = nodes.get(key)!;
    return expr(nd.base) + nd.suf;
  };
  let body = "";
  for (const key of order) {
    if (nodes.get(key)!.uses < 2) continue;
    const name = "a" + names.size;
    body += `var ${name}=${expr(key)};`;
    names.set(key, name);
  }
  holes.forEach((i, h) => (body += `s[${i}]=${expr(keys[h]!)};`));
  return "(r,s)=>{" + body + "}";
}

// A call writing value v into node expression x, by kind (the writers live in the runtime).
function writer(kind: number, name: string | undefined, x: string, v: string): string {
  switch (kind) {
    case K_TEXT:
      return `$hb.wt(${x},${v})`;
    case K_CLASS:
      return `$hb.wc(${x},${v})`;
    case K_ATTR:
      return `$hb.wa(${x},${JSON.stringify(name)},${v})`;
    default:
      return `${x}${member(name!)}=${v}`;
  }
}

/**
 * The view's code: a call of the runtime's V with the static markup, one
 * letter per hole (t text, c class, a attribute, p property, d delegated
 * handler, h direct handler, s slot), the delegated event types, and the
 * generated parts: the description constructor, the walk to every hole,
 * the bake of values into the prototype, what mounting does for eager
 * holes, and the patch.
 */
function generate(a: Analysis): string {
  const n = a.n,
    kinds = a.kinds,
    names = a.names;
  let letters = "";
  const types: (string | undefined)[] = new Array(n);
  let delegates = false;
  for (let i = 0; i < n; i++) {
    const k = kinds[i]!,
      name = names[i];
    if (k === K_SLOT) letters += "s";
    else if (k === K_PROP && name!.startsWith("on")) {
      const type = name!.slice(2);
      if (BUBBLING.has(type)) {
        letters += "d";
        types[i] = type;
        delegates = true;
      } else letters += "h";
    } else letters += k === K_PROP ? "p" : k === K_TEXT ? "t" : k === K_CLASS ? "c" : "a";
  }
  const all = Array.from({ length: n }, (_, i) => i);
  const D = `function(${all.map((i) => "v" + i).join(",")}){${all.map((i) => `this.$${i}=v${i};`).join("")}}`;
  let bake = "";
  for (const i of all)
    if ("tca".includes(letters[i]!))
      bake += `if(p[${i}]!==(v=d.$${i}))${writer(kinds[i]!, names[i], `q[${i}]`, `p[${i}]=v`)};`;
  let extras = "";
  for (const i of all) {
    const l = letters[i]!,
      nm = JSON.stringify(names[i]);
    if (l === "h") extras += `$hb.attach(s[${i}],${nm},F[${i}]);`;
    else if (l === "d") extras += `if(x)$hb.attach(s[${i}],${nm},F[${i}]);`;
    else if (l === "p") extras += `if((v=d.$${i})!=null)s[${i}]${member(names[i]!)}=v;`;
    else if (l === "s") extras += `s[${i}]=$hb.slotOf(s[${i}],d.$${i});`;
  }
  // Views with child, property or direct handler holes locate every hole at mount.
  const located = /[sph]/.test(letters);
  let patch = "";
  for (const i of all) {
    const l = letters[i]!;
    if (l === "d" || l === "h") continue; // handlers read the description when they run
    const cond = `if((v=d.$${i})!==o.$${i})`;
    const node = located ? `s[${i}]` : `(s||(s=$hb.L(i)))[${i}]`;
    patch += cond + (l === "s" ? `$hb.setChild(${node},v)` : writer(kinds[i]!, names[i], node, "v")) + ";";
  }
  const ty = delegates
    ? "[" +
      types.map((x) => (x === undefined ? "" : JSON.stringify(x))).join(",") +
      (types[n - 1] === undefined ? ",]" : "]")
    : "0";
  return (
    `$hb.V(${JSON.stringify(a.markup)},${a.svg ? 1 : 0},${JSON.stringify(letters)},${ty},${D},` +
    `${walker(a.paths, all)},` +
    (bake ? `(d,q,p)=>{let v;${bake}}` : "0") +
    "," +
    (extras ? `(s,d,x,F)=>{let v;${extras}}` : "0") +
    "," +
    (patch ? `(i,d,o)=>{let v,s=i.s;${patch}}` : "0") +
    ")"
  );
}
