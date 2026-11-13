import { createActor } from "@reactre/state";

import { mountRoot } from "./components/mount-root.ts";
import { observeActor } from "./state/observe.ts";
import { tooltipMachine } from "./state/tooltip.machine.ts";
import { valueState } from "./state/value.ts";

// Delegate title discovery across app and modal roots. Reactre owns the
// tooltip's content, visibility and placement; the installer owns listeners.
const showDelay = 450;
const withTip = "[title], [data-tip]";
const tipId = "herdsman-tooltip";

interface TipState {
  text: string;
  below: boolean;
  top: number;
  left: number;
}
function Tooltip(props: { state: () => TipState; visible: () => boolean }) {
  return (
    <div
      id={tipId}
      role="tooltip"
      class={`tooltip${props.visible() ? " on" : ""}${props.state().below ? " below" : ""}`}
      style={{ top: `${props.state().top}px`, left: `${props.state().left}px` }}
    >
      {props.state().text}
    </div>
  );
}

function tipText(el: HTMLElement): string {
  const title = el.getAttribute("title");
  if (title) {
    el.dataset["tip"] = title;
    el.removeAttribute("title");
    if (!el.hasAttribute("aria-label") && !el.textContent?.trim()) el.setAttribute("aria-label", title);
  }
  return el.dataset["tip"] ?? "";
}

/** Installs delegated mouse/keyboard tooltips; the disposer removes every resource. */
export function installTooltips(): () => void {
  const [state, setState] = valueState<TipState>({
    text: "",
    below: false,
    top: 0,
    left: 0,
  });
  let view: ReturnType<typeof mountRoot<"div">> | undefined;
  let target: HTMLElement | null = null;
  const actor = createActor(tooltipMachine);
  const phase = observeActor(actor);
  const hide = (): void => {
    if (target?.getAttribute("aria-describedby") === tipId) target.removeAttribute("aria-describedby");
    actor.send({ type: "hide", now: Date.now() });
    target = null;
  };
  const show = (el: HTMLElement): void => {
    const text = tipText(el);
    if (actor.snapshot().state !== "placing") return;
    if (!text || !el.isConnected) {
      hide();
      return;
    }
    view ??= mountRoot("div", () => <Tooltip state={state} visible={() => phase.read().state === "showing"} />);
    setState({ ...state(), text });
    // A modal sits in the top layer; its tooltip must be inside that modal.
    const parent = el.closest<HTMLElement>("dialog[open]") ?? document.body;
    if (view.el.parentElement !== parent) parent.append(view.el);
    const rect = el.getBoundingClientRect();
    const gap = 8,
      width = view.el.offsetWidth,
      height = view.el.offsetHeight;
    const above = rect.top - height - gap;
    const left = Math.min(Math.max(gap, rect.left + rect.width / 2 - width / 2), window.innerWidth - width - gap);
    setState({
      text,
      below: above < gap,
      top: Math.round(above < gap ? rect.bottom + gap : above),
      left: Math.round(left),
    });
    el.setAttribute("aria-describedby", tipId);
    actor.send({ type: "placed" });
  };
  const arm = (el: HTMLElement, delay: number): void => {
    hide();
    if (!tipText(el)) return;
    target = el;
    actor.send({ type: "arm", now: Date.now(), delay });
  };
  const off = actor.subscribe((snapshot) => {
    if (snapshot.state === "placing") {
      if (target) show(target);
      else hide();
    }
  });
  const listeners: (() => void)[] = [];
  const listen = <K extends keyof DocumentEventMap>(
    type: K,
    handler: (event: DocumentEventMap[K]) => void,
    capture = false,
  ): void => {
    document.addEventListener(type, handler, capture);
    listeners.push(() => document.removeEventListener(type, handler, capture));
  };
  listen("pointerover", (event) => {
    if (event.pointerType === "touch") return;
    const el = (event.target as Element | null)?.closest?.<HTMLElement>(withTip) ?? null;
    if (el === target) return;
    if (el) arm(el, showDelay);
    else hide();
  });
  listen("pointerout", (event) => {
    const to = event.relatedTarget as Node | null;
    if (target && !(to && target.contains(to))) hide();
  });
  listen("focusin", (event) => {
    const el = (event.target as Element | null)?.closest?.<HTMLElement>(withTip) ?? null;
    if (el?.matches(":focus-visible")) arm(el, 150);
  });
  listen("focusout", hide);
  listen("pointerdown", hide, true);
  listen("scroll", hide, true);
  listen("keydown", (event) => {
    if (event.key === "Escape" && target) hide();
  });
  return () => {
    actor.send({ type: "close" });
    hide();
    off();
    phase.stop();
    for (const remove of listeners.splice(0)) remove();
    view?.dispose();
    view = undefined;
  };
}
