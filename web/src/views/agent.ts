import { run } from "../actions.ts";
// The agent panel: a live pane tail, a composer and every direct action.
// It is built once per pane and only patched on state changes, so the
// composer keeps its text and the tail keeps its scroll position. Its
// terminal view shows the pane's screen and sends typing and the wheel
// straight to it.
import { arrowKeys, keySet, renamePane, sendOtherKeys, startAgent } from "../agent-actions.ts";
import { readPalette, renderAnsi } from "../ansi.ts";
import { act, AuthError, forgetPane, readPane } from "../api.ts";
import { icon, type IconNode, icons, mountElement } from "../dom.ts";
import {
  attr,
  child,
  cls,
  html,
  list,
  type ListNode,
  on,
  render,
  text,
  type ViewNodeOf,
} from "../herdsman-blocks-view.ts";
import { afterSend, type Member, roleName } from "../model.ts";
import { isMac } from "../palette.ts";
import { panelResizer } from "../resize.ts";
import { store } from "../store.ts";
import {
  type Input,
  InputQueue,
  keyInput,
  type KeyLike,
  loadZoom,
  nextZoom,
  saveZoom,
  typedText,
  WheelLines,
  zoomKey,
} from "../terminal.ts";
import type { Action } from "../types.ts";
import { pill, type PillNode, toast } from "../ui.ts";

type PaneView = "log" | "terminal";

const viewKey = "herdsman.pane-view";
const infoKey = "herdsman.pane-info";
/** The tail's font size in px, as in the CSS; the terminal view shrinks it to fit a wide pane, down to minFont. */
const defaultPlaceholder = "Message this agent…  (⌘/Ctrl+Enter to send)";
const baseFont = 12;
const minFont = 8;
/** The smallest the user's own zoom takes the terminal text. */
const zoomedMin = 6;
/** After input, the pane is read every fastPoll ms for fastFor ms. */
const fastPoll = 50;
const fastFor = 800;

function loadView(): PaneView {
  try {
    return localStorage.getItem(viewKey) === "terminal" ? "terminal" : "log";
  } catch {
    return "log";
  }
}

function loadInfoOpen(): boolean {
  try {
    return localStorage.getItem(infoKey) === "open";
  } catch {
    return false;
  }
}

// The elements the panel owns and works on directly: the screen, the typing
// field, the composer's box and mode, and the width probe. Each is made once,
// with its handlers, and placed in the panel's view through a child hole.
const Tail = html`<pre
  class="tail"
  tabindex="0"
  aria-label="Pane output"
  onscroll=${on<"scroll", HTMLPreElement>()}
  onmouseup=${on<"mouseup", HTMLPreElement>()}
  onkeydown=${on<"keydown", HTMLPreElement>()}
  onwheel=${on<"wheel", HTMLPreElement>()}
></pre>`;
const TermInput = html`<textarea
  class="term-input"
  rows="1"
  autocapitalize="off"
  autocomplete="off"
  autocorrect="off"
  aria-label="Type into the pane"
  spellcheck="false"
  onkeydown=${on<"keydown", HTMLTextAreaElement>()}
  oninput=${on<"input", HTMLTextAreaElement>()}
  onfocus=${on<"focus", HTMLTextAreaElement>()}
  onblur=${on<"blur", HTMLTextAreaElement>()}
></textarea>`;
const Want = html`<div class="panel-want" aria-hidden="true"></div>`;
const MessageBox = html`<textarea
  rows="3"
  placeholder=${attr()}
  spellcheck="true"
  aria-label="Message"
  onkeydown=${on<"keydown", HTMLTextAreaElement>()}
></textarea>`;
const Mode = html`<select aria-label="Send as" title="Prompt goes through the agent; Type sends raw text into the pane">
  <option value="prompt">Prompt</option>
  <option value="text">Type raw</option>
</select>`;
// What fitRows measures a row and a cell with.
const RowProbe = html`<span>Ag│█</span>`;
const CellProbe = html`<span>MMMMMMMMMMMMMMMMMMMM</span>`;
const probeCells = 20;

