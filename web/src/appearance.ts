// Terminal color schemes and code highlighting themes. Each one is a set of
// CSS variables put on <html>; "Match app" removes them, so the defaults in
// tokens.css follow the light or dark app theme. Each terminal scheme also
// makes a whole app theme.
import { blend, isDark, readable } from "./ansi.ts";
export interface TermScheme {
  id: string;
  label: string;
  bg: string;
  fg: string;
  /** The 16 ANSI colors: black, red, green, yellow, blue, magenta, cyan, white, then the bright ones. */
  ansi: string[];
}

export interface CodeTheme {
  id: string;
  label: string;
  bg: string;
  fg: string;
  comment: string;
  keyword: string;
  string: string;
  number: string;
  type: string;
  func: string;
  constant: string;
  property: string;
  tag: string;
  attr: string;
  meta: string;
  variable: string;
}

export const matchApp = "match";
export const noHighlight = "off";

export const termSchemes: TermScheme[] = [
  {
    id: "one-dark",
    label: "One Dark",
    bg: "#282c34",
    fg: "#abb2bf",
    ansi: ["#3f4451", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#d7dae0"].concat([
      "#4f5666",
      "#ff7b86",
      "#b1e18b",
      "#efb074",
      "#67cdff",
      "#e48bff",
      "#63d4e0",
      "#e6e6e6",
    ]),
  },
  {
    id: "dracula",
    label: "Dracula",
    bg: "#282a36",
    fg: "#f8f8f2",
    ansi: ["#21222c", "#ff5555", "#50fa7b", "#f1fa8c", "#bd93f9", "#ff79c6", "#8be9fd", "#f8f8f2"].concat([
      "#6272a4",
      "#ff6e6e",
      "#69ff94",
      "#ffffa5",
      "#d6acff",
      "#ff92df",
      "#a4ffff",
      "#ffffff",
    ]),
  },
  {
    id: "monokai",
    label: "Monokai",
    bg: "#272822",
    fg: "#f8f8f2",
    ansi: ["#272822", "#f92672", "#a6e22e", "#f4bf75", "#66d9ef", "#ae81ff", "#a1efe4", "#f8f8f2"].concat([
      "#75715e",
      "#f92672",
      "#a6e22e",
      "#f4bf75",
      "#66d9ef",
      "#ae81ff",
      "#a1efe4",
      "#f9f8f5",
    ]),
  },
  {
    id: "nord",
    label: "Nord",
    bg: "#2e3440",
    fg: "#d8dee9",
    ansi: ["#3b4252", "#bf616a", "#a3be8c", "#ebcb8b", "#81a1c1", "#b48ead", "#88c0d0", "#e5e9f0"].concat([
      "#4c566a",
      "#bf616a",
      "#a3be8c",
      "#ebcb8b",
      "#81a1c1",
      "#b48ead",
      "#8fbcbb",
      "#eceff4",
    ]),
  },
  {
    id: "gruvbox-dark",
    label: "Gruvbox Dark",
    bg: "#282828",
    fg: "#ebdbb2",
    ansi: ["#282828", "#cc241d", "#98971a", "#d79921", "#458588", "#b16286", "#689d6a", "#a89984"].concat([
      "#928374",
      "#fb4934",
      "#b8bb26",
      "#fabd2f",
      "#83a598",
      "#d3869b",
      "#8ec07c",
      "#ebdbb2",
    ]),
  },
  {
    id: "tokyo-night",
    label: "Tokyo Night",
    bg: "#1a1b26",
    fg: "#c0caf5",
    ansi: ["#15161e", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6"].concat([
      "#414868",
      "#f7768e",
      "#9ece6a",
      "#e0af68",
      "#7aa2f7",
      "#bb9af7",
      "#7dcfff",
      "#c0caf5",
    ]),
  },
  {
    id: "ayu-dark",
    label: "Ayu Dark",
    bg: "#0b0e14",
    fg: "#bfbdb6",
    ansi: ["#11151c", "#ea6c73", "#7fd962", "#f9af4f", "#53bdfa", "#cda1fa", "#90e1c6", "#c7c7c7"].concat([
      "#686868",
      "#f07178",
      "#aad94c",
      "#ffb454",
      "#59c2ff",
      "#d2a6ff",
      "#95e6cb",
      "#ffffff",
    ]),
  },
  {
    id: "ayu-mirage",
    label: "Ayu Mirage",
    bg: "#1f2430",
    fg: "#cccac2",
    ansi: ["#171b24", "#ed8274", "#87d96c", "#facc6e", "#6dcbfa", "#dabafa", "#90e1c6", "#c7c7c7"].concat([
      "#686868",
      "#f28779",
      "#d5ff80",
      "#ffd173",
      "#73d0ff",
      "#dfbfff",
      "#95e6cb",
      "#ffffff",
    ]),
  },
  {
    id: "catppuccin-mocha",
    label: "Catppuccin Mocha",
    bg: "#1e1e2e",
    fg: "#cdd6f4",
    ansi: ["#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de"].concat([
      "#585b70",
      "#f38ba8",
      "#a6e3a1",
      "#f9e2af",
      "#89b4fa",
      "#f5c2e7",
      "#94e2d5",
      "#a6adc8",
    ]),
  },
  {
    id: "solarized-dark",
    label: "Solarized Dark",
    bg: "#002b36",
    fg: "#839496",
    ansi: ["#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5"].concat([
      "#002b36",
      "#cb4b16",
      "#586e75",
      "#657b83",
      "#839496",
      "#6c71c4",
      "#93a1a1",
      "#fdf6e3",
    ]),
  },
  {
    id: "github-dark",
    label: "GitHub Dark",
    bg: "#0d1117",
    fg: "#e6edf3",
    ansi: ["#484f58", "#ff7b72", "#3fb950", "#d29922", "#58a6ff", "#bc8cff", "#39c5cf", "#b1bac4"].concat([
      "#6e7681",
      "#ffa198",
      "#56d364",
      "#e3b341",
      "#79c0ff",
      "#d2a8ff",
      "#56d4dd",
      "#ffffff",
    ]),
  },
  {
    id: "solarized-light",
    label: "Solarized Light",
    bg: "#fdf6e3",
    fg: "#657b83",
    ansi: ["#073642", "#dc322f", "#859900", "#b58900", "#268bd2", "#d33682", "#2aa198", "#eee8d5"].concat([
      "#002b36",
      "#cb4b16",
      "#586e75",
      "#657b83",
      "#839496",
      "#6c71c4",
      "#93a1a1",
      "#fdf6e3",
    ]),
  },
  {
    id: "catppuccin-latte",
    label: "Catppuccin Latte",
    bg: "#eff1f5",
    fg: "#4c4f69",
    ansi: ["#5c5f77", "#d20f39", "#40a02b", "#df8e1d", "#1e66f5", "#ea76cb", "#179299", "#acb0be"].concat([
      "#6c6f85",
      "#d20f39",
      "#40a02b",
      "#df8e1d",
      "#1e66f5",
      "#ea76cb",
      "#179299",
      "#bcc0cc",
    ]),
  },
  {
    id: "ayu-light",
    label: "Ayu Light",
    bg: "#f8f9fa",
    fg: "#5c6166",
    ansi: ["#000000", "#ea6c6d", "#6cbf43", "#eca944", "#3199e1", "#9e75c7", "#46ba94", "#bababa"].concat([
      "#686868",
      "#f07171",
      "#86b300",
      "#f2ae49",
      "#399ee6",
      "#a37acc",
      "#4cbf99",
      "#d1d1d1",
    ]),
  },
  {
    id: "github-light",
    label: "GitHub Light",
    bg: "#ffffff",
    fg: "#1f2328",
    ansi: ["#24292f", "#cf222e", "#116329", "#4d2d00", "#0969da", "#8250df", "#1b7c83", "#6e7781"].concat([
      "#57606a",
      "#a40e26",
      "#1a7f37",
      "#633c01",
      "#218bff",
      "#a475f9",
      "#3192aa",
      "#8c959f",
    ]),
  },
];

