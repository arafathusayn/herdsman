// The pairing screen: the browser opened the URL, and now the person types
// the code that `herdsman web` printed in their terminal.
import { pair } from "../api.ts";
import { icon, type IconNode, icons, logoMark, type LogoNode, mountElement } from "../dom.ts";
import { attr, child, cls, html, on, render, text, type ViewNodeOf } from "../herdsman-blocks-view.ts";
import { openHosts } from "./hosts.ts";

const digits = 6;

// Text in the markup is the elements' text as written, so the templates that
// hold prose keep it on one line: `prettier-ignore` stops bun fmt from
// wrapping it, which would put line breaks and indentation into the text.

// prettier-ignore
const HostsLink = html`<button type="button" class="btn ghost pair-hosts" onclick=${on<"click">()}>${child<IconNode>()}Hosts</button>`;

type HostsLinkNode = ViewNodeOf<typeof HostsLink>;

const hostsLink = (): HostsLinkNode => HostsLink([openHosts, icon(icons.server)]);

// The page is the render container: the screen's own element, not #app, so
// whatever shows in #app next starts from a plain element.
const Page = html`<main class="pair"></main>`;

// The code field is an element the screen owns: it reformats its own value as
// the person types, and the screen clears and focuses it after a wrong code.
const CodeInput = html`<input
  class="pair-code"
  inputmode="numeric"
  autocomplete="one-time-code"
  maxlength=${attr<number>()}
  placeholder="000 000"
  aria-label="Pairing code"
  spellcheck="false"
  oninput=${on<"input", HTMLInputElement>()}
/>`;

// prettier-ignore
const PairCard = html`<div class="pair-card">
  ${child<LogoNode>()}
  <h1>Pair this browser</h1>
  <p class="pair-host">${child<IconNode>()}${text()}</p>
  <p class="pair-lead">Type the 6-digit code that <code>herdsman web</code> just printed in the host's terminal. It expires in 2 minutes.</p>
  <form class="pair-form" onsubmit=${on<"submit">()}>
    ${child<HTMLInputElement>()}
    <button class="btn primary pair-submit" type="submit" disabled=${attr<boolean>()}>Pair this browser</button>
  </form>
  <p class=${cls()} role="status" aria-live="polite">${text()}</p>
  <p class="pair-note">${child<IconNode>()}Pairing proves you can see the terminal, so a shared or leaked link is not enough to control your agents.</p>
  ${child<HostsLinkNode>()}
</div>`;

// prettier-ignore
const EndedCard = html`<div class="pair-card">
  ${child<LogoNode>()}
  <h1>Pairing ended</h1>
  <p class="pair-lead">${text()}</p>
  <p class="pair-lead">Open the URL or scan the QR code from the terminal again to get a new code.</p>
  ${child<HostsLinkNode>()}
</div>`;

/** What the pairing screen shows: the code form, or the end of pairing with its reason. */
export type PairScreenNode = ViewNodeOf<typeof PairCard> | ViewNodeOf<typeof EndedCard>;

export function showPairing(root: HTMLElement, hostName: string, onPaired: (device: string) => void): void {
  let busy = false;
  let complete = false; // the field holds all the digits
  let note = { error: false, text: "" };
  let ended: string | null = null;

  const page = mountElement("main", Page, []);
  const input = mountElement("input", CodeInput, [
    digits + 1,
    function () {
      const code = clean();
      // Show the code as "123 456", the way the terminal prints it.
      this.value = code.length > 3 ? `${code.slice(0, 3)} ${code.slice(3)}` : code;
      complete = code.length === digits;
      draw();
      if (complete) void send();
    },
  ]);

  const clean = (): string => input.value.replace(/\D/g, "").slice(0, digits);

  const draw = (): void =>
    render(
      page,
      ended === null
        ? PairCard([
            logoMark(),
            icon(icons.server),
            hostName,
            (e) => {
              e.preventDefault();
              void send();
            },
            input,
            busy || !complete,
            note.error ? "pair-message error" : "pair-message",
            note.text,
            icon(icons.keyboard),
            hostsLink(),
          ])
        : EndedCard([logoMark(), ended, hostsLink()]),
    );

  const send = async (): Promise<void> => {
    const code = clean();
    if (code.length !== digits || busy) return;
    busy = true;
    note = { error: false, text: "Checking…" };
    draw();
    try {
      const st = await pair(code);
      if (st.status === "paired" && st.device) {
        onPaired(st.device);
        return;
      }
      if (st.status === "none") {
        ended = st.error ?? "This pairing request ended.";
        return;
      }
      note = {
        error: true,
        text: st.left
          ? `${st.error ?? "That code is wrong."} ${st.left} ${st.left === 1 ? "try" : "tries"} left.`
          : (st.error ?? ""),
      };
      input.value = "";
      input.focus();
    } catch (e) {
      note = { error: true, text: e instanceof Error ? e.message : String(e) };
    } finally {
      busy = false;
      complete = clean().length === digits;
      draw();
    }
  };

  draw();
  root.replaceChildren(page);
  input.focus();
}
