import { effect, For, memo, onCleanup, Show, signal, untrack } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { run } from "../actions.ts";
import { arrowKeys, keySet, renamePane, sendOtherKeys, startAgent } from "../agent-actions.ts";
import { type Palette, readPalette } from "../ansi.ts";
import { act, AuthError, forgetPane, readPane } from "../api.ts";
import { AnsiText } from "../components/ansi.tsx";
import { Icon } from "../components/icons.tsx";
import { mountRoot } from "../components/mount-root.ts";
import { Pill } from "../components/status.tsx";
import { icons } from "../icons.ts";
import { afterSend, type Member, roleName } from "../model.ts";
import { isMac } from "../palette.tsx";
import { panelResizer } from "../resize.tsx";
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
import { toast } from "../ui.ts";

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

const probeCells = 20;
const views: readonly { id: PaneView; glyph: readonly string[]; label: string; tip: string }[] = [
  { id: "log", glyph: icons.list, label: "Log", tip: "The pane's recent output, with a box to message the agent" },
  {
    id: "terminal",
    glyph: icons.terminal,
    label: "Terminal",
    tip: "The pane's screen: type straight into it, and the wheel scrolls it",
  },
];

interface PanelSnapshot {
  member: Member | null;
  hasAdjutant: boolean;
  infoOpen: boolean;
  note: string;
  view: PaneView;
  zoom: number;
  busy: boolean;
  typing: boolean;
  font: number;
  lineHeight: string;
  screen: { text: string; palette: Palette | null };
  resizer: HTMLElement | null;
}
export interface AgentHandle {
  focusComposer: () => void;
}

/** Owns terminal I/O and measurements; every displayed value lives in the signal snapshot. */
class AgentController implements AgentHandle {
  private readonly data = signal<PanelSnapshot>({
    member: null,
    hasAdjutant: false,
    infoOpen: loadInfoOpen(),
    note: "",
    view: loadView(),
    zoom: loadZoom(),
    busy: false,
    typing: false,
    font: baseFont,
    lineHeight: "",
    screen: { text: "", palette: null },
    resizer: null,
  });
  readonly state = this.data[0];
  private set(patch: Partial<PanelSnapshot>): void {
    this.data[1]((state) => ({ ...state, ...patch }));
  }
  get member() {
    return this.state().member;
  }
  get view() {
    return this.state().view;
  }
  get note() {
    return this.state().note;
  }
  get zoom() {
    return this.state().zoom;
  }
  get font() {
    return this.state().font;
  }
  get busy() {
    return this.state().busy;
  }
  get text() {
    return this.state().screen.text;
  }

  el!: HTMLElement;
  tail!: HTMLPreElement;
  termInput!: HTMLTextAreaElement;
  input!: HTMLTextAreaElement;
  mode!: HTMLSelectElement;
  want!: HTMLDivElement;
  private readonly wheel = new WheelLines();
  private readonly inputs = new InputQueue((input) => this.sendInput(input));
  private fit: ResizeObserver | undefined;
  private cell = { w: 0, h: 0 };
  private cols = 0;
  private unit = 0;
  private rowFitted = false;
  private timer = 0;
  private stuck = true;
  private truncated = false;
  private palKey = "";
  private dead = false;
  private reading = false;
  private again = false;
  private fastUntil = 0;
  private generation = 0;

  readonly pane: string;
  readonly kinds: () => string[];
  constructor(pane: string, kinds: () => string[]) {
    this.pane = pane;
    this.kinds = kinds;
  }