// The panel's view: its parts are rendered from the panel's state.
const Close = html`<button class="icon-btn" title="Close panel" aria-label="Close panel" onclick=${on<"click">()}>
  ${child<IconNode>()}
</button>`;
const Chip = html`<span class=${cls()}>${text()}</span>`;
const Title = html`<div class="panel-title">
  <h2>${text()}</h2>
  <div class="sub">
    ${child<PillNode>()}${child<ViewNodeOf<typeof Chip> | null>()}${child<ViewNodeOf<typeof Chip> | null>()}
  </div>
</div>`;
const Tool = html`<button class=${cls()} title=${attr()} aria-label=${attr()} onclick=${on<"click">()}>
  ${child<IconNode>()}
</button>`;
const Tools = html`<div class="panel-tools">${child<ListNode<ViewNodeOf<typeof Tool>, string>>()}</div>`;
const Head = html`<div class="panel-head">
  ${child<ViewNodeOf<typeof Close> | null>()}${child<ViewNodeOf<typeof Title> | null>()}${child<ViewNodeOf<
    typeof Tools
  > | null>()}
</div>`;

const ViewOption = html`<button
  type="button"
  title=${attr()}
  onclick=${on<"click">()}
  class=${cls()}
  aria-pressed=${attr<"true" | "false">()}
>
  ${child<IconNode>()}${text()}
</button>`;
const PaneBar = html`<div class="pane-bar">
  <button
    type="button"
    class="pane-info"
    title="Show or hide the pane's details"
    aria-controls=${attr()}
    aria-expanded=${attr<"true" | "false">()}
    onclick=${on<"click">()}
  >
    ${child<IconNode>()}<span class="pane-info-label">Details</span><span class="pane-info-line">${text()}</span>
  </button>
  <div class="term-hint">${text()}</div>
  <div class="view-switch" role="group" aria-label="Pane view">
    ${child<ListNode<ViewNodeOf<typeof ViewOption>, PaneView>>()}
  </div>
</div>`;

// A details row is a term and its value, siblings in the list: two items.
const Term = html`<dt>${text()}</dt>`;
const Value = html`<dd title=${attr()}>${text()}</dd>`;
type RowPart = ViewNodeOf<typeof Term> | ViewNodeOf<typeof Value>;
const Meta = html`<dl class="meta" id=${attr()} hidden=${attr<boolean>()}>${child<ListNode<RowPart, string>>()}</dl>`;

const TailWrap = html`<div class="tail-wrap">
  ${child<HTMLPreElement>()}
  <div class="tail-note">${text()}</div>
  ${child<HTMLTextAreaElement>()}
</div>`;

const Key = html`<button class="key" title=${attr()} onclick=${on<"click">()}>${text()}</button>`;
const ArrowKey = html`<button class="key arrow" title=${attr()} aria-label=${attr()} onclick=${on<"click">()}>
  ${text()}
</button>`;
const Keys = html`<div class="keys" role="group" aria-label="Send a key">
  ${child<IconNode>()}${child<ListNode<ViewNodeOf<typeof Key>, string>>()}
  <span class="key-group" role="group" aria-label="Arrow keys">
    ${child<ListNode<ViewNodeOf<typeof ArrowKey>, string>>()}
  </span>
  <button class="key" title="Send other key names, separated by spaces" onclick=${on<"click">()}>Other…</button>
</div>`;

const Composer = html`<form class="composer" onsubmit=${on<"submit">()}>
  ${child<HTMLTextAreaElement>()}
  <div class="composer-bar">
    <label class="select">${child<HTMLSelectElement>()}${child<IconNode>()}</label>
    <span class="grow"></span>
    <button class="btn primary" type="submit" disabled=${attr<boolean>()}>${child<IconNode>()}Send</button>
  </div>
</form>`;

