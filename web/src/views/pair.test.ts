import { afterEach, beforeEach, expect, test } from "bun:test";

import { h, icon, icons, logoMark } from "../dom.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "../herdsman-blocks.testdom.ts";
import { dispatch, FakeElement as FormElement } from "../testdom.ts";
import { showPairing } from "./pair.ts";

const fake = (n: unknown): FakeElement => n as FakeElement;
const proto = FakeElement.prototype as unknown as Record<string, unknown>;
let focused: unknown[] = [];
beforeEach(() => {
  installFakeDocument();
  focused = [];
  proto["focus"] = function (this: FakeElement) {
    focused.push(this);
  };
});
afterEach(() => {
  delete proto["focus"];
});

// Elements and text with attributes sorted and the bridge's data-key left
// out; a slot's empty anchor text is not content.
const dump = (n: FakeNode): string => {
  if (n.nodeType === 3) return n.textContent;
  if (n.nodeType !== 1) return "";
  const e = fake(n);
  const attrs = e.attributes
    .filter((a) => a.name !== "data-key")
    .map((a) => `${a.name}="${a.value}"`)
    .sort()
    .join(" ");
  return `<${e.tagName.toLowerCase()} ${attrs}>${e.childNodes.map(dump).join("")}</${e.tagName.toLowerCase()}>`;
};
const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);

// The old h() screen, with its properties written as the attributes the
// browser reflects them to (inputMode, maxLength, spellcheck, disabled).
const hostsLink = (): HTMLElement =>
  h("button", { type: "button", class: "btn ghost pair-hosts" }, icon(icons.server), "Hosts");
const oldScreen = (message: string, messageClass: string, disabled: boolean): HTMLElement =>
  h(
    "main",
    { class: "pair" },
    h(
      "div",
      { class: "pair-card" },
      logoMark(),
      h("h1", null, "Pair this browser"),
      h("p", { class: "pair-host" }, icon(icons.server), "mac"),
      h(
        "p",
        { class: "pair-lead" },
        "Type the 6-digit code that ",
        h("code", null, "herdsman web"),
        " just printed in the host's terminal. It expires in 2 minutes.",
      ),
      h(
        "form",
        { class: "pair-form" },
        h("input", {
          class: "pair-code",
          inputmode: "numeric",
          autocomplete: "one-time-code",
          maxlength: "7",
          placeholder: "000 000",
          "aria-label": "Pairing code",
          spellcheck: "false",
        }),
        h("button", { class: "btn primary pair-submit", type: "submit", disabled: disabled }, "Pair this browser"),
      ),
      h("p", { class: messageClass, role: "status", "aria-live": "polite" }, message),
      h(
        "p",
        { class: "pair-note" },
        icon(icons.keyboard),
        "Pairing proves you can see the terminal, so a shared or leaked link is not enough to control your agents.",
      ),
      hostsLink(),
    ),
  );

test("pairing screen: the markup of the h() version, and typing formats the code in place", () => {
  const root = new FormElement("DIV");
  showPairing(root as unknown as HTMLElement, "mac", () => {});
  const page = elements(root)[0]!;
  expect(dump(page)).toBe(dump(fake(oldScreen("", "pair-message", true))));
  const card = elements(page)[0]!;
  const form = elements(card)[4]!;
  const [input, submit] = elements(form);
  expect(focused).toEqual([input]);

  input!["value"] = "12a3";
  dispatch(input!, "input");
  expect(input!["value"]).toBe("123");
  input!["value"] = "1234 5";
  dispatch(input!, "input");
  expect(input!["value"]).toBe("123 45");
  // The screen was patched: the same card, field and button.
  expect(elements(page)[0]).toBe(card);
  expect(elements(form)).toEqual([input!, submit!]);
  expect(submit!.hasAttribute("disabled")).toBe(true);
});
