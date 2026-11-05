import { effect, For, memo, Show } from "@reactre/core";
import { map } from "@reactre/core/features/lists";
import type { Child } from "@reactre/core/jsx-runtime";

import { Icon } from "../components/icons.tsx";
import { icons } from "../icons.ts";

export interface InputOpts {
  cls?: string;
  placeholder?: string;
  list?: string;
  inputmode?: "numeric";
  maxlength?: number;
}

export function TextInput(props: {
  id: string;
  name: string;
  value: string;
  busy: boolean;
  change: (value: string) => void;
  options?: InputOpts | undefined;
}) {
  return (
    <input
      id={props.id}
      name={props.name}
      class={props.options?.cls ?? ""}
      placeholder={props.options?.placeholder ?? null}
      list={props.options?.list ?? null}
      inputmode={props.options?.inputmode ?? null}
      maxlength={props.options?.maxlength === undefined ? null : String(props.options.maxlength)}
      autocomplete="off"
      value={props.value}
      onInput={(event) => {
        const input = event.currentTarget as HTMLInputElement;
        if (props.busy) input.value = props.value;
        else props.change(input.value);
      }}
    />
  );
}

export function RequestBox(props: { id: string; value: string; busy: boolean; change: (value: string) => void }) {
  let input!: HTMLTextAreaElement;
  const node = (
    <textarea
      id={props.id}
      name="request"
      rows="4"
      placeholder="What should this route do?"
      ref={(element) => {
        input = element;
      }}
      onInput={() => {
        if (props.busy) input.value = props.value;
        else props.change(input.value);
      }}
    />
  );
  effect(() => {
    const value = props.value;
    if (input.value !== value) input.value = value;
  });
  return node;
}

export function SelectBox(props: {
  id: string;
  name: string;
  value: string;
  options: readonly (readonly [string, string])[];
  busy: boolean;
  change: (value: string) => void;
}) {
  const options = memo(() => [...new Map(props.options.map(([value, label]) => [value, label]))]);
  const rows = map(options, (option) => option, { keyed: ([value]) => value });
  return (
    <span class="select">
      <select
        id={props.id}
        name={props.name}
        onChange={(event) => {
          const select = event.currentTarget as HTMLSelectElement;
          if (props.busy) {
            select.value = props.value;
            return;
          }
          const form = select.form,
            focused = document.activeElement === select;
          props.change(select.value);
          if (focused && !select.isConnected)
            form?.querySelector<HTMLElement>(`[name="${props.name.replace(/["\\]/g, "")}"]`)?.focus();
        }}
      >
        <For each={rows()} key={(option) => option()[0]}>
          {(option) => (
            <option value={option()[0]} selected={option()[0] === props.value}>
              {option()[1]}
            </option>
          )}
        </For>
      </select>
      <Icon paths={icons.chevronDown} />
    </span>
  );
}

export function Field(props: {
  id: string;
  name: string;
  label: string;
  children: Child;
  hint?: Child;
  error?: string | undefined;
  wide?: boolean;
  tabsHint?: boolean;
}) {
  return (
    <div class={`field${props.wide ? " wide" : ""}${props.error ? " invalid" : ""}`} data-field={props.name}>
      <label for={props.id}>{props.label}</label>
      <>{props.children}</>
      <Show when={props.hint}>
        <small>
          <Show when={props.tabsHint} fallback={<>{props.hint}</>}>
            <small>{props.hint}</small>
          </Show>
        </small>
      </Show>
      <Show when={props.error}>
        <small class="error" role="alert" data-error={props.name}>
          {props.error}
        </small>
      </Show>
    </div>
  );
}

export function CopyButton(props: { label: string; copy: () => void }) {
  return (
    <button type="button" class="chip-btn" title={props.label} aria-label={props.label} onClick={() => props.copy()}>
      <Icon paths={icons.copy} />
    </button>
  );
}