const Panel = html`<aside class=${cls()} aria-label="Agent">
  ${child<ViewNodeOf<typeof Head>>()}${child<ViewNodeOf<typeof PaneBar>>()}${child<ViewNodeOf<typeof Meta>>()}${child<
    ViewNodeOf<typeof TailWrap>
  >()}${child<ViewNodeOf<typeof Keys>>()}${child<ViewNodeOf<typeof Composer>>()}${child<HTMLDivElement>()}${child<HTMLElement | null>()}
</aside>`;

const views: readonly { id: PaneView; glyph: readonly string[]; label: string; tip: string }[] = [
  { id: "log", glyph: icons.list, label: "Log", tip: "The pane's recent output, with a box to message the agent" },
  {
    id: "terminal",
    glyph: icons.terminal,
    label: "Terminal",
    tip: "The pane's screen: type straight into it, and the wheel scrolls it",
  },
];

// A checked narrowing, not a cast: the view's root is an HTML element.
const isHTML = (n: Node | null): n is HTMLElement => n !== null && n.nodeType === 1;

export class AgentPanel {
  readonly el: HTMLElement;
  // render() keeps the panel's view instance on box; the root moves to wherever main.ts puts el.
  private readonly box = document.createElement("div");
  /** Whether the details are open below the bar; the choice is kept. */
  private infoOpen = loadInfoOpen();
  /** The note under the tail: a cut tail or a read error. */
  private note = "";
  /** Whether the last text read was cut to its line limit. */
  private truncated = false;
  private view = loadView();
  private readonly wheel = new WheelLines();
  private readonly inputs = new InputQueue((i) => this.sendInput(i));
  private cell = { w: 0, h: 0 };
  /** The pane's width in cells, from herdr's layout; 0 when unknown. */
  private cols = 0;
  private font = baseFont;
  /** The user's zoom of the terminal view (⌘ + / − / 0), on top of the fit. */
  private zoom = loadZoom();
  /** A cell's width per px of font size. */
  private unit = 0;
  private readonly fit = new ResizeObserver(() => this.fitFont());
  private readonly tail: HTMLPreElement;
  /** Takes the typing of the terminal view, so IME, paste and phone keyboards work. */
  private readonly termInput: HTMLTextAreaElement;
  /** Measures the width the panel asks for, which the window may cut. */
  private readonly want = mountElement("div", Want, []);
  private readonly input: HTMLTextAreaElement;
  private readonly mode = mountElement("select", Mode, []);
  private readonly resizer: HTMLElement;
  private timer = 0;
  private stuck = true;
  private text = "";
  private palKey = "";
  private rowFitted = false;
  private member: Member | null = null;
  private busy = false;
  private dead = false;
  private reading = false;
  /** A poke came while a read was out: read again as soon as it ends. */
  private again = false;
  /** Until when to poll at fastPoll, after input. */
  private fastUntil = 0;