export const codeThemes: CodeTheme[] = [
  {
    id: "github-light",
    label: "GitHub Light",
    bg: "#ffffff",
    fg: "#1f2328",
    comment: "#6e7781",
    keyword: "#cf222e",
    string: "#0a3069",
    number: "#0550ae",
    type: "#953800",
    func: "#8250df",
    constant: "#0550ae",
    property: "#0550ae",
    tag: "#116329",
    attr: "#0550ae",
    meta: "#8250df",
    variable: "#953800",
  },
  {
    id: "github-dark",
    label: "GitHub Dark",
    bg: "#0d1117",
    fg: "#e6edf3",
    comment: "#8b949e",
    keyword: "#ff7b72",
    string: "#a5d6ff",
    number: "#79c0ff",
    type: "#ffa657",
    func: "#d2a8ff",
    constant: "#79c0ff",
    property: "#79c0ff",
    tag: "#7ee787",
    attr: "#79c0ff",
    meta: "#d2a8ff",
    variable: "#ffa657",
  },
  {
    id: "one-dark",
    label: "One Dark",
    bg: "#282c34",
    fg: "#abb2bf",
    comment: "#7f848e",
    keyword: "#c678dd",
    string: "#98c379",
    number: "#d19a66",
    type: "#e5c07b",
    func: "#61afef",
    constant: "#d19a66",
    property: "#e06c75",
    tag: "#e06c75",
    attr: "#d19a66",
    meta: "#c678dd",
    variable: "#e06c75",
  },
  {
    id: "dracula",
    label: "Dracula",
    bg: "#282a36",
    fg: "#f8f8f2",
    comment: "#6272a4",
    keyword: "#ff79c6",
    string: "#f1fa8c",
    number: "#bd93f9",
    type: "#8be9fd",
    func: "#50fa7b",
    constant: "#bd93f9",
    property: "#ffb86c",
    tag: "#ff79c6",
    attr: "#50fa7b",
    meta: "#ff79c6",
    variable: "#ffb86c",
  },
  {
    id: "monokai",
    label: "Monokai",
    bg: "#272822",
    fg: "#f8f8f2",
    comment: "#75715e",
    keyword: "#f92672",
    string: "#e6db74",
    number: "#ae81ff",
    type: "#66d9ef",
    func: "#a6e22e",
    constant: "#ae81ff",
    property: "#fd971f",
    tag: "#f92672",
    attr: "#a6e22e",
    meta: "#f92672",
    variable: "#fd971f",
  },
  {
    id: "nord",
    label: "Nord",
    bg: "#2e3440",
    fg: "#d8dee9",
    comment: "#616e88",
    keyword: "#81a1c1",
    string: "#a3be8c",
    number: "#b48ead",
    type: "#8fbcbb",
    func: "#88c0d0",
    constant: "#81a1c1",
    property: "#8fbcbb",
    tag: "#81a1c1",
    attr: "#8fbcbb",
    meta: "#5e81ac",
    variable: "#d8dee9",
  },
  {
    id: "gruvbox-dark",
    label: "Gruvbox Dark",
    bg: "#282828",
    fg: "#ebdbb2",
    comment: "#928374",
    keyword: "#fb4934",
    string: "#b8bb26",
    number: "#d3869b",
    type: "#fabd2f",
    func: "#8ec07c",
    constant: "#d3869b",
    property: "#83a598",
    tag: "#8ec07c",
    attr: "#fabd2f",
    meta: "#fe8019",
    variable: "#83a598",
  },
  {
    id: "tokyo-night",
    label: "Tokyo Night",
    bg: "#1a1b26",
    fg: "#a9b1d6",
    comment: "#565f89",
    keyword: "#bb9af7",
    string: "#9ece6a",
    number: "#ff9e64",
    type: "#2ac3de",
    func: "#7aa2f7",
    constant: "#ff9e64",
    property: "#73daca",
    tag: "#f7768e",
    attr: "#bb9af7",
    meta: "#89ddff",
    variable: "#e0af68",
  },
  {
    id: "ayu-dark",
    label: "Ayu Dark",
    bg: "#0b0e14",
    fg: "#bfbdb6",
    comment: "#646a72",
    keyword: "#ff8f40",
    string: "#aad94c",
    number: "#d2a6ff",
    type: "#59c2ff",
    func: "#ffb454",
    constant: "#d2a6ff",
    property: "#39bae6",
    tag: "#39bae6",
    attr: "#ffb454",
    meta: "#e6b673",
    variable: "#f07178",
  },
  {
    id: "ayu-mirage",
    label: "Ayu Mirage",
    bg: "#1f2430",
    fg: "#cccac2",
    comment: "#6c7a8b",
    keyword: "#ffad66",
    string: "#d5ff80",
    number: "#dfbfff",
    type: "#73d0ff",
    func: "#ffd173",
    constant: "#dfbfff",
    property: "#5ccfe6",
    tag: "#5ccfe6",
    attr: "#ffd173",
    meta: "#ffdfb3",
    variable: "#f28779",
  },
  {
    id: "catppuccin-mocha",
    label: "Catppuccin Mocha",
    bg: "#1e1e2e",
    fg: "#cdd6f4",
    comment: "#6c7086",
    keyword: "#cba6f7",
    string: "#a6e3a1",
    number: "#fab387",
    type: "#f9e2af",
    func: "#89b4fa",
    constant: "#fab387",
    property: "#89dceb",
    tag: "#89b4fa",
    attr: "#f9e2af",
    meta: "#f5c2e7",
    variable: "#f38ba8",
  },
  {
    id: "catppuccin-latte",
    label: "Catppuccin Latte",
    bg: "#eff1f5",
    fg: "#4c4f69",
    comment: "#9ca0b0",
    keyword: "#8839ef",
    string: "#40a02b",
    number: "#fe640b",
    type: "#df8e1d",
    func: "#1e66f5",
    constant: "#fe640b",
    property: "#04a5e5",
    tag: "#1e66f5",
    attr: "#df8e1d",
    meta: "#ea76cb",
    variable: "#d20f39",
  },
  {
    id: "ayu-light",
    label: "Ayu Light",
    bg: "#fcfcfc",
    fg: "#5c6166",
    comment: "#adafb2",
    keyword: "#fa8d3e",
    string: "#86b300",
    number: "#a37acc",
    type: "#399ee6",
    func: "#f2ae49",
    constant: "#a37acc",
    property: "#55b4d4",
    tag: "#55b4d4",
    attr: "#f2ae49",
    meta: "#e59645",
    variable: "#f07171",
  },
  {
    id: "solarized-light",
    label: "Solarized Light",
    bg: "#fdf6e3",
    fg: "#657b83",
    comment: "#93a1a1",
    keyword: "#859900",
    string: "#2aa198",
    number: "#d33682",
    type: "#b58900",
    func: "#268bd2",
    constant: "#cb4b16",
    property: "#268bd2",
    tag: "#268bd2",
    attr: "#b58900",
    meta: "#cb4b16",
    variable: "#268bd2",
  },
  {
    id: "solarized-dark",
    label: "Solarized Dark",
    bg: "#002b36",
    fg: "#839496",
    comment: "#586e75",
    keyword: "#859900",
    string: "#2aa198",
    number: "#d33682",
    type: "#b58900",
    func: "#268bd2",
    constant: "#cb4b16",
    property: "#268bd2",
    tag: "#268bd2",
    attr: "#b58900",
    meta: "#cb4b16",
    variable: "#268bd2",
  },
];