  mount(): void {
    this.set({ resizer: panelResizer(this.el) });
    this.fit = new ResizeObserver(() => this.fitFont());
    this.fit.observe(this.tail);
    this.fit.observe(this.want);
    this.applyView();
    this.start();
  }
  tailScroll(): void {
    this.stuck = this.tail.scrollHeight - this.tail.scrollTop - this.tail.clientHeight < 24;
  }
  tailMouse(): void {
    if (this.view === "terminal" && (window.getSelection()?.isCollapsed ?? true))
      this.termInput.focus({ preventScroll: true });
  }
  tailKey(event: KeyboardEvent): void {
    if (this.zoomFor(event)) return;
    if (this.view === "terminal" && event.key === "Enter") {
      event.preventDefault();
      this.termInput.focus({ preventScroll: true });
    }
  }
  tailWheel(event: WheelEvent): void {
    if (this.view !== "terminal" || event.shiftKey || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
    event.preventDefault();
    const lines = this.wheel.add(event.deltaY, event.deltaMode, this.text.split("\n").length);
    if (lines) this.inputs.push({ lines, ...this.cellAt(event.clientX, event.clientY) });
  }
  takeText(): void {
    const text = typedText(this.termInput.value);
    this.termInput.value = "";
    if (text && this.view === "terminal") this.inputs.push({ text });
  }
  termKey(event: KeyboardEvent): void {
    if (this.view !== "terminal" || this.zoomFor(event)) return;
    const input = keyInput(keyLike(event), isMac);
    if (!input) return;
    event.preventDefault();
    event.stopPropagation();
    this.inputs.push(input);
  }
  typing(value: boolean): void {
    this.set({ typing: value });
  }

  // The details fold to one line in the bar, so the screen gets the room;
  // they open below the bar, and the choice is kept.
  fold(open: boolean): void {
    this.set({ infoOpen: open });
    try {
      localStorage.setItem(infoKey, open ? "open" : "closed");
    } catch {
      // The choice lasts for this page only.
    }
  }

  setView(v: PaneView): void {
    if (v === this.view) return;
    this.generation++;
    this.set({ view: v, typing: false, screen: { ...this.state().screen, text: "" } });
    try {
      localStorage.setItem(viewKey, v);
    } catch {
      // The choice lasts for this page only.
    }
    // The other view's text is not on screen, so read it whole.
    forgetPane(this.pane);
    this.stuck = true;
    this.applyView();
    this.poke();
    if (v === "terminal") this.termInput.focus({ preventScroll: true });
  }

  private applyView(): void {
    const term = this.view === "terminal";
    // The typing field exists only for the terminal view: off it, nothing
    // can focus it, tab to it or type into the pane through it.
    if (!term) this.termInput.blur();
    this.fitFont();
  }

  /**
   * ⌘ + / − / 0 (Ctrl elsewhere) while the terminal view has focus zoom its
   * text, not the page; the browser's own zoom is kept from acting. Returns
   * whether the key was a zoom key, so the caller sends nothing to the pane.
   */
  zoomFor(e: KeyboardEvent): boolean {
    if (this.view !== "terminal") return false;
    const how = zoomKey(keyLike(e), isMac);
    if (!how) return false;
    e.preventDefault();
    e.stopPropagation();
    const z = nextZoom(this.zoom, how);
    if (z !== this.zoom) {
      this.set({ zoom: z });
      saveZoom(z);
      this.fitFont();
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
  sendKeys(keys: string[]): void {
    if (this.view === "terminal") this.inputs.push({ keys });
    else void run({ type: "keys", pane: this.pane, keys });
  }

  private async sendInput(i: Input): Promise<boolean> {
    if (this.dead) return false;
    const a: Action =
      "lines" in i ? { type: "scroll", pane: this.pane, ...i } : { type: "input", pane: this.pane, ...i };
    try {
      const res = await act(a);
      if (this.dead) return false;
      if (!res.ok) {
        toast(res.error ?? "The pane did not take the input.", "error");
        return false;
      }
      this.poke();
      return true;
    } catch (e) {
      if (this.dead) return false;
      if (e instanceof AuthError) store.set({ fatal: e.message });
      else toast(e instanceof Error ? e.message : String(e), "error");
      return false;
    }
  }

  update(member: Member | null, size: { cols: number } | null, hasAdjutant = false): void {
    this.set({ member, hasAdjutant });
    const cols = size?.cols ?? 0;
    if (cols !== this.cols) {
      this.cols = cols;
      if (this.tail) this.fitFont();
    }
  }

  destroy(): void {
    this.dead = true;
    this.fit?.disconnect();
    clearTimeout(this.timer);
    forgetPane(this.pane);
  }

  focusComposer(): void {
    if (!this.dead) this.input.focus();
  }

  /**
   * Makes each row exactly as tall as the font's glyph box, the way a
   * terminal sizes its cells, so background colors and box-drawing lines of
   * neighboring rows touch instead of leaving a gap of leading between them.
   */
  private fitRows(): void {
    if (this.rowFitted || !this.tail.isConnected) return;
    const probe = mountRoot("span", () => <span>Ag│█</span>);
    const wide = mountRoot("span", () => <span>MMMMMMMMMMMMMMMMMMMM</span>);
    this.tail.append(probe.el, wide.el);
    const height = probe.el.getBoundingClientRect().height;
    const width = wide.el.getBoundingClientRect().width / probeCells;
    probe.dispose();
    wide.dispose();
    if (height > 0) {
      this.set({ lineHeight: `${height}px` });
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
    this.set({ font });
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
    const generation = this.generation;
    if (!document.hidden && this.member?.status !== "gone") {
      try {
        const r = await readPane(this.pane, term);
        if (this.dead) return;
        // The view can change while the read is out; its answer is for the other one.
        if (generation !== this.generation) {
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
          const palette = palKey === this.palKey ? (this.state().screen.palette ?? pal) : pal;
          this.palKey = palKey;
          this.fitRows();
          this.fitFont();
          this.set({ screen: { text: r?.text ?? this.text, palette } });
          if (this.stuck) this.tail.scrollTop = this.tail.scrollHeight;
        }
      } catch (e) {
        if (this.dead) return;
        if (e instanceof AuthError) store.set({ fatal: e.message });
        else this.setNote(e instanceof Error ? e.message : String(e));
      }
    }
  }

  private setNote(note: string): void {
    if (note === this.note) return;
    this.set({ note });
  }

  async send(): Promise<void> {
    const text = this.input.value;
    if (!text.trim() || this.busy) return;
    this.set({ busy: true });
    const type = this.mode.value === "text" ? "text" : "prompt";
    const ok = await run({ type, pane: this.pane, text });
    if (this.dead) return;
    this.set({ busy: false });
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

interface Detail {
  key: string;
  term: string;
  value: string;
}
function details(member: Member | null): Detail[] {
  if (!member) return [];
  const rows: [string, string][] = [["Pane", member.pane_id]];
  if (member.tab) rows.push(["Tab", member.tab.label || member.tab.tab_id]);
  if (member.task?.task) rows.push(["Task", member.task.task]);
  if (member.task?.branch) rows.push(["Branch", member.task.branch]);
  if (member.task?.contract) rows.push(["Contract", member.task.contract]);
  if (member.pane?.cwd) rows.push(["Folder", member.pane.cwd]);
  return rows.map(([term, value]) => ({ key: term, term, value }));
}

function PanelHead(props: { member: Member; controller: AgentController }) {
  const controller = props.controller;
  const tools = [
    {
      glyph: icons.focus,
      label: "Focus in herdr",
      run: () => run({ type: "focus", pane: controller.pane }),
      danger: false,
    },
    {
      glyph: icons.play,
      label: "Start an agent in this pane",
      run: () => startAgent(controller.pane, controller.kinds()),
      danger: false,
    },
    { glyph: icons.tag, label: "Rename", run: () => controller.member && renamePane(controller.member), danger: false },
    {
      glyph: icons.split,
      label: "Split right",
      run: () => run({ type: "split", pane: controller.pane, direction: "right" }),
      danger: false,
    },
    {
      glyph: icons.splitDown,
      label: "Split down",
      run: () => run({ type: "split", pane: controller.pane, direction: "down" }),
      danger: false,
    },
    {
      glyph: icons.trash,
      label: "Delete pane",
      run: () => run({ type: "close", pane: controller.pane }),
      danger: true,
    },
  ];
  return (
    <>
      <button class="icon-btn" title="Close panel" aria-label="Close panel" onClick={() => store.go({ pane: "" })}>
        <Icon paths={icons.close} />
      </button>
      <div class="panel-title">
        <h2>{props.member.title}</h2>
        <div class="sub">
          <Pill status={props.member.status} />
          <Show when={props.member.role === "adjutant" || props.member.role === "orchestrator"}>
            <span class={`chip role-${props.member.role}`}>{roleName[props.member.role]}</span>
          </Show>
          <Show when={props.member.agent?.agent}>
            <span class="chip">{props.member.agent?.display_agent ?? props.member.agent?.agent}</span>
          </Show>
        </div>
      </div>
      <div class="panel-tools">
        <For each={tools} key={(tool) => tool.label}>
          {(tool) => (
            <button
              class={tool.danger ? "icon-btn danger" : "icon-btn"}
              title={tool.label}
              aria-label={tool.label}
              onClick={() => void tool.run()}
            >
              <Icon paths={tool.glyph} />
            </button>
          )}
        </For>
      </div>
    </>
  );
}

function PanelView(props: { controller: AgentController }) {
  const controller = props.controller;
  const state = controller.state;
  const term = memo(() => state().view === "terminal");
  const gone = memo(() => state().member?.status === "gone");
  const zoom = memo(() => (state().zoom === 1 ? "" : ` · ${Math.round(state().zoom * 100)}%`));
  const hint = memo(() =>
    !term()
      ? ""
      : state().typing
        ? `Typing into the pane${zoom()} · click outside to stop`
        : `Click the screen to type${zoom()}`,
  );
  const rows = map(
    () => details(state().member),
    (row) => row,
    { keyed: (row) => row.key },
  );
  const placeholder = memo(() =>
    state().member?.role === "adjutant"
      ? "Talk to the adjutant: it relays to the orchestrator…  (⌘/Ctrl+Enter to send)"
      : state().member?.role === "orchestrator" && state().hasAdjutant
        ? "This route has an adjutant: talk through it. Text here bypasses the message channel."
        : defaultPlaceholder,
  );
  return (
    <aside
      class={["panel", term() && "terminal-view", term() && state().typing && "typing"].filter(Boolean).join(" ")}
      aria-label="Agent"
      ref={(element) => {
        controller.el = element;
      }}
    >
      <div class="panel-head">
        <Show when={state().member}>{(member) => <PanelHead member={member()} controller={controller} />}</Show>
      </div>
      <div class="pane-bar">
        <button
          type="button"
          class="pane-info"
          title="Show or hide the pane's details"
          aria-controls={`meta-${controller.pane}`}
          aria-expanded={state().infoOpen ? "true" : "false"}
          onClick={() => controller.fold(!state().infoOpen)}
        >
          <Icon paths={icons.chevronDown} />
          <span class="pane-info-label">Details</span>
          <span class="pane-info-line">
            {[state().member?.pane_id, state().member?.tab?.label || state().member?.tab?.tab_id]
              .filter(Boolean)
              .join(" · ")}
          </span>
        </button>
        <div class="term-hint">{hint()}</div>
        <div class="view-switch" role="group" aria-label="Pane view">
          <For each={views} key={(view) => view.id}>
            {(view) => (
              <button
                type="button"
                title={view.tip}
                class={view.id === state().view ? "active" : ""}
                aria-pressed={view.id === state().view ? "true" : "false"}
                onClick={() => controller.setView(view.id)}
              >
                <Icon paths={view.glyph} />
                {view.label}
              </button>
            )}
          </For>
        </div>
      </div>
      <dl class="meta" id={`meta-${controller.pane}`} hidden={!state().infoOpen}>
        <For each={rows()} key={(row) => row().key}>
          {(row) => (
            <>
              <dt>{row().term}</dt>
              <dd title={row().value}>{row().value}</dd>
            </>
          )}
        </For>
      </dl>
      <div class="tail-wrap">
        <pre
          class="tail"
          tabindex="0"
          aria-label={term() ? "Pane screen: press Enter to type into it" : "Pane output"}
          style={{ fontSize: state().font === baseFont ? "" : `${state().font}px`, lineHeight: state().lineHeight }}
          ref={(element) => {
            controller.tail = element;
          }}
          onScroll={() => controller.tailScroll()}
          onMouseUp={() => controller.tailMouse()}
          onKeyDown={(event) => controller.tailKey(event)}
          onWheel={(event) => controller.tailWheel(event)}
        >
          <Show when={state().screen.palette}>
            {(palette) => <AnsiText text={state().screen.text} palette={palette()} />}
          </Show>
        </pre>
        <div class="tail-note">{state().note}</div>
        <textarea
          class="term-input"
          rows="1"
          autocapitalize="off"
          autocomplete="off"
          autocorrect="off"
          aria-label="Type into the pane"
          spellcheck="false"
          disabled={!term() || gone()}
          ref={(element) => {
            controller.termInput = element;
          }}
          onKeyDown={(event) => controller.termKey(event)}
          onInput={(event) => {
            if (!("isComposing" in event && event.isComposing === true)) controller.takeText();
          }}
          onFocus={() => controller.typing(true)}
          onBlur={() => controller.typing(false)}
          onCompositionEnd={() => controller.takeText()}
        />
      </div>
      <div class="keys" role="group" aria-label="Send a key">
        <Icon paths={icons.keyboard} />
        <For each={keySet} key={(key) => key.label}>
          {(key) => (
            <button class="key" title={key.hint} onClick={() => controller.sendKeys(key.keys)}>
              {key.label}
            </button>
          )}
        </For>
        <span class="key-group" role="group" aria-label="Arrow keys">
          <For each={arrowKeys} key={(key) => key.name}>
            {(key) => (
              <button
                class="key arrow"
                title={`${key.name} arrow`}
                aria-label={`${key.name} arrow`}
                onClick={() => controller.sendKeys(key.keys)}
              >
                {key.label}
              </button>
            )}
          </For>
        </span>
        <button
          class="key"
          title="Send other key names, separated by spaces"
          onClick={() => void sendOtherKeys((keys) => controller.sendKeys(keys))}
        >
          Other…
        </button>
      </div>
      <form
        class="composer"
        onSubmit={(event) => {
          event.preventDefault();
          void controller.send();
        }}
      >
        <textarea
          rows="3"
          placeholder={placeholder()}
          spellcheck="true"
          aria-label="Message"
          disabled={gone()}
          ref={(element) => {
            controller.input = element;
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
              event.preventDefault();
              void controller.send();
            }
          }}
        />
        <div class="composer-bar">
          <label class="select">
            <select
              aria-label="Send as"
              title="Prompt goes through the agent; Type sends raw text into the pane"
              ref={(element) => {
                controller.mode = element;
              }}
            >
              <option value="prompt">Prompt</option>
              <option value="text">Type raw</option>
            </select>
            <Icon paths={icons.chevronDown} />
          </label>
          <span class="grow" />
          <button class="btn primary" type="submit" disabled={state().busy || gone()}>
            <Icon paths={icons.send} />
            Send
          </button>
        </div>
      </form>
      <div
        class="panel-want"
        aria-hidden="true"
        ref={(element) => {
          controller.want = element;
        }}
      />
      <>{state().resizer}</>
    </aside>
  );
}

export function AgentPanel(props: {
  pane: string;
  kinds: () => string[];
  member: Member | null;
  size: { cols: number } | null;
  hasAdjutant?: boolean;
  handle?: (handle: AgentHandle | null) => void;
}) {
  const controller = new AgentController(props.pane, props.kinds);
  effect(() => {
    const member = props.member,
      size = props.size,
      hasAdjutant = props.hasAdjutant ?? false;
    untrack(() => controller.update(member, size, hasAdjutant));
  });
  onCleanup(() => {
    controller.destroy();
    props.handle?.(null);
  });
  const view = <PanelView controller={controller} />;
  controller.mount();
  props.handle?.(controller);
  return view;
}
