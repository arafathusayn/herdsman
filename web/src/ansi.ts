// Turns terminal text with SGR color codes into styled runs, then into DOM
// spans. Text only ever becomes text nodes; any escape sequence other than
// SGR is dropped. The 16 base colors are CSS variables (--t0…--t15), so a
// terminal color scheme can remap them the way a real terminal does; 256-color
// and truecolor codes keep their exact colors.

export type Color = number | string; // a palette index 0-255, or "#rrggbb"

export interface Style {
  fg?: Color;
  bg?: Color;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
  inverse?: boolean;
  strike?: boolean;
  hidden?: boolean;
}

export interface Run {
  text: string;
  style: Style;
}

// SGR (ESC [ … m), any other CSI, OSC (ESC ] … BEL or ST), any other escape
// (ESC, intermediate bytes, a final byte: charset switches and the like), or a
// lone ESC.
const escapes = /\x1b(?:\[([0-9;:<=>?]*)[ -\/]*([@-~])|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[ -\/]*[0-~]|$)/g;
// Control characters other than tab and newline (BEL, backspace, CR …).
const controls = /[\x00-\x08\x0b-\x1f\x7f]/g;

function hex(n: number): string {
  return n.toString(16).padStart(2, "0");
}

/** The exact color of a 256-color index from 16 up: the 6×6×6 cube, then the grays. */
export function xterm256(i: number): string {
  if (i >= 232) {
    const v = 8 + (i - 232) * 10;
    return `#${hex(v)}${hex(v)}${hex(v)}`;
  }
  const c = i - 16;
  const step = (v: number) => (v === 0 ? 0 : 55 + v * 40);
  return `#${hex(step(Math.floor(c / 36)))}${hex(step(Math.floor(c / 6) % 6))}${hex(step(c % 6))}`;
}

/** Reads an extended color (38/48;5;n or 38/48;2;r;g;b) at p[i]; returns it and how many params it used. */
function extended(p: number[], i: number): [Color | undefined, number] {
  if (p[i + 1] === 5) {
    const n = p[i + 2];
    return [n !== undefined && n >= 0 && n <= 255 ? n : undefined, 3];
  }
  if (p[i + 1] === 2) {
    const [r, g, b] = [p[i + 2], p[i + 3], p[i + 4]];
    if (r === undefined || g === undefined || b === undefined) return [undefined, 5];
    const clamp = (v: number) => Math.max(0, Math.min(255, v));
    return [`#${hex(clamp(r))}${hex(clamp(g))}${hex(clamp(b))}`, 5];
  }
  return [undefined, 1];
}

function apply(style: Style, params: string): Style {
  // "38:2::r:g:b" (colon form) reads like the semicolon form without the color space id.
  const p =
    params === ""
      ? [0]
      : params
          .replace(/:+/g, ";")
          .split(";")
          .map((x) => (x === "" ? 0 : Number(x)));
  const s: Style = { ...style };
  for (let i = 0; i < p.length; i++) {
    const n = p[i] ?? 0;
    if (n === 0) {
      for (const k of Object.keys(s)) delete s[k as keyof Style];
    } else if (n === 1) s.bold = true;
    else if (n === 2) s.dim = true;
    else if (n === 3) s.italic = true;
    else if (n === 4) s.underline = true;
    else if (n === 7) s.inverse = true;
    else if (n === 8) s.hidden = true;
    else if (n === 9) s.strike = true;
    else if (n === 21 || n === 22) {
      delete s.bold;
      delete s.dim;
    } else if (n === 23) delete s.italic;
    else if (n === 24) delete s.underline;
    else if (n === 27) delete s.inverse;
    else if (n === 28) delete s.hidden;
    else if (n === 29) delete s.strike;
    else if (n >= 30 && n <= 37) s.fg = n - 30;
    else if (n >= 90 && n <= 97) s.fg = n - 90 + 8;
    else if (n === 39) delete s.fg;
    else if (n >= 40 && n <= 47) s.bg = n - 40;
    else if (n >= 100 && n <= 107) s.bg = n - 100 + 8;
    else if (n === 49) delete s.bg;
    else if (n === 38 || n === 48) {
      const [c, used] = extended(p, i);
      if (c !== undefined) {
        if (n === 38) s.fg = c;
        else s.bg = c;
      }
      i += used - 1;
    }
  }
  return s;
}

/** Splits text into runs of one style each. Other escapes and control characters are dropped. */
export function parseAnsi(text: string): Run[] {
  const runs: Run[] = [];
  let style: Style = {};
  let last = 0;
  const push = (t: string) => {
    t = t.replace(controls, "");
    if (!t) return;
    const prev = runs[runs.length - 1];
    if (prev && prev.style === style) prev.text += t;
    else runs.push({ text: t, style });
  };
  for (const m of text.matchAll(escapes)) {
    push(text.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[2] === "m") style = apply(style, m[1] ?? "");
  }
  push(text.slice(last));
  return runs;
}

/** The terminal's resolved colors, each "#rrggbb": default text, background and the 16 base colors. */
export interface Palette {
  fg: string;
  bg: string;
  ansi: string[];
}

type RGB = [number, number, number];

function rgbOf(c: string): RGB | null {
  const s = c.trim();
  let m = /^#([\da-f]{3})$/i.exec(s);
  if (m) {
    const [r, g, b] = [...(m[1] ?? "")].map((x) => parseInt(x + x, 16));
    return [r ?? 0, g ?? 0, b ?? 0];
  }
  m = /^#([\da-f]{6})/i.exec(s);
  if (m) {
    const v = m[1] ?? "000000";
    return [parseInt(v.slice(0, 2), 16), parseInt(v.slice(2, 4), 16), parseInt(v.slice(4, 6), 16)];
  }
  const f = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/i.exec(s);
  return f ? [Number(f[1]), Number(f[2]), Number(f[3])] : null;
}

function toHex([r, g, b]: RGB): string {
  return `#${hex(Math.round(r))}${hex(Math.round(g))}${hex(Math.round(b))}`;
}

function mix(a: RGB, b: RGB, t: number): RGB {
  return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t];
}