const termKey = "herdsman.terminal-scheme";
const codeKey = "herdsman.code-theme";
const codeFields = [
  "comment",
  "keyword",
  "string",
  "number",
  "type",
  "func",
  "constant",
  "property",
  "tag",
  "attr",
  "meta",
  "variable",
] as const;

function read(key: string): string {
  try {
    return localStorage.getItem(key) ?? matchApp;
  } catch {
    return matchApp; // storage can be blocked
  }
}

function write(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Not remembered, still applied.
  }
}

export function loadTermScheme(): string {
  const id = read(termKey);
  return termSchemes.some((s) => s.id === id) ? id : matchApp;
}

export function loadCodeTheme(): string {
  const id = read(codeKey);
  return id === noHighlight || codeThemes.some((t) => t.id === id) ? id : matchApp;
}

/** The variables of a terminal scheme, for <html> or for a preview. */
export function termVars(id: string): Record<string, string> {
  const s = termSchemes.find((x) => x.id === id);
  if (!s) return {};
  const vars: Record<string, string> = { "--t-bg": s.bg, "--t-fg": s.fg };
  s.ansi.forEach((c, i) => {
    vars[`--t${i}`] = c;
  });
  return vars;
}

export function codeVars(id: string): Record<string, string> {
  const t = codeThemes.find((x) => x.id === id);
  if (!t) return {};
  const vars: Record<string, string> = {
    "--code-bg": t.bg,
    "--code-fg": t.fg,
    "--code-ln": t.comment,
  };
  for (const f of codeFields) vars[`--hl-${f}`] = t[f];
  return vars;
}

