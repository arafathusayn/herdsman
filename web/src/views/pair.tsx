import { attach, onCleanup, Show } from "@reactre/core";
import { createActor } from "@reactre/state";

import { pair } from "../api.ts";
import { Icon, Logo } from "../components/icons.tsx";
import { icons } from "../icons.ts";
import { observeActor } from "../state/observe.ts";
import { pairingMachine } from "../state/pairing.machine.ts";
import { registerRequest } from "../state/request-service.ts";
import { encodeValue } from "../state/wire.ts";
import { openHosts } from "./hosts.tsx";

const digits = 6;

function HostsLink() {
  return (
    <button type="button" class="btn ghost pair-hosts" onClick={openHosts}>
      <Icon paths={icons.server} />
      Hosts
    </button>
  );
}

/** Mount the pairing flow once; its fields, status and terminal state follow signals. */
export function showPairing(
  root: HTMLElement,
  hostName: string,
  onPaired: (device: string) => void,
  request: typeof pair = pair,
): () => void {
  const service = registerRequest(async (code, signal) => encodeValue(await request(code, signal)));
  const actor = createActor(pairingMachine, { input: { service: service.id } });
  const observed = observeActor(actor);
  const state = observed.read;
  const busy = () => state().state === "checking";
  const code = () => {
    const s = state();
    return s.state === "editing" || s.state === "checking" ? s.data.code : "";
  };
  const complete = () => code().length === digits;
  const ended = () => {
    const s = state();
    return s.state === "ended" ? s.data.message : null;
  };
  const note = () => {
    const s = state();
    return s.state === "editing"
      ? { error: s.data.error, text: s.data.message }
      : { error: false, text: busy() ? "Checking…" : "" };
  };
  let input: HTMLInputElement;
  const off = actor.subscribe((snapshot) => {
    if (snapshot.state === "paired") onPaired(snapshot.data.device);
    else if (snapshot.state === "editing" && snapshot.data.error) input?.focus();
  });
  const send = (): void => {
    actor.send({ type: "submit" });
  };

  const View = () => {
    onCleanup(() => {
      off();
      observed.stop();
      service.dispose();
    });
    return (
      <main class="pair">
        <Show
          when={ended() === null}
          fallback={
            <div class="pair-card">
              <Logo />
              <h1>Pairing ended</h1>
              <p class="pair-lead">{ended()}</p>
              <p class="pair-lead">Open the URL or scan the QR code from the terminal again to get a new code.</p>
              <HostsLink />
            </div>
          }
        >
          <div class="pair-card">
            <Logo />
            <h1>Pair this browser</h1>
            <p class="pair-host">
              <Icon paths={icons.server} />
              {hostName}
            </p>
            <p class="pair-lead">
              Type the 6-digit code that <code>herdsman web</code> just printed in the host's terminal. It expires in 2
              minutes.
            </p>
            <form
              class="pair-form"
              onSubmit={(event) => {
                event.preventDefault();
                void send();
              }}
            >
              <input
                class="pair-code"
                inputmode="numeric"
                autocomplete="one-time-code"
                maxlength={digits + 1}
                placeholder="000 000"
                aria-label="Pairing code"
                spellcheck="false"
                ref={(element) => {
                  input = element;
                }}
                value={code().length > 3 ? `${code().slice(0, 3)} ${code().slice(3)}` : code()}
                onInput={() => {
                  actor.send({ type: "edit", code: input.value.replace(/\D/g, "").slice(0, digits) });
                  if (complete()) send();
                }}
              />
              <button class="btn primary pair-submit" type="submit" disabled={busy() || !complete()}>
                Pair this browser
              </button>
            </form>
            <p class={note().error ? "pair-message error" : "pair-message"} role="status" aria-live="polite">
              {note().text}
            </p>
            <p class="pair-note">
              <Icon paths={icons.keyboard} />
              Pairing proves you can see the terminal, so a shared or leaked link is not enough to control your agents.
            </p>
            <HostsLink />
          </div>
        </Show>
      </main>
    );
  };

  root.replaceChildren();
  const dispose = attach(View, root);
  input!.focus();
  return dispose;
}
