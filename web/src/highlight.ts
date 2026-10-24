// A small syntax highlighter: a line tokenizer per language, with state that
// carries block comments and multi-line strings from one line to the next.
// It only ever produces text in spans, so nothing in a file can become HTML.

/** Token kinds; each maps to a --hl-* color. "" is plain text. */
export type Kind =
  | ""
  | "comment"
  | "keyword"
  | "string"
  | "number"
  | "type"
  | "func"
  | "constant"
  | "property"
  | "tag"
  | "attr"
  | "meta"
  | "variable";

export type Piece = [Kind, string];

/** What is still open at the end of a line: a block comment or a string, by its closing text. */
export interface State {
  close: string;
  kind: Kind;
  escape: boolean;
}

interface Str {
  open: string;
  close: string;
  multi?: boolean;
  raw?: boolean; // no backslash escapes
}

/** A rule tried at each position: its regex is sticky; groups, if given, color the captures in order. */
interface Rule {
  re: RegExp;
  kind?: Kind;
  groups?: Kind[];
  lineStart?: boolean;
}

interface Lang {
  line?: string[];
  block?: [string, string][];
  strings?: Str[];
  keywords?: string[];
  types?: string[];
  constants?: string[];
  rules?: Rule[];
  /** Capitalized names are types. */
  capsTypes?: boolean;
  /** The name after one of these words is a type. */
  typeAfter?: string[];
  /** A string followed by ":" is a key (JSON, YAML flow maps). */
  keyStrings?: boolean;
  caseless?: boolean;
  noNumbers?: boolean;
  noIdents?: boolean;
}

interface Compiled extends Lang {
  kw: Set<string>;
  ty: Set<string>;
  co: Set<string>;
  after: Set<string>;
}

const words = (s: string) => s.split(/\s+/).filter(Boolean);

const cStrings: Str[] = [
  { open: '"', close: '"' },
  { open: "'", close: "'" },
];
const cBlock: [string, string][] = [["/*", "*/"]];