const applied = { term: new Set<string>(), code: new Set<string>() };

function put(kind: "term" | "code", vars: Record<string, string>): void {
  const st = document.documentElement.style;
  for (const k of applied[kind]) st.removeProperty(k);
  applied[kind] = new Set(Object.keys(vars));
  for (const [k, v] of Object.entries(vars)) st.setProperty(k, v);
}

export function applyTermScheme(id: string, remember = true): void {
  put("term", termVars(id));
  if (remember) write(termKey, id);
}

export function applyCodeTheme(id: string, remember = true): void {
  put("code", codeVars(id));
  if (id === noHighlight) document.documentElement.dataset["code"] = "off";
  else delete document.documentElement.dataset["code"];
  if (remember) write(codeKey, id);
}

/** Highlighting runs unless the code theme is "No highlighting". */
export function highlighting(): boolean {
  return document.documentElement.dataset["code"] !== "off";
}

/** The app themes made from the terminal schemes, besides herdsman's own Light and Dark. */
export const appThemes = termSchemes.map(({ id, label }) => ({ id, label }));

/**
 * The app's variables for a scheme: its background and text for the page,
 * with surfaces and borders between them; its ANSI colors, made readable on
 * its background, for the accent and the statuses; its own colors for the
 * terminal and the code theme of the same name for highlighting, so "Match
 * app" follows it. Null for an unknown id.
 */
