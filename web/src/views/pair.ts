// The pairing screen: the browser opened the URL, and now the person types
// the code that `herdsman web` printed in their terminal.
import { pair } from "../api.ts";
import { h, icon, icons, logoMark, mount } from "../dom.ts";
import { openHosts } from "./hosts.ts";

const hostsLink = (): HTMLElement =>
  h("button", { type: "button", class: "btn ghost pair-hosts", onclick: openHosts }, icon(icons.server), "Hosts");

const digits = 6;

export function showPairing(root: HTMLElement, hostName: string, onPaired: (device: string) => void): void {
  const input = h("input", {
    class: "pair-code",
    inputMode: "numeric",
    autocomplete: "one-time-code",
    maxLength: digits + 1,
    placeholder: "000 000",
    "aria-label": "Pairing code",
    spellcheck: false,
  });
  const message = h("p", {
    class: "pair-message",
    role: "status",
    "aria-live": "polite",
  });
  const submit = h("button", { class: "btn primary pair-submit", type: "submit", disabled: true }, "Pair this browser");
  let busy = false;

  const clean = () => input.value.replace(/\D/g, "").slice(0, digits);

  const ended = (text: string): void =>
    mount(
      card,
      logoMark(),
      h("h1", null, "Pairing ended"),
      h("p", { class: "pair-lead" }, text),
      h("p", { class: "pair-lead" }, "Open the URL or scan the QR code from the terminal again to get a new code."),
      hostsLink(),
    );

  const send = async (): Promise<void> => {
    const code = clean();
    if (code.length !== digits || busy) return;
    busy = true;
    submit.disabled = true;
    message.className = "pair-message";
    message.textContent = "Checking…";
    try {
      const st = await pair(code);
      if (st.status === "paired" && st.device) {
        onPaired(st.device);
        return;
      }
      if (st.status === "none") {
        ended(st.error ?? "This pairing request ended.");
        return;
      }
      message.className = "pair-message error";
      message.textContent = st.left
        ? `${st.error ?? "That code is wrong."} ${st.left} ${st.left === 1 ? "try" : "tries"} left.`
        : (st.error ?? "");
      input.value = "";
      input.focus();
    } catch (e) {
      message.className = "pair-message error";
      message.textContent = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
      submit.disabled = clean().length !== digits;
    }
  };

  input.addEventListener("input", () => {
    const code = clean();
    // Show the code as "123 456", the way the terminal prints it.
    input.value = code.length > 3 ? `${code.slice(0, 3)} ${code.slice(3)}` : code;
    submit.disabled = code.length !== digits;
    if (code.length === digits) void send();
  });

  const form = h(
    "form",
    {
      class: "pair-form",
      onsubmit: (e: Event) => {
        e.preventDefault();
        void send();
      },
    },
    input,
    submit,
  );
  const card = h(
    "div",
    { class: "pair-card" },
    logoMark(),
    h("h1", null, "Pair this browser"),
    h("p", { class: "pair-host" }, icon(icons.server), hostName),
    h(
      "p",
      { class: "pair-lead" },
      "Type the 6-digit code that ",
      h("code", null, "herdsman web"),
      " just printed in the host's terminal. It expires in 2 minutes.",
    ),
    form,
    message,
    h(
      "p",
      { class: "pair-note" },
      icon(icons.keyboard),
      "Pairing proves you can see the terminal, so a shared or leaked link is not enough to control your agents.",
    ),
    hostsLink(),
  );
  mount(root, h("main", { class: "pair" }, card));
  input.focus();
}
