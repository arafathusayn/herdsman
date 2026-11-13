import { mountRoot } from "./components/mount-root.ts";
import { lifetime } from "./state/lifetime.ts";
import { valueState } from "./state/value.ts";

// Delegate title discovery across app and modal roots. Reactre owns the
// tooltip's content, visibility and placement; the installer owns listeners.
const showDelay = 450;
const warmFor = 600;
const withTip = "[title], [data-tip]";
const tipId = "herdsman-tooltip";

interface TipState {
  text: string;
  on: boolean;
  below: boolean;
  top: number;
  left: number;
}
function Tooltip(props: { state: () => TipState }) {
  return (
    <div
      id={tipId}
      role="tooltip"
      class={`tooltip${props.state().on ? " on" : ""}${props.state().below ? " below" : ""}`}
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
    on: false,
    below: false,
    top: 0,
    left: 0,
  });
  let view: ReturnType<typeof mountRoot<"div">> | undefined;
  let target: HTMLElement | null = null;
  let timer = 0;
  const [warmUntil, setWarmUntil] = valueState(0);
  const life = lifetime();
  const hide = (): void => {
    window.clearTimeout(timer);
    if (target?.getAttribute("aria-describedby") === tipId) target.removeAttribute("aria-describedby");
    if (state().on) {
      setState({ ...state(), on: false });
      setWarmUntil(Date.now() + warmFor);
    }
    target = null;
  };
  const show = (el: HTMLElement): void => {
    const text = tipText(el);
    if (life.closed() || !text || !el.isConnected) return;
    view ??= mountRoot("div", () => <Tooltip state={state} />);
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
      on: true,
      below: above < gap,
      top: Math.round(above < gap ? rect.bottom + gap : above),
      left: Math.round(left),
    });
    el.setAttribute("aria-describedby", tipId);
  };
  const arm = (el: HTMLElement, delay: number): void => {
    hide();
    if (!tipText(el)) return;
    target = el;
    timer = window.setTimeout(() => show(el), Date.now() < warmUntil() ? 0 : delay);
  };
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
    if (!life.close()) return;
    hide();
    for (const remove of listeners) remove();
    view?.dispose();
  };
}