export function appVars(id: string): Record<string, string> | null {
  const s = termSchemes.find((x) => x.id === id);
  if (!s) return null;
  const { bg, fg, ansi } = s;
  const dark = isDark(bg);
  const tone = (c: string | undefined, min = 3) => readable(c ?? fg, bg, min);
  const soft = (c: string) => blend(bg, c, dark ? 0.18 : 0.12);
  const accent = tone(ansi[2]);
  const danger = tone(ansi[1]);
  const working = tone(ansi[4]);
  const blocked = tone(ansi[3]);
  const muted = tone(blend(fg, bg, 0.3), 4.5);
  const faint = tone(blend(fg, bg, 0.5));
  const vars: Record<string, string> = {
    "--bg": blend(bg, "#000000", dark ? 0.22 : 0.035),
    "--surface": bg,
    "--surface-2": blend(bg, fg, 0.05),
    "--surface-3": blend(bg, fg, 0.09),
    "--border": blend(bg, fg, 0.12),
    "--border-strong": blend(bg, fg, 0.22),
    "--text": tone(fg, 4.5),
    "--muted": muted,
    "--faint": faint,
    "--accent": accent,
    "--accent-soft": soft(accent),
    "--accent-ink": dark ? bg : "#ffffff",
    "--danger": danger,
    "--danger-soft": soft(danger),
    "--tail-bg": blend(bg, "#000000", dark ? 0.12 : 0.015),
    "--s-working": working,
    "--s-working-soft": soft(working),
    "--s-idle": muted,
    "--s-idle-soft": blend(bg, fg, 0.07),
    "--s-blocked": blocked,
    "--s-blocked-soft": soft(blocked),
    "--s-done": accent,
    "--s-done-soft": soft(accent),
    "--s-unknown": faint,
    "--s-unknown-soft": blend(bg, fg, 0.05),
    "--s-gone": danger,
    "--s-gone-soft": soft(danger),
    "--shadow": dark
      ? "0 1px 2px rgb(0 0 0 / 30%), 0 4px 16px rgb(0 0 0 / 25%)"
      : "0 1px 2px rgb(20 22 18 / 6%), 0 4px 16px rgb(20 22 18 / 6%)",
    "--shadow-lg": dark ? "0 12px 48px rgb(0 0 0 / 55%)" : "0 12px 48px rgb(20 22 18 / 18%)",
    "color-scheme": dark ? "dark" : "light",
  };
  ansi.forEach((c, i) => {
    vars[`--t${i}`] = c;
  });
  const code = codeThemes.find((x) => x.id === id);
  if (code) {
    vars["--code-ln"] = code.comment;
    for (const f of codeFields) vars[`--hl-${f}`] = code[f];
  }
  return vars;
}

/**
 * Writes a scheme's app theme as a rule for its data-theme, in a style
 * element after tokens.css. A rule, not inline variables: a terminal scheme
 * or code theme the user picked sits inline on <html> and must still win.
 * Returns false, writing nothing, for an unknown id.
 */
export function installAppTheme(id: string): boolean {
  let el = document.getElementById("app-theme");
  if (!el) {
    el = document.createElement("style");
    el.id = "app-theme";
    document.head.append(el);
  }
  const vars = appVars(id);
  const sel = `:root[data-theme="${id}"]`;
  el.textContent = vars
    ? `${sel},\n${sel} .match-app {\n${Object.entries(vars)
        .map(([k, v]) => `  ${k}: ${v};`)
        .join("\n")}\n}\n`
    : "";
  return vars !== null;
}
