// Styled tooltips for every element with a title (or data-tip): shown after a
// short hover delay or on keyboard focus, never on touch. The title moves to
// data-tip so the browser's own tooltip does not show as well; an element
// whose title was its only name keeps it as aria-label.

const showDelay = 450;
/** After one tooltip, the next shows at once while the pointer keeps moving between controls. */
const warmFor = 600;

let tip: HTMLDivElement | null = null;
let target: HTMLElement | null = null;
let timer = 0;
let warmUntil = 0;

function tipText(el: HTMLElement): string {
  const t = el.getAttribute("title");
  if (t) {
    el.dataset["tip"] = t;
    el.removeAttribute("title");
    if (!el.hasAttribute("aria-label") && !el.textContent?.trim()) el.setAttribute("aria-label", t);
  }
  return el.dataset["tip"] ?? "";
}

function host(el: HTMLElement): HTMLElement {
  // A modal dialog sits in the top layer; a tooltip outside it would hide behind it.
  return el.closest<HTMLElement>("dialog[open]") ?? document.body;
}

function place(el: HTMLElement, t: HTMLDivElement): void {
  const r = el.getBoundingClientRect();
  const w = t.offsetWidth;
  const ht = t.offsetHeight;
  const gap = 8;
  let top = r.top - ht - gap;
  let below = false;
  if (top < gap) {
    top = r.bottom + gap;
    below = true;
  }
  const left = Math.min(Math.max(gap, r.left + r.width / 2 - w / 2), window.innerWidth - w - gap);
  t.style.top = `${Math.round(top)}px`;
  t.style.left = `${Math.round(left)}px`;
  t.classList.toggle("below", below);
}

function show(el: HTMLElement): void {
  const text = tipText(el);
  if (!text || !el.isConnected) return;
  if (!tip) {
    tip = document.createElement("div");
    tip.className = "tooltip";
    tip.id = "herdsman-tooltip";
    tip.setAttribute("role", "tooltip");
  }
  tip.textContent = text;
  const parent = host(el);
  if (tip.parentElement !== parent) parent.append(tip);
  place(el, tip);
  tip.classList.add("on");
  el.setAttribute("aria-describedby", tip.id);
}

function hide(): void {
  window.clearTimeout(timer);
  if (target?.getAttribute("aria-describedby") === "herdsman-tooltip") target.removeAttribute("aria-describedby");
  if (tip?.classList.contains("on")) {
    tip.classList.remove("on");
    warmUntil = Date.now() + warmFor;
  }
  target = null;
}

function arm(el: HTMLElement, delay: number): void {
  hide();
  if (!tipText(el)) return;
  target = el;
  timer = window.setTimeout(() => show(el), Date.now() < warmUntil ? 0 : delay);
}

const withTip = "[title], [data-tip]";

export function installTooltips(): void {
  document.addEventListener("pointerover", (e) => {
    if (e.pointerType === "touch") return;
    const el = (e.target as Element | null)?.closest?.<HTMLElement>(withTip) ?? null;
    if (el === target) return;
    if (el) arm(el, showDelay);
    else hide();
  });
  document.addEventListener("pointerout", (e) => {
    const to = e.relatedTarget as Node | null;
    if (target && !(to && target.contains(to))) hide();
  });
  document.addEventListener("focusin", (e) => {
    const el = (e.target as Element | null)?.closest?.<HTMLElement>(withTip) ?? null;
    if (el?.matches(":focus-visible")) arm(el, 150);
  });
  document.addEventListener("focusout", hide);
  document.addEventListener("pointerdown", hide, true);
  document.addEventListener("scroll", hide, true);
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && target) hide();
  });
}
