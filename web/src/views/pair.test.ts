import { afterEach, beforeEach, expect, test } from "bun:test";

import { render } from "../herdsman-blocks-view.ts";
import { FakeElement, type FakeNode, installFakeDocument } from "../herdsman-blocks.testdom.ts";
import { icon, icons } from "../icons.ts";
import { dispatch, dump, FakeElement as FormElement, markup } from "../testdom.ts";
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

const elements = (n: FakeNode): FakeElement[] => n.childNodes.filter((c): c is FakeElement => c.nodeType === 1);
const iconMarkup = (paths: readonly string[]): string => {
  const box = document.createElement("div");
  render(box, icon(paths));
  return dump(fake(box).firstChild!);
};

test("pairing screen: the markup of the h() version, and typing formats the code in place", () => {
  const root = new FormElement("DIV");
  showPairing(root as unknown as HTMLElement, "mac", () => {});
  const page = elements(root)[0]!;
  expect(dump(page)).toBe(markup`
    <main class="pair">
      <div class="pair-card">
        <svg:svg aria-hidden="true" class="logo" viewBox="0 0 32 32">
          <svg:rect fill="#1d4d38" height="32" rx="8" width="32"></svg:rect>
          <svg:g fill="none" stroke-linecap="round" stroke-linejoin="round" stroke-width="2.6" stroke="#e9f6ef">
            <svg:path d="M10 6.5V26"></svg:path>
            <svg:path d="M10 17.5a5.5 5.5 0 0 1 11 0v5.5a3 3 0 0 0 3 3"></svg:path>
          </svg:g>
          <svg:circle cx="15.5" cy="24" fill="#5cc093" r="2"></svg:circle>
        </svg:svg>
        <h1>Pair this browser</h1>
        <p class="pair-host">${iconMarkup(icons.server)}mac</p>
        <p class="pair-lead">Type the 6-digit code that <code>herdsman web</code> just printed in the host's terminal. It expires in 2 minutes.</p>
        <form class="pair-form">
          <input aria-label="Pairing code" autocomplete="one-time-code" class="pair-code" inputmode="numeric" maxlength="7" placeholder="000 000" spellcheck="false"></input>
          <button class="btn primary pair-submit" disabled="" type="submit">Pair this browser</button>
        </form>
        <p aria-live="polite" class="pair-message" role="status"></p>
        <p class="pair-note">
          ${iconMarkup(icons.keyboard)}
          Pairing proves you can see the terminal, so a shared or leaked link is not enough to control your agents.
        </p>
        <button class="btn ghost pair-hosts" type="button">${iconMarkup(icons.server)}Hosts</button>
      </div>
    </main>
  `);
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