const defs: Record<string, Lang> = {
  go: {
    line: ["//"],
    block: cBlock,
    strings: [{ open: "`", close: "`", multi: true, raw: true }, ...cStrings],
    keywords: words(
      "break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var",
    ),
    types: words(
      "any bool byte comparable complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr",
    ),
    constants: words(
      "true false nil iota append cap clear close complex copy delete imag len make max min new panic print println real recover",
    ),
    typeAfter: words("type"),
  },
  ts: {
    line: ["//"],
    block: cBlock,
    strings: [{ open: "`", close: "`", multi: true }, ...cStrings],
    keywords: words(
      "abstract as async await break case catch class const continue debugger declare default delete do else enum export extends finally for from function get if implements import in infer instanceof interface is keyof let namespace new of private protected public readonly return satisfies set static super switch this throw try type typeof var void while with yield",
    ),
    types: words("any bigint boolean never number object string symbol unknown"),
    constants: words("true false null undefined NaN Infinity console window document globalThis"),
    rules: [{ re: /@[A-Za-z_$][\w$.]*/y, kind: "meta" }],
    capsTypes: true,
    typeAfter: words("class interface type enum extends implements new"),
  },
  py: {
    line: ["#"],
    strings: [{ open: '"""', close: '"""', multi: true }, { open: "'''", close: "'''", multi: true }, ...cStrings],
    keywords: words(
      "and as assert async await break class continue def del elif else except finally for from global if import in is lambda match case nonlocal not or pass raise return try while with yield",
    ),
    constants: words(
      "True False None self cls print len range str int float list dict set tuple bool type isinstance super open enumerate zip map filter sorted any all",
    ),
    rules: [
      { re: /@[A-Za-z_][\w.]*/y, kind: "meta" },
      { re: /[rbfuRBFU]{1,2}(?=["'])/y, kind: "string" },
    ],
    capsTypes: true,
    typeAfter: words("class"),
  },
  rust: {
    line: ["//"],
    block: cBlock,
    strings: [{ open: '"', close: '"', multi: true }],
    keywords: words(
      "as async await break const continue crate dyn else enum extern fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait type unsafe use where while",
    ),
    types: words(
      "bool char f32 f64 i8 i16 i32 i64 i128 isize str u8 u16 u32 u64 u128 usize String Vec Option Result Box",
    ),
    constants: words("true false None Some Ok Err"),
    rules: [
      { re: /'[A-Za-z_]\w*(?!')/y, kind: "meta" }, // a lifetime, not a char
      { re: /'(?:\\.|[^\\'])'/y, kind: "string" },
      { re: /[A-Za-z_]\w*!/y, kind: "func" }, // a macro
      { re: /#!?\[[^\]]*\]/y, kind: "meta" },
    ],
    capsTypes: true,
    typeAfter: words("struct enum trait type impl for"),
  },
  c: {
    line: ["//"],
    block: cBlock,
    strings: cStrings,
    keywords: words(
      "auto break case catch class const constexpr continue default delete do else enum explicit export extern final for friend goto if inline namespace new noexcept operator override private protected public register return sizeof static struct switch template this throw try typedef typename union using virtual volatile while abstract extends implements import instanceof interface package super synchronized throws transient var val fun when is object companion data sealed suspend let guard func protocol extension init deinit internal fileprivate open lazy weak async await",
    ),
    types: words(
      "void bool char short int long float double signed unsigned size_t int8_t int16_t int32_t int64_t uint8_t uint16_t uint32_t uint64_t boolean byte String Int Long Double Float Bool Unit Any",
    ),
    constants: words("true false null nullptr NULL nil self this"),
    rules: [
      { re: /#\s*\w+/y, kind: "meta", lineStart: true },
      { re: /@[A-Za-z_][\w.]*/y, kind: "meta" },
    ],
    capsTypes: true,
    typeAfter: words("class struct enum interface new extends implements protocol extension object"),
  },
  ruby: {
    line: ["#"],
    strings: cStrings,
    keywords: words(
      "alias and begin break case class def defined? do else elsif end ensure for if in module next not or redo rescue retry return super then undef unless until when while yield",
    ),
    constants: words("true false nil self puts require require_relative attr_accessor attr_reader"),
    rules: [
      { re: /:[A-Za-z_]\w*[?!]?/y, kind: "constant" },
      { re: /@{1,2}[A-Za-z_]\w*/y, kind: "variable" },
    ],
    capsTypes: true,
  },
  sh: {
    line: ["#"],
    strings: [
      { open: '"', close: '"', multi: true },
      { open: "'", close: "'", multi: true, raw: true },
    ],
    keywords: words(
      "if then else elif fi for in do done while until case esac function return local export readonly set unset shift trap exit select time",
    ),
    constants: words(
      "echo printf cd ls cat grep sed awk find test read source eval exec true false mkdir rm cp mv chmod git make go bun npm",
    ),
    rules: [
      {
        re: /\$\{[^}\n]*\}|\$\(|\$[A-Za-z_]\w*|\$[0-9@#?$!*-]/y,
        kind: "variable",
      },
      { re: /\s--?[A-Za-z][\w-]*/y, kind: "attr" },
    ],
    noNumbers: false,
  },
  sql: {
    line: ["--"],
    block: cBlock,
    strings: [{ open: "'", close: "'" }],
    keywords: words(
      "select from where and or not insert into values update set delete create table alter drop index view join left right inner outer full on group by order having limit offset as distinct union all exists in is null like between case when then else end primary key foreign references default unique check constraint begin commit rollback with returning",
    ),
    types: words(
      "int integer bigint smallint text varchar char boolean bool date timestamp timestamptz numeric decimal real float double uuid json jsonb serial",
    ),
    constants: words("true false null count sum avg min max now coalesce"),
    caseless: true,
  },
  json: {
    strings: [{ open: '"', close: '"' }],
    constants: words("true false null"),
    keyStrings: true,
    rules: [{ re: /\/\/.*/y, kind: "comment" }],
  },
  yaml: {
    line: ["#"],
    strings: cStrings,
    constants: words("true false null yes no on off ~"),
    rules: [
      {
        re: /(\s*(?:-\s+)?)([^\s#:'"{}[\],][^#:]*?)(\s*:)(?=\s|$)/y,
        groups: ["", "property", ""],
        lineStart: true,
      },
      { re: /---|\.\.\./y, kind: "meta", lineStart: true },
      { re: /[&*][\w-]+/y, kind: "variable" },
      { re: /!![\w]+|![\w]+/y, kind: "type" },
    ],
    keyStrings: true,
    noIdents: true,
  },
  toml: {
    line: ["#", ";"],
    strings: [
      { open: '"""', close: '"""', multi: true },
      { open: "'''", close: "'''", multi: true, raw: true },
      ...cStrings,
    ],
    constants: words("true false"),
    rules: [
      {
        re: /(\s*)(\[\[?[^\]\n]+\]\]?)/y,
        groups: ["", "type"],
        lineStart: true,
      },
      {
        re: /(\s*)([\w.\-"']+)(\s*=)/y,
        groups: ["", "property", ""],
        lineStart: true,
      },
    ],
    noIdents: true,
  },
  css: {
    block: cBlock,
    strings: cStrings,
    rules: [
      { re: /@[\w-]+/y, kind: "keyword" },
      { re: /#[\da-fA-F]{3,8}\b/y, kind: "number" },
      {
        re: /([\w-]+)(\s*:)(?!:)(?=[^;{}]*[;}]|[^;{}]*$)/y,
        groups: ["property", ""],
      },
      { re: /!important/y, kind: "keyword" },
      { re: /[.#][\w-]+/y, kind: "tag" },
      { re: /::?[\w-]+/y, kind: "attr" },
      { re: /--[\w-]+/y, kind: "variable" },
      {
        re: /\d*\.?\d+(?:px|em|rem|%|vh|vw|dvh|s|ms|deg|fr|ch)?/y,
        kind: "number",
      },
    ],
    noNumbers: true,
    noIdents: true,
  },
  html: {
    block: [["<!--", "-->"]],
    strings: [],
    rules: [
      { re: /(<\/?)([A-Za-z][\w:.-]*)/y, groups: ["", "tag"] },
      { re: /(\s)([A-Za-z_:@][\w:.@-]*)(?==)/y, groups: ["", "attr"] },
      { re: /(=)("[^"]*"|'[^']*')/y, groups: ["", "string"] },
      { re: /<!DOCTYPE[^>]*>|<\?[^?]*\?>/iy, kind: "meta" },
      { re: /&[\w#]+;/y, kind: "constant" },
    ],
    noNumbers: true,
    noIdents: true,
  },
  md: {
    rules: [
      { re: /#{1,6}\s.*/y, kind: "keyword", lineStart: true },
      {
        re: /(\s*)([-*+]|\d+[.)])(?=\s)/y,
        groups: ["", "keyword"],
        lineStart: true,
      },
      { re: />.*/y, kind: "comment", lineStart: true },
      { re: /```.*|~~~.*/y, kind: "meta", lineStart: true },
      { re: /`[^`\n]+`/y, kind: "string" },
      { re: /\*\*[^*\n]+\*\*|__[^_\n]+__/y, kind: "type" },
      {
        re: /(\[)([^\]\n]+)(\]\()([^)\s]+)(\))/y,
        groups: ["", "func", "", "property", ""],
      },
      { re: /- \[[ xX]\]/y, kind: "constant" },
    ],
    noNumbers: true,
    noIdents: true,
  },
  make: {
    line: ["#"],
    strings: cStrings,
    keywords: words("ifeq ifneq ifdef ifndef else endif include define endef export override"),
    rules: [
      {
        re: /([\w./%$() -]+?)(::?)(?!=)/y,
        groups: ["func", ""],
        lineStart: true,
      },
      { re: /\$\(?[\w@<^*%]+\)?/y, kind: "variable" },
      {
        re: /([\w.-]+)(\s*[:+?]?=)/y,
        groups: ["property", ""],
        lineStart: true,
      },
    ],
  },
  docker: {
    line: ["#"],
    strings: cStrings,
    rules: [
      {
        re: /(\s*)(FROM|RUN|CMD|LABEL|EXPOSE|ENV|ADD|COPY|ENTRYPOINT|VOLUME|USER|WORKDIR|ARG|ONBUILD|STOPSIGNAL|HEALTHCHECK|SHELL|AS)\b/iy,
        groups: ["", "keyword"],
        lineStart: true,
      },
      { re: /\$\{[^}\n]*\}|\$[A-Za-z_]\w*/y, kind: "variable" },
    ],
  },
  diff: {
    rules: [
      { re: /\+.*/y, kind: "string", lineStart: true },
      { re: /-.*/y, kind: "keyword", lineStart: true },
      { re: /@@.*/y, kind: "meta", lineStart: true },
    ],
    noNumbers: true,
    noIdents: true,
  },
  conf: { line: ["#"], strings: cStrings },
};

const aliases: Record<string, string> = {
  golang: "go",
  "go.mod": "go",
  typescript: "ts",
  tsx: "ts",
  mts: "ts",
  cts: "ts",
  js: "ts",
  javascript: "ts",
  jsx: "ts",
  mjs: "ts",
  cjs: "ts",
  python: "py",
  pyi: "py",
  rs: "rust",
  h: "c",
  cc: "c",
  cpp: "c",
  hpp: "c",
  cxx: "c",
  "c++": "c",
  java: "c",
  kt: "c",
  kts: "c",
  kotlin: "c",
  swift: "c",
  cs: "c",
  csharp: "c",
  scala: "c",
  dart: "c",
  rb: "ruby",
  bash: "sh",
  zsh: "sh",
  shell: "sh",
  console: "sh",
  fish: "sh",
  jsonc: "json",
  json5: "json",
  yml: "yaml",
  ini: "toml",
  cfg: "toml",
  scss: "css",
  less: "css",
  xml: "html",
  svg: "html",
  htm: "html",
  vue: "html",
  markdown: "md",
  mdx: "md",
  makefile: "make",
  mk: "make",
  dockerfile: "docker",
  containerfile: "docker",
  patch: "diff",
  gitignore: "conf",
  dockerignore: "conf",
  env: "sh",
  editorconfig: "toml",
};

const compiled = new Map<string, Compiled>();

function compile(id: string): Compiled | null {
  const hit = compiled.get(id);
  if (hit) return hit;
  const d = defs[id];
  if (!d) return null;
  const fold = (xs?: string[]) => new Set((xs ?? []).map((x) => (d.caseless ? x.toLowerCase() : x)));
  const c: Compiled = {
    ...d,
    kw: fold(d.keywords),
    ty: fold(d.types),
    co: fold(d.constants),
    after: fold(d.typeAfter),
  };
  // Longest openers first, so """ wins over ".
  c.strings = [...(d.strings ?? [])].sort((a, b) => b.open.length - a.open.length);
  compiled.set(id, c);
  return c;
}

/** The language of a file name or a Markdown fence's info string, or "" for none. */
export function languageOf(name: string): string {
  const base = (name.split("/").pop() ?? "").trim().toLowerCase();
  const word = base.split(/\s+/)[0] ?? "";
  if (defs[word]) return word;
  if (aliases[word]) return aliases[word] ?? "";
  if (/^(gnu)?makefile$/.test(word)) return "make";
  if (/^(docker|container)file/.test(word)) return "docker";
  if (word === "go.sum") return "";
  const ext = word.includes(".") ? (word.split(".").pop() ?? "") : "";
  if (ext === "lock") return "";
  if (defs[ext]) return ext;
  return aliases[ext] ?? (/^\.(bash|zsh)rc$|^\.profile$/.test(word) ? "sh" : "");
}

const identStart = /[A-Za-z_$À-￿]/;
const ident = /[A-Za-z_$À-￿][\w$À-￿]*/y;
const number = /(?:0[xX][\da-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|(?:\d[\d_]*)?\.?\d[\d_]*(?:[eE][+-]?\d+)?)[a-zA-Z]*/y;

/** Finds `close` in line from i, honoring backslash escapes; returns the index after it, or -1. */
function findClose(line: string, i: number, close: string, escape: boolean): number {
  for (let j = i; j < line.length; j++) {
    if (escape && line[j] === "\\") {
      j++;
      continue;
    }
    if (line.startsWith(close, j)) return j + close.length;
  }
  return -1;
}

/** Tokenizes one line, given what the previous line left open. */
export function tokenizeLine(
  lang: string,
  line: string,
  state: State | null,
): { pieces: Piece[]; state: State | null } {
  const L = compile(lang);
  if (!L) return { pieces: [["", line]], state: null };
  const out: Piece[] = [];
  const push = (k: Kind, t: string) => {
    if (!t) return;
    const last = out[out.length - 1];
    if (last && last[0] === k) last[1] += t;
    else out.push([k, t]);
  };
  let i = 0;
  if (state) {
    const end = findClose(line, 0, state.close, state.escape);
    if (end < 0) return { pieces: [[state.kind, line]], state };
    push(state.kind, line.slice(0, end));
    i = end;
  }
  let prevWord = "";
  scan: while (i < line.length) {
    for (const r of L.rules ?? []) {
      if (r.lineStart && line.slice(0, i).trim() !== "") continue;
      r.re.lastIndex = i;
      const m = r.re.exec(line);
      if (!m || !m[0]) continue;
      if (r.groups) r.groups.forEach((k, g) => push(k, m[g + 1] ?? ""));
      else push(r.kind ?? "", m[0]);
      i += m[0].length;
      continue scan;
    }
    for (const c of L.line ?? []) {
      if (line.startsWith(c, i)) {
        push("comment", line.slice(i));
        return { pieces: out, state: null };
      }
    }
    for (const [open, close] of L.block ?? []) {
      if (line.startsWith(open, i)) {
        const end = findClose(line, i + open.length, close, false);
        if (end < 0) {
          push("comment", line.slice(i));
          return {
            pieces: out,
            state: { close, kind: "comment", escape: false },
          };
        }
        push("comment", line.slice(i, end));
        i = end;
        continue scan;
      }
    }
    for (const s of L.strings ?? []) {
      if (!line.startsWith(s.open, i)) continue;
      const end = findClose(line, i + s.open.length, s.close, !s.raw);
      if (end < 0) {
        push("string", line.slice(i));
        return {
          pieces: out,
          state: s.multi ? { close: s.close, kind: "string", escape: !s.raw } : null,
        };
      }
      const isKey = L.keyStrings && /^\s*:/.test(line.slice(end));
      push(isKey ? "property" : "string", line.slice(i, end));
      i = end;
      continue scan;
    }
    const ch = line[i] ?? "";
    const prev = i > 0 ? (line[i - 1] ?? "") : "";
    if (!L.noNumbers && /[\d.]/.test(ch) && !/[\w$]/.test(prev)) {
      number.lastIndex = i;
      const m = number.exec(line);
      if (m && /\d/.test(m[0])) {
        push("number", m[0]);
        i += m[0].length;
        continue;
      }
    }
    if (!L.noIdents && identStart.test(ch)) {
      ident.lastIndex = i;
      const w = ident.exec(line)?.[0] ?? ch;
      const key = L.caseless ? w.toLowerCase() : w;
      const rest = line.slice(i + w.length);
      let k: Kind = "";
      if (L.kw.has(key)) k = "keyword";
      else if (L.ty.has(key)) k = "type";
      else if (L.co.has(key)) k = "constant";
      else if (L.after.has(prevWord)) k = "type";
      else if (/^\s*\(/.test(rest)) k = "func";
      else if (L.capsTypes && /^[A-Z][a-z0-9]\w*$/.test(w)) k = "type";
      else if (prev === "." && /^\s*[^(\w]/.test(rest || " ")) k = "property";
      push(k, w);
      prevWord = key;
      i += w.length;
      continue;
    }
    if (!/\s/.test(ch)) prevWord = /[\w]/.test(ch) ? prevWord : "";
    push("", ch);
    i++;
  }
  return { pieces: out, state: null };
}

/** Tokenizes lines in order, carrying state across them. */
export function tokenizeLines(lang: string, lines: string[]): Piece[][] {
  let state: State | null = null;
  return lines.map((l) => {
    const r = tokenizeLine(lang, l, state);
    state = r.state;
    return r.pieces;
  });
}

/** The pieces as text nodes and spans. */
export function renderPieces(pieces: Piece[]): DocumentFragment {
  const frag = document.createDocumentFragment();
  for (const [k, t] of pieces) {
    if (!k) {
      frag.append(t);
      continue;
    }
    const el = document.createElement("span");
    el.className = `hl-${k}`;
    el.textContent = t;
    frag.append(el);
  }
  return frag;
}
