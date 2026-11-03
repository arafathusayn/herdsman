import { attach, batch, onCleanup, Show, signal } from "@reactre/core";

import { pair } from "../api.ts";
import { Icon, Logo } from "../components/icons.tsx";
import { icons } from "../icons.ts";
import { openHosts } from "./hosts.ts";

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
  const [busy, setBusy] = signal(false);
  const [complete, setComplete] = signal(false);
  const [note, setNote] = signal({ error: false, text: "" });
  const [ended, setEnded] = signal<string | null>(null);
  let input: HTMLInputElement;
  let disposed = false;
  const clean = (): string => input.value.replace(/\D/g, "").slice(0, digits);

  const send = async (): Promise<void> => {
    const code = clean();
    if (code.length !== digits || busy() || disposed) return;
    batch(() => {
      setBusy(true);
      setNote({ error: false, text: "Checking…" });
    });
    try {
      const status = await request(code);
      if (disposed) return;
      if (status.status === "paired" && status.device) {
        onPaired(status.device);
        return;
      }
      if (status.status === "none") {
        setEnded(status.error ?? "This pairing request ended.");
        return;
      }
      setNote({
        error: true,
        text: status.left
          ? `${status.error ?? "That code is wrong."} ${status.left} ${status.left === 1 ? "try" : "tries"} left.`
          : (status.error ?? ""),
      });
      input.value = "";
      input.focus();
    } catch (error) {
      if (!disposed) setNote({ error: true, text: error instanceof Error ? error.message : String(error) });
    } finally {
      if (!disposed)
        batch(() => {
          setBusy(false);
          setComplete(clean().length === digits);
        });
    }
  };

  const View = () => {
    onCleanup(() => {
      disposed = true;
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
                onInput={() => {
                  const code = clean();
                  input.value = code.length > 3 ? `${code.slice(0, 3)} ${code.slice(3)}` : code;
                  setComplete(code.length === digits);
                  if (complete()) void send();
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