  constructor(
    readonly pane: string,
    private readonly kinds: () => string[],
  ) {
    this.tail = mountElement("pre", Tail, [
      () => {
        const t = this.tail;
        this.stuck = t.scrollHeight - t.scrollTop - t.clientHeight < 24;
      },
      // A click on the screen starts typing there, unless it selected text to copy.
      () => {
        if (this.view === "terminal" && (window.getSelection()?.isCollapsed ?? true)) {
          this.termInput.focus({ preventScroll: true });
        }
      },
      (e) => {
        if (this.zoomFor(e)) return;
        if (this.view === "terminal" && e.key === "Enter") {
          e.preventDefault();
          this.termInput.focus({ preventScroll: true });
        }
      },
      (e) => {
        // A sideways scroll moves along a screen wider than the panel.
        if (this.view !== "terminal" || e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY)) return;
        e.preventDefault();
        const lines = this.wheel.add(e.deltaY, e.deltaMode, this.text.split("\n").length);
        if (lines) this.inputs.push({ lines, ...this.cellAt(e.clientX, e.clientY) });
      },
    ]);
    const takeText = (): void => {
      const text = typedText(this.termInput.value);
      this.termInput.value = "";
      if (text && this.view === "terminal") this.inputs.push({ text });
    };
    this.termInput = mountElement("textarea", TermInput, [
      (e) => {
        if (this.view !== "terminal") return;
        if (this.zoomFor(e)) return;
        const i = keyInput(keyLike(e), isMac);
        if (!i) return;
        // Esc, Tab and the rest belong to the pane, not to the page.
        e.preventDefault();
        e.stopPropagation();
        this.inputs.push(i);
      },
      // An input event that is not an InputEvent is not composing either.
      (e) => {
        if (!("isComposing" in e && e.isComposing === true)) takeText();
      },
      () => this.paint(),
      () => this.paint(),
    ]);
    // No element has an oncompositionend property, so no on() hole can take
    // it: the listener goes on the element this panel owns (mountElement,
    // not render(), so event delegation would not change it).
    this.termInput.addEventListener("compositionend", takeText);
    this.input = mountElement("textarea", MessageBox, [
      defaultPlaceholder,
      (e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          void this.send();
        }
      },
    ]);
    render(this.box, this.describe(null));
    const root = this.box.firstChild;
    if (!isHTML(root)) throw new Error("AgentPanel: the view rendered no element");
    this.el = root;
    this.resizer = panelResizer(this.el);
    this.fit.observe(this.tail);
    this.fit.observe(this.want);
    this.applyView();
    this.start();
  }

  /** Renders the panel's view from its state; only what changed is written. */
  private paint(): void {
    render(this.box, this.describe(this.resizer));
  }

  private describe(resizer: HTMLElement | null): ViewNodeOf<typeof Panel> {
    const term = this.view === "terminal";
    const typing = term && document.activeElement === this.termInput;
    const zoom = this.zoom === 1 ? "" : ` · ${Math.round(this.zoom * 100)}%`;
    const m = this.member;
    const gone = m?.status === "gone";
    return Panel([
      ["panel", term && "terminal-view", typing && "typing"].filter(Boolean).join(" "),
      this.head(m),
      PaneBar([
        `meta-${this.pane}`,
        this.infoOpen ? "true" : "false",
        () => this.fold(!this.infoOpen),
        icon(icons.chevronDown),
        m ? [m.pane_id, m.tab ? m.tab.label || m.tab.tab_id : ""].filter(Boolean).join(" · ") : "",
        !term ? "" : typing ? `Typing into the pane${zoom} · click outside to stop` : `Click the screen to type${zoom}`,
        list(
          views,
          (v) => v.id,
          (v) =>
            ViewOption([
              v.tip,
              () => this.setView(v.id),
              v.id === this.view ? "active" : "",
              v.id === this.view ? "true" : "false",
              icon(v.glyph),
              v.label,
            ]),
        ),
      ]),
      Meta([`meta-${this.pane}`, !this.infoOpen, list(m ? details(m) : [], (p) => p.key, part)]),
      TailWrap([this.tail, this.note, this.termInput]),
      Keys([
        icon(icons.keyboard),
        list(
          keySet,
          (k) => k.label,
          (k) => Key([k.hint, () => this.sendKeys(k.keys), k.label]),
        ),
        list(
          arrowKeys,
          (k) => k.name,
          (k) => ArrowKey([`${k.name} arrow`, `${k.name} arrow`, () => this.sendKeys(k.keys), k.label]),
        ),
        () => void sendOtherKeys((keys) => this.sendKeys(keys)),
      ]),
      Composer([
        (e) => {
          e.preventDefault();
          void this.send();
        },
        this.input,
        this.mode,
        icon(icons.chevronDown),
        this.busy || gone,
        icon(icons.send),
      ]),
      this.want,
      resizer,
    ]);
  }

  private head(m: Member | null): ViewNodeOf<typeof Head> {
    if (m === null) return Head([null, null, null]);
    const a = m.agent;
    return Head([
      Close([() => store.go({ pane: "" }), icon(icons.close)]),
      Title([
        m.title,
        pill(m.status),
        m.role === "adjutant" || m.role === "orchestrator" ? Chip([`chip role-${m.role}`, roleName[m.role]]) : null,
        a?.agent ? Chip(["chip", a.display_agent ?? a.agent]) : null,
      ]),
      Tools([
        list(
          [
            tool(icons.focus, "Focus in herdr", () => run({ type: "focus", pane: this.pane })),
            tool(icons.play, "Start an agent in this pane", () => startAgent(this.pane, this.kinds())),
            tool(icons.tag, "Rename", () => this.member && renamePane(this.member)),
            tool(icons.split, "Split right", () => run({ type: "split", pane: this.pane, direction: "right" })),
            tool(icons.splitDown, "Split down", () => run({ type: "split", pane: this.pane, direction: "down" })),
            tool(icons.trash, "Delete pane", () => run({ type: "close", pane: this.pane }), true),
          ],
          (t) => t.label,
          (t) => Tool([t.danger ? "icon-btn danger" : "icon-btn", t.label, t.label, () => void t.fn(), icon(t.glyph)]),
        ),
      ]),
    ]);
  }

  // The details fold to one line in the bar, so the screen gets the room;
  // they open below the bar, and the choice is kept.
  private fold(open: boolean): void {
    this.infoOpen = open;
    try {
      localStorage.setItem(infoKey, open ? "open" : "closed");
    } catch {
      // The choice lasts for this page only.
    }
    this.paint();
  }

  private setView(v: PaneView): void {
    if (v === this.view) return;
    this.view = v;
    try {
      localStorage.setItem(viewKey, v);
    } catch {
      // The choice lasts for this page only.
    }
    // The other view's text is not on screen, so read it whole.
    forgetPane(this.pane);
    this.text = "";
    this.stuck = true;
    this.applyView();
    this.poke();
    if (v === "terminal") this.termInput.focus({ preventScroll: true });
  }

  private applyView(): void {
    const term = this.view === "terminal";
    this.tail.setAttribute("aria-label", term ? "Pane screen: press Enter to type into it" : "Pane output");
    // The typing field exists only for the terminal view: off it, nothing
    // can focus it, tab to it or type into the pane through it.
    if (!term) this.termInput.blur();
    this.termInput.disabled = !term || this.member?.status === "gone";
    this.paint();
    this.fitFont();
  }

  /**
   * ⌘ + / − / 0 (Ctrl elsewhere) while the terminal view has focus zoom its
   * text, not the page; the browser's own zoom is kept from acting. Returns
   * whether the key was a zoom key, so the caller sends nothing to the pane.
   */
  private zoomFor(e: KeyboardEvent): boolean {
    if (this.view !== "terminal") return false;
    const how = zoomKey(keyLike(e), isMac);
    if (!how) return false;
    e.preventDefault();
    e.stopPropagation();
    const z = nextZoom(this.zoom, how);
    if (z !== this.zoom) {
      this.zoom = z;
      saveZoom(z);
      this.fitFont();
      this.paint();
    }
    return true;
  }

  /** The 1-based terminal cell under a point of the screen. */
  private cellAt(x: number, y: number): { col: number; row: number } {
    const r = this.tail.getBoundingClientRect();
    const cs = getComputedStyle(this.tail);
    const left = x - r.left - Number.parseFloat(cs.paddingLeft) + this.tail.scrollLeft;
    const top = y - r.top - Number.parseFloat(cs.paddingTop) + this.tail.scrollTop;
    const col = this.cell.w ? Math.floor(left / this.cell.w) + 1 : 1;
    const row = this.cell.h ? Math.floor(top / this.cell.h) + 1 : 1;
    return { col: Math.max(1, col), row: Math.max(1, row) };
  }

  /**
   * Sends quick keys. In the terminal view they join the typing queue, so a
   * tapped Enter lands after the text typed before it, not ahead of it.
   */
  private sendKeys(keys: string[]): void {
    if (this.view === "terminal") this.inputs.push({ keys });
    else void run({ type: "keys", pane: this.pane, keys });
  }

  private async sendInput(i: Input): Promise<boolean> {
    const a: Action =
      "lines" in i ? { type: "scroll", pane: this.pane, ...i } : { type: "input", pane: this.pane, ...i };
    try {
      const res = await act(a);
      if (!res.ok) {
        toast(res.error ?? "The pane did not take the input.", "error");
        return false;
      }
      this.poke();
      return true;
    } catch (e) {
      if (e instanceof AuthError) store.set({ fatal: e.message });
      else toast(e instanceof Error ? e.message : String(e), "error");
      return false;
    }
  }

  /** hasAdjutant: the route has an adjutant, so the user talks through it. */
  update(m: Member, size: { cols: number } | null, hasAdjutant = false): void {
    this.member = m;
    this.input.placeholder =
      m.role === "adjutant"
        ? "Talk to the adjutant: it relays to the orchestrator…  (⌘/Ctrl+Enter to send)"
        : m.role === "orchestrator" && hasAdjutant
          ? "This route has an adjutant: talk through it. Text here bypasses the message channel."
          : defaultPlaceholder;
    if ((size?.cols ?? 0) !== this.cols) {
      this.cols = size?.cols ?? 0;
      this.fitFont();
    }
    const gone = m.status === "gone";
    this.input.disabled = gone;
    this.termInput.disabled = gone || this.view !== "terminal";
    this.paint();
  }

  destroy(): void {
    this.dead = true;
    this.fit.disconnect();
    clearTimeout(this.timer);
    forgetPane(this.pane);
  }

  focusComposer(): void {
    this.input.focus();
  }

  /**
   * Makes each row exactly as tall as the font's glyph box, the way a
   * terminal sizes its cells, so background colors and box-drawing lines of
   * neighboring rows touch instead of leaving a gap of leading between them.
   */
  private fitRows(): void {
    if (this.rowFitted || !this.tail.isConnected) return;
    const probe = mountElement("span", RowProbe, []);
    const wide = mountElement("span", CellProbe, []);
    this.tail.append(probe, wide);
    const height = probe.getBoundingClientRect().height;
    const width = wide.getBoundingClientRect().width / probeCells;
    probe.remove();
    wide.remove();
    if (height > 0) {
      this.tail.style.lineHeight = `${height}px`;
      this.cell = { w: width, h: height };
      this.unit = width / this.font;
      this.rowFitted = true;
    }
  }

  /**
   * Sizes the terminal view's font so the pane's whole width fits the panel,
   * the way herdr shows it, and again whenever the panel is resized. It fits
   * the width the panel asks for: when the window gives it less (page zoom,
   * a narrow window), the text keeps its size and the screen scrolls
   * sideways. The user's zoom then scales the fitted size; zoomed past the
   * fit, the screen scrolls sideways too. The log view keeps the normal size
   * and wraps instead.
   */
  private fitFont(): void {
    let font = baseFont;
    if (this.view === "terminal") {
      if (this.cols && this.unit) {
        const cs = getComputedStyle(this.tail);
        const cut = Math.max(0, this.want.offsetWidth - this.el.offsetWidth);
        const room =
          this.tail.clientWidth + cut - Number.parseFloat(cs.paddingLeft) - Number.parseFloat(cs.paddingRight) - 1;
        font = Math.min(baseFont, Math.max(minFont, Math.floor((room / (this.cols * this.unit)) * 10) / 10));
      }
      font = Math.max(zoomedMin, Math.round(font * this.zoom * 10) / 10);
    }
    if (font === this.font) return;
    this.font = font;
    this.tail.style.fontSize = `${font}px`;
    this.rowFitted = false;
    this.fitRows();
    if (this.stuck) this.tail.scrollTop = this.tail.scrollHeight;
  }

  /**
   * Reads the pane again soon, then keeps reading fast for a moment: the
   * agent redraws a little after it gets the keys or the wheel, and each
   * frame should show as it lands, not a whole poll later.
   */
  private poke(): void {
    this.fastUntil = performance.now() + fastFor;
    if (this.reading) {
      this.again = true;
      return;
    }
    clearTimeout(this.timer);
    this.timer = window.setTimeout(() => void this.tick(), 30);
  }

  private start(): void {
    void this.tick();
  }

  /** Reads and draws the pane; one read at a time, so frames never land out of order. */
  private async tick(): Promise<void> {
    if (this.dead || this.reading) return;
    this.reading = true;
    this.again = false;
    try {
      await this.read();
    } finally {
      this.reading = false;
    }
    if (this.dead) return;
    const term = this.view === "terminal";
    const delay = this.again ? 0 : performance.now() < this.fastUntil ? fastPoll : term ? 250 : 1000;
    clearTimeout(this.timer);
    this.timer = window.setTimeout(() => void this.tick(), delay);
  }

  private async read(): Promise<void> {
    const term = this.view === "terminal";
    if (!document.hidden && this.member?.status !== "gone") {
      try {
        const r = await readPane(this.pane, term);
        // The view can change while the read is out; its answer is for the other one.
        if (term !== (this.view === "terminal")) {
          this.again = true;
          return;
        }
        // Colors are fixed when drawn, so a new theme or scheme redraws the same text.
        const pal = readPalette(this.tail);
        const palKey = JSON.stringify(pal);
        if (r) this.truncated = !!r.truncated;
        // Any answer, a 304 too, means the pane reads again, so an old error note goes.
        this.setNote(this.truncated && !term ? "Showing the last 400 lines." : "");
        if (r || palKey !== this.palKey) {
          if (r) this.text = r.text;
          this.palKey = palKey;
          this.fitRows();
          this.fitFont();
          this.tail.replaceChildren(renderAnsi(this.text, pal));
          if (this.stuck) this.tail.scrollTop = this.tail.scrollHeight;
        }
      } catch (e) {
        if (e instanceof AuthError) store.set({ fatal: e.message });
        else this.setNote(e instanceof Error ? e.message : String(e));
      }
    }
  }

  private setNote(note: string): void {
    if (note === this.note) return;
    this.note = note;
    this.paint();
  }

  private async send(): Promise<void> {
    const text = this.input.value;
    if (!text.trim() || this.busy) return;
    this.busy = true;
    this.paint();
    const type = this.mode.value === "text" ? "text" : "prompt";
    const ok = await run({ type, pane: this.pane, text });
    this.busy = false;
    this.paint();
    if (ok) {
      // The box stays editable while the send is out; keep what was typed meanwhile.
      this.input.value = afterSend(text, this.input.value);
      this.stuck = true;
    }
    this.input.focus();
  }
}

