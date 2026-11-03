import { attach } from "@reactre/core";

import { installTooltips } from "../../src/tooltip.tsx";
import { check, equal, q, run } from "./lib.ts";

await run(async () => {
  const dispose = attach(
    () => (
      <>
        <button id="first" title="<b>Plain text</b>"></button>
        <button id="second" title="Second">
          Second
        </button>
        <dialog>
          <button id="modal" title="Modal tip">
            Modal
          </button>
        </dialog>
      </>
    ),
    q(document, "#app"),
  );
  const stop = installTooltips();
  const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
  const hover = (element: Element, pointerType = "mouse") =>
    element.dispatchEvent(new PointerEvent("pointerover", { bubbles: true, pointerType }));
  const first = q<HTMLButtonElement>(document, "#first");
  try {
    hover(first, "touch");
    await wait(500);
    check("touch does not show a tooltip", document.querySelector('[role="tooltip"]') === null);
    hover(first);
    check("mouse hover waits before showing", document.querySelector('[role="tooltip"]') === null);
    await wait(500);
    const tip = q<HTMLElement>(document, '[role="tooltip"]');
    check("hover displays a native tooltip", tip.classList.contains("on"));
    equal("tooltip text is never parsed as HTML", tip.textContent, "<b>Plain text</b>");
    check("tooltip text remains safe", tip.querySelector("b") === null);
    equal(
      "title supplies the unnamed control's accessible name",
      first.getAttribute("aria-label"),
      "<b>Plain text</b>",
    );
    equal("tooltip is described by its control", first.getAttribute("aria-describedby"), tip.id);
    document.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" }));
    check(
      "Escape hides and clears description",
      !tip.classList.contains("on") && !first.hasAttribute("aria-describedby"),
    );
    hover(q(document, "#second"));
    await wait(30);
    check(
      "warm hover reuses the tooltip immediately",
      document.querySelector('[role="tooltip"]') === tip &&
        tip.textContent === "Second" &&
        tip.classList.contains("on"),
    );
    document.dispatchEvent(new Event("scroll"));
    check("scroll hides the tooltip", !tip.classList.contains("on"));
    const dialog = q<HTMLDialogElement>(document, "dialog");
    dialog.showModal();
    hover(q(dialog, "#modal"));
    await wait(30);
    check(
      "modal tooltip lives in the dialog's top layer",
      tip.parentElement === dialog && tip.classList.contains("on"),
    );
    stop();
    check(
      "disposal removes the tooltip and its description",
      !tip.isConnected && !q(dialog, "#modal").hasAttribute("aria-describedby"),
    );
    hover(q(dialog, "#modal"));
    await wait(500);
    check("disposal removes delegated listeners", document.querySelector('[role="tooltip"]') === null);
    dialog.close();
    const stopPending = installTooltips();
    hover(first);
    stopPending();
    await wait(500);
    check("disposal cancels a pending show", document.querySelector('[role="tooltip"]') === null);
  } finally {
    stop();
    dispose();
  }
});
