import { run } from "../actions.ts";
// The agent panel: a live pane tail, a composer and every direct action.
// It is built once per pane and only patched on state changes, so the
// composer keeps its text and the tail keeps its scroll position. Its
// terminal view shows the pane's screen and sends typing and the wheel
// straight to it.
import { arrowKeys, keySet, renamePane, sendOtherKeys, startAgent } from "../agent-actions.ts";
import { readPalette, renderAnsi } from "../ansi.ts";
import { act, AuthError, forgetPane, readPane } from "../api.ts";
import { h, icon, icons, mount } from "../dom.ts";
import { type Member, roleName } from "../model.ts";
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
import { pill, toast } from "../ui.ts";

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

export class AgentPanel {
  readonly el: HTMLElement;
  private readonly head = h("div", { class: "panel-head" });
  private readonly meta = h("dl", { class: "meta" });
  /** The pane's id and tab on one line, shown while its details are folded. */
  private readonly infoLine = h("span", { class: "pane-info-line" });
  private readonly tail = h("pre", {
    class: "tail",
    tabIndex: 0,
    "aria-label": "Pane output",
  });
  private readonly tailNote = h("div", { class: "tail-note" });
  /** Whether the last text read was cut to its line limit. */
  private truncated = false;
  /** Takes the typing of the terminal view, so IME, paste and phone keyboards work. */
  private readonly termInput = h("textarea", {
    class: "term-input",
    rows: 1,
    autocapitalize: "off",
    autocomplete: "off",
    autocorrect: "off",
    "aria-label": "Type into the pane",
  });
  private readonly termHint = h("div", { class: "term-hint" });
  private readonly viewBtns = new Map<PaneView, HTMLButtonElement>();
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
  /** Measures the width the panel asks for, which the window may cut. */
  private readonly want = h("div", { class: "panel-want", "aria-hidden": "true" });
  private readonly input = h("textarea", {
    rows: 3,
    placeholder: defaultPlaceholder,
    spellcheck: true,
    "aria-label": "Message",
  });
  private readonly mode = h(
    "select",
    {
      "aria-label": "Send as",
      title: "Prompt goes through the agent; Type sends raw text into the pane",
    },
    h("option", { value: "prompt" }, "Prompt"),
    h("option", { value: "text" }, "Type raw"),
  );
  private readonly sendBtn = h("button", { class: "btn primary", type: "submit" }, icon(icons.send), "Send");
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
    this.tail.addEventListener("scroll", () => {
      const t = this.tail;
      this.stuck = t.scrollHeight - t.scrollTop - t.clientHeight < 24;
    });
    this.input.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        void this.send();
      }
    });
    const composer = h(
      "form",
      {
        class: "composer",
        onsubmit: (e: Event) => {
          e.preventDefault();
          void this.send();
        },
      },
      this.input,
      h(
        "div",
        { class: "composer-bar" },
        h("label", { class: "select" }, this.mode, icon(icons.chevronDown)),
        h("span", { class: "grow" }),
        this.sendBtn,
      ),
    );
    const keys = h(
      "div",
      { class: "keys", role: "group", "aria-label": "Send a key" },
      icon(icons.keyboard),
      keySet.map((k) =>
        h(
          "button",
          {
            class: "key",
            title: k.hint,
            onclick: () => void run({ type: "keys", pane: this.pane, keys: k.keys }),
          },
          k.label,
        ),
      ),
      h(
        "span",
        { class: "key-group", role: "group", "aria-label": "Arrow keys" },
        arrowKeys.map((k) =>
          h(
            "button",
            {
              class: "key arrow",
              title: `${k.name} arrow`,
              "aria-label": `${k.name} arrow`,
              onclick: () => void run({ type: "keys", pane: this.pane, keys: k.keys }),
            },
            k.label,
          ),
        ),
      ),
      h(
        "button",
        {
          class: "key",
          title: "Send other key names, separated by spaces",
          onclick: () => void sendOtherKeys(this.pane),
        },
        "Other…",
      ),
    );
    const viewSwitch = h(
      "div",
      { class: "view-switch", role: "group", "aria-label": "Pane view" },
      (
        [
          ["log", icons.list, "Log", "The pane's recent output, with a box to message the agent"],
          [
            "terminal",
            icons.terminal,
            "Terminal",
            "The pane's screen: type straight into it, and the wheel scrolls it",
          ],
        ] as const
      ).map(([v, glyph, label, tip]) => {
        const b = h("button", { type: "button", title: tip, onclick: () => this.setView(v) }, icon(glyph), label);
        this.viewBtns.set(v, b);
        return b;
      }),
    );
    this.termInput.spellcheck = false;
    this.termInput.addEventListener("keydown", (e) => {
      if (this.zoomFor(e)) return;
      const i = keyInput(keyLike(e), isMac);
      if (!i) return;
      // Esc, Tab and the rest belong to the pane, not to the page.
      e.preventDefault();
      e.stopPropagation();
      this.inputs.push(i);
    });
    const takeText = (): void => {
      const text = typedText(this.termInput.value);
      this.termInput.value = "";
      if (text) this.inputs.push({ text });
    };
    this.termInput.addEventListener("input", (e) => {
      if (!(e as InputEvent).isComposing) takeText();
    });
    this.termInput.addEventListener("compositionend", takeText);
    this.termInput.addEventListener("focus", () => this.showTyping());
    this.termInput.addEventListener("blur", () => this.showTyping());
    // A click on the screen starts typing there, unless it selected text to copy.
    this.tail.addEventListener("mouseup", () => {
      if (this.view === "terminal" && (window.getSelection()?.isCollapsed ?? true)) {
        this.termInput.focus({ preventScroll: true });
      }
    });
    this.tail.addEventListener("keydown", (e) => {
      if (this.zoomFor(e)) return;
      if (this.view === "terminal" && e.key === "Enter") {
        e.preventDefault();
        this.termInput.focus({ preventScroll: true });
      }
    });
    this.tail.addEventListener(
      "wheel",
      (e) => {
        // A sideways scroll moves along a screen wider than the panel.
        if (this.view !== "terminal" || e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY)) return;
        e.preventDefault();
        const lines = this.wheel.add(e.deltaY, e.deltaMode, this.text.split("\n").length);
        if (lines) this.inputs.push({ lines, ...this.cellAt(e.clientX, e.clientY) });
      },
      { passive: false },
    );
    // The details fold to one line in the bar, so the screen gets the room;
    // they open below the bar, and the choice is kept.
    this.meta.id = `meta-${pane}`;
    const info = h(
      "button",
      {
        type: "button",
        class: "pane-info",
        title: "Show or hide the pane's details",
        "aria-controls": this.meta.id,
      },
      icon(icons.chevronDown),
      h("span", { class: "pane-info-label" }, "Details"),
      this.infoLine,
    );
    const fold = (open: boolean): void => {
      info.setAttribute("aria-expanded", String(open));
      this.meta.hidden = !open;
    };
    try {
      fold(localStorage.getItem(infoKey) === "open");
    } catch {
      fold(false);
    }
    info.addEventListener("click", () => {
      fold(this.meta.hidden !== false);
      try {
        localStorage.setItem(infoKey, this.meta.hidden ? "closed" : "open");
      } catch {
        // The choice lasts for this page only.
      }
    });
    this.el = h(
      "aside",
      { class: "panel", "aria-label": "Agent" },
      this.head,
      h("div", { class: "pane-bar" }, info, this.termHint, viewSwitch),
      this.meta,
      h("div", { class: "tail-wrap" }, this.tail, this.tailNote, this.termInput),
      keys,
      composer,
    );
    this.el.append(this.want, panelResizer(this.el));
    this.fit.observe(this.tail);
    this.fit.observe(this.want);
    this.applyView();
    this.start();
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
    this.el.classList.toggle("terminal-view", term);
    this.tail.setAttribute("aria-label", term ? "Pane screen: press Enter to type into it" : "Pane output");
    for (const [v, b] of this.viewBtns) {
      b.classList.toggle("active", v === this.view);
      b.setAttribute("aria-pressed", String(v === this.view));
    }
    if (!term) this.termInput.blur();
    this.showTyping();
    this.fitFont();
  }

  private showTyping(): void {
    const typing = this.view === "terminal" && document.activeElement === this.termInput;
    this.el.classList.toggle("typing", typing);
    const zoom = this.zoom === 1 ? "" : ` · ${Math.round(this.zoom * 100)}%`;
    this.termHint.textContent =
      this.view !== "terminal"
        ? ""
        : typing
          ? `Typing into the pane${zoom} · click outside to stop`
          : `Click the screen to type${zoom}`;
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
      this.showTyping();
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
    const a = m.agent;
    mount(
      this.head,
      h(
        "button",
        {
          class: "icon-btn",
          title: "Close panel",
          "aria-label": "Close panel",
          onclick: () => store.go({ pane: "" }),
        },
        icon(icons.close),
      ),
      h(
        "div",
        { class: "panel-title" },
        h("h2", null, m.title),
        h(
          "div",
          { class: "sub" },
          pill(m.status),
          m.role === "adjutant" || m.role === "orchestrator"
            ? h("span", { class: `chip role-${m.role}` }, roleName[m.role])
            : null,
          a?.agent ? h("span", { class: "chip" }, a.display_agent ?? a.agent) : null,
        ),
      ),
      h(
        "div",
        { class: "panel-tools" },
        tool(icons.focus, "Focus in herdr", () => run({ type: "focus", pane: this.pane })),
        tool(icons.play, "Start an agent in this pane", () => startAgent(this.pane, this.kinds())),
        tool(icons.tag, "Rename", () => this.member && renamePane(this.member)),
        tool(icons.split, "Split right", () => run({ type: "split", pane: this.pane, direction: "right" })),
        tool(icons.splitDown, "Split down", () => run({ type: "split", pane: this.pane, direction: "down" })),
        tool(icons.trash, "Delete pane", () => run({ type: "close", pane: this.pane }), true),
      ),
    );
    this.infoLine.textContent = [m.pane_id, m.tab ? m.tab.label || m.tab.tab_id : ""].filter(Boolean).join(" · ");
    mount(
      this.meta,
      row("Pane", m.pane_id),
      m.tab ? row("Tab", m.tab.label || m.tab.tab_id) : null,
      m.task?.task ? row("Task", m.task.task) : null,
      m.task?.branch ? row("Branch", m.task.branch) : null,
      m.task?.contract ? row("Contract", m.task.contract) : null,
      m.pane?.cwd ? row("Folder", m.pane.cwd) : null,
    );
    const gone = m.status === "gone";
    this.input.disabled = gone;
    this.sendBtn.disabled = gone;
    this.termInput.disabled = gone;
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
    const probe = h("span", null, "Ag│█");
    const wide = h("span", null, "M".repeat(20));
    this.tail.append(probe, wide);
    const height = probe.getBoundingClientRect().height;
    const width = wide.getBoundingClientRect().width / 20;
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
        this.tailNote.textContent = this.truncated && !term ? "Showing the last 400 lines." : "";
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
        else this.tailNote.textContent = e instanceof Error ? e.message : String(e);
      }
    }
  }

  private async send(): Promise<void> {
    const text = this.input.value;
    if (!text.trim() || this.busy) return;
    this.busy = true;
    this.sendBtn.disabled = true;
    const type = this.mode.value === "text" ? "text" : "prompt";
    const ok = await run({ type, pane: this.pane, text });
    this.busy = false;
    this.sendBtn.disabled = false;
    if (ok) {
      this.input.value = "";
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

function row(k: string, v: string): HTMLElement[] {
  return [h("dt", null, k), h("dd", { title: v }, v)];
}

function tool(glyph: string[], label: string, fn: () => unknown, danger = false): HTMLElement {
  return h(
    "button",
    {
      class: danger ? "icon-btn danger" : "icon-btn",
      title: label,
      "aria-label": label,
      onclick: () => void fn(),
    },
    icon(glyph),
  );
}
