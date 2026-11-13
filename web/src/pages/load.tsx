// What every page shares: one read of the SDK with its loading and failure
// states, and the notice for an action the daemon has no method for yet.
import { Show } from "@reactre/core";
import type { Child } from "@reactre/core/jsx-runtime";

import { Icon } from "../components/icons.tsx";
import { icons } from "../icons.ts";
import { explain, notServed } from "../sdk/index.ts";
import { createRequest } from "../state/request.ts";

export interface Loaded<T> {
  data: () => T | undefined;
  error: () => unknown;
  loading: () => boolean;
  reload: () => Promise<void>;
}

/** Reads now and on each `reload`. Only the newest read may change the page. */
export function load<T>(read: (signal: AbortSignal) => Promise<T>): Loaded<T> {
  const request = createRequest<void, T>((_, signal) => read(signal));
  const reload = async () => {
    await request.run(undefined);
  };
  void reload();
  return {
    data: request.data,
    error: request.error,
    loading: request.loading,
    reload,
  };
}

/** The text of the field an event was bound on. Not for list rows, whose events are delegated. */
export function fieldValue(event: Event): string {
  return (event.currentTarget as HTMLInputElement).value;
}

export function PageFrame(props: { title: string; lead: string; children: Child }) {
  return (
    <section class="page">
      <header class="page-head">
        <h1>{props.title}</h1>
        <p>{props.lead}</p>
      </header>
      {props.children}
    </section>
  );
}

/** Says plainly that this build's daemon has no method for something. */
export function NotServed(props: { what: string; terminal?: string }) {
  return (
    <div class="page-note" role="status">
      <strong>{`${props.what} is not served by this daemon yet.`}</strong>
      <Show when={props.terminal}>
        <span>
          {"From a terminal: "}
          <code>{props.terminal}</code>
        </span>
      </Show>
    </div>
  );
}

export function Failure(props: { error: unknown; what: string; retry?: () => void }) {
  return (
    <Show when={!notServed(props.error)} fallback={<NotServed what={props.what} />}>
      <div class="page-note danger" role="alert">
        <strong>{explain(props.error)}</strong>
        <Show when={props.retry}>
          <button class="btn" type="button" onClick={() => props.retry?.()}>
            <Icon paths={icons.refresh} />
            Try again
          </button>
        </Show>
      </div>
    </Show>
  );
}

/** A page's data, or what stands in for it while there is none. */
export function Async<T>(props: { state: Loaded<T>; what: string; children: (data: T) => Child }) {
  return (
    <Show
      when={props.state.data() !== undefined && props.state.error() === undefined}
      fallback={
        <Show
          when={props.state.error() !== undefined}
          fallback={<div class="spinner" role="status" aria-label="Loading" />}
        >
          <Failure error={props.state.error()} what={props.what} retry={() => void props.state.reload()} />
        </Show>
      }
    >
      {props.children(props.state.data() as T)}
    </Show>
  );
}
