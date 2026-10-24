// Draggable widths: the agent panel at its left edge and the sidebar at its
// right edge, each kept for next time.
import { h } from "./dom.ts";

/** One resizable column: where its width lives and how far it may go. */
interface Edge {
  /** The localStorage key of the dragged width. */
  key: string;
  /** The CSS variable on the root that holds it; the stylesheet has the default. */
  prop: string;
  /** Which side of the column the handle sits on. */
  side: "left" | "right";
  min: number;
  max: () => number;
  label: string;
}

/** The main column keeps at least this much room. */
const mainRoom = 360;
const step = 32;

const panelEdge: Edge = {
  key: "herdsman.panel-width",
  prop: "--panel-w",
  side: "left",
  min: 340,
  max: () => {
    const nav = document.querySelector(".sidebar")?.getBoundingClientRect().width ?? 0;
    const navShown = !document.body.classList.contains("nav-collapsed");
    return window.innerWidth - (navShown ? nav : 0) - mainRoom;
  },
  label: "Resize the panel",
};

const navEdge: Edge = {
  key: "herdsman.nav-width",
  prop: "--nav-size",
  side: "right",
  min: 180,
  max: () => {
    const panel = document.querySelector(".panel")?.getBoundingClientRect().width ?? 0;
    return Math.min(480, window.innerWidth - panel - mainRoom);
  },
  label: "Resize the sidebar",
};

/** Puts back the widths the user last dragged the panel and the sidebar to. */
export function loadWidths(): void {
  for (const e of [panelEdge, navEdge]) {
    try {
      const w = Number(localStorage.getItem(e.key));
      if (w > 0) setWidth(e, w);
    } catch {
      // The default width applies.
    }
  }
}

function setWidth(e: Edge, w: number): void {
  document.documentElement.style.setProperty(e.prop, `${Math.round(w)}px`);
}

function saveWidth(e: Edge, w: number | null): void {
  try {
    if (w === null) localStorage.removeItem(e.key);
    else localStorage.setItem(e.key, String(Math.round(w)));
  } catch {
    // The width lasts for this page only.
  }
}

/** A width between min and the room there is; a window too small for both still leaves min. */
export function clampWidth(w: number, max: number, min = panelEdge.min): number {
  return Math.min(Math.max(w, min), Math.max(min, max));
}

export function panelResizer(panel: HTMLElement): HTMLElement {
  return resizer(panel, panelEdge);
}

export function navResizer(nav: HTMLElement): HTMLElement {
  return resizer(nav, navEdge);
}

/**
 * A handle on a column's edge: drag it, or focus it and use the arrow keys;
 * a double click goes back to the default width.
 */
function resizer(col: HTMLElement, e: Edge): HTMLElement {
  const handle = h("div", {
    class: `col-resize ${e.side}`,
    role: "separator",
    tabIndex: 0,
    "aria-orientation": "vertical",
    "aria-label": e.label,
    title: "Drag to resize · double-click for the default width",
  });
  const apply = (w: number): number => {
    const c = clampWidth(w, e.max(), e.min);
    setWidth(e, c);
    handle.setAttribute("aria-valuenow", String(Math.round(c)));
    return c;
  };
  // The arrow toward the column's free side widens it.
  const grow = e.side === "left" ? "ArrowLeft" : "ArrowRight";
  const shrink = e.side === "left" ? "ArrowRight" : "ArrowLeft";
  handle.addEventListener("pointerdown", (ev) => {
    if (ev.button !== 0) return;
    ev.preventDefault();
    handle.setPointerCapture(ev.pointerId);
    const r = col.getBoundingClientRect();
    let w = r.width;
    document.body.classList.add("resizing");
    const move = (m: PointerEvent): void => {
      w = apply(e.side === "left" ? r.right - m.clientX : m.clientX - r.left);
    };
    const up = (): void => {
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", up);
      handle.removeEventListener("pointercancel", up);
      document.body.classList.remove("resizing");
      saveWidth(e, w);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", up);
    handle.addEventListener("pointercancel", up);
  });
  handle.addEventListener("keydown", (k) => {
    const d = k.key === grow ? step : k.key === shrink ? -step : 0;
    if (!d) return;
    k.preventDefault();
    saveWidth(e, apply(col.getBoundingClientRect().width + d));
  });
  handle.addEventListener("dblclick", () => {
    document.documentElement.style.removeProperty(e.prop);
    saveWidth(e, null);
  });
  return handle;
}