function keyLike(e: KeyboardEvent): KeyLike {
  return {
    key: e.key,
    code: e.code,
    ctrlKey: e.ctrlKey,
    altKey: e.altKey,
    shiftKey: e.shiftKey,
    metaKey: e.metaKey,
    isComposing: e.isComposing,
    altGraph: e.getModifierState("AltGraph"),
  };
}

/** A details row's term or value, keyed by the row and the part. */
interface Part {
  key: string;
  term: string;
  value: string | null;
}

/** The pane's details: a term and a value per row, as list items. */
function details(m: Member): Part[] {
  const rows: [string, string][] = [["Pane", m.pane_id]];
  if (m.tab) rows.push(["Tab", m.tab.label || m.tab.tab_id]);
  if (m.task?.task) rows.push(["Task", m.task.task]);
  if (m.task?.branch) rows.push(["Branch", m.task.branch]);
  if (m.task?.contract) rows.push(["Contract", m.task.contract]);
  if (m.pane?.cwd) rows.push(["Folder", m.pane.cwd]);
  return rows.flatMap(([k, v]): Part[] => [
    { key: `${k}|dt`, term: k, value: null },
    { key: `${k}|dd`, term: k, value: v },
  ]);
}

function part(p: Part): RowPart {
  return p.value === null ? Term([p.term]) : Value([p.value, p.value]);
}

interface ToolSpec {
  glyph: readonly string[];
  label: string;
  fn: () => unknown;
  danger: boolean;
}

function tool(glyph: readonly string[], label: string, fn: () => unknown, danger = false): ToolSpec {
  return { glyph, label, fn, danger };
}