/** WCAG relative luminance. */
function luminance([r, g, b]: RGB): number {
  const ch = (v: number) => {
    const x = v / 255;
    return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b);
}

/** The color t of the way from a to b, as #rrggbb. */
export function blend(a: string, b: string, t: number): string {
  const x = rgbOf(a);
  const y = rgbOf(b);
  return x && y ? toHex(mix(x, y, t)) : a;
}

/** Whether a background is dark enough for light text. */
export function isDark(c: string): boolean {
  const x = rgbOf(c);
  return x ? luminance(x) < 0.18 : false;
}

/** WCAG contrast ratio of two colors, from 1 to 21. */
export function contrast(a: string, b: string): number {
  const x = rgbOf(a);
  const y = rgbOf(b);
  if (!x || !y) return 21;
  const hi = Math.max(luminance(x), luminance(y));
  const lo = Math.min(luminance(x), luminance(y));
  return (hi + 0.05) / (lo + 0.05);
}

/**
 * A text color that reads on bg: fg itself when it has at least min
 * contrast, else fg moved toward black (on a light background) or white (on
 * a dark one) just far enough. Output colored for a dark terminal stays
 * legible on a light one, and the other way round.
 */
export function readable(fg: string, bg: string, min: number): string {
  const f = rgbOf(fg);
  const b = rgbOf(bg);
  if (!f || !b || contrast(fg, bg) >= min) return fg;
  const toward: RGB = luminance(b) > 0.18 ? [0, 0, 0] : [255, 255, 255];
  for (let t = 0.05; t < 1; t += 0.05) {
    const c = toHex(mix(f, toward, t));
    if (contrast(c, bg) >= min) return c;
  }
  return toHex(toward);
}

/** The terminal colors in effect on an element: its scheme's, or Match app's. */
export function readPalette(el: Element): Palette {
  const cs = getComputedStyle(el);
  const get = (name: string, fallback: string) => {
    const rgb = rgbOf(cs.getPropertyValue(name));
    return rgb ? toHex(rgb) : fallback;
  };
  const fg = get("--t-fg", toHex(rgbOf(cs.color) ?? [0, 0, 0]));
  const bg = get("--t-bg", "#ffffff");
  return {
    fg,
    bg,
    ansi: Array.from({ length: 16 }, (_, i) => get(`--t${i}`, fg)),
  };
}

function resolve(c: Color, pal: Palette): string {
  if (typeof c === "string") return c;
  return c < 16 ? (pal.ansi[c] ?? pal.fg) : xterm256(c);
}

/** Minimum contrast of terminal text; dimmed text may go lower, as in a terminal. */
const minContrast = 4.5;
const minDimContrast = 3;

/** Builds the runs as text nodes and spans in the palette's colors, every one legible. */
export function renderAnsi(text: string, pal: Palette): DocumentFragment {
  const frag = document.createDocumentFragment();
  for (const r of parseAnsi(text)) {
    const s = r.style;
    if (!Object.keys(s).length) {
      frag.append(r.text);
      continue;
    }
    const el = document.createElement("span");
    el.textContent = r.text;
    let fg = s.fg === undefined ? pal.fg : resolve(s.fg, pal);
    let bg = s.bg === undefined ? undefined : resolve(s.bg, pal);
    if (s.inverse) [fg, bg] = [bg ?? pal.bg, fg];
    const under = bg ?? pal.bg;
    const f = rgbOf(fg);
    const u = rgbOf(under);
    if (s.dim && f && u) fg = toHex(mix(f, u, 0.4)); // dim the way a terminal does, toward the background
    el.style.color = readable(fg, under, s.dim ? minDimContrast : minContrast);
    if (bg !== undefined) el.style.backgroundColor = bg;
    if (s.bold) el.classList.add("a-b");
    if (s.dim) el.classList.add("a-d");
    if (s.italic) el.classList.add("a-i");
    if (s.underline) el.classList.add("a-u");
    if (s.strike) el.classList.add("a-s");
    if (s.hidden) el.classList.add("a-h");
    frag.append(el);
  }
  return frag;
}
