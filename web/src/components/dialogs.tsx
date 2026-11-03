import { attach, For, Show } from "@reactre/core";
import type { Child } from "@reactre/core/jsx-runtime";

import { dismissOnBackdrop } from "../dialog-backdrop.ts";

function openDialog(content: () => Child, closed: (dialog: HTMLDialogElement) => void): void {
  let dialog!: HTMLDialogElement;
  let dispose = (): void => {};
  let finished = false;
  dispose = attach(
    () => (
      <dialog
        class="dialog"
        ref={(element) => {
          dialog = element;
        }}
        onClose={() => {
          if (finished) return;
          finished = true;
          try {
            closed(dialog);
          } finally {
            dispose();
          }
        }}
      >
        <>{content()}</>
      </dialog>
    ),
    document.body,
  );
  dismissOnBackdrop(dialog);
  dialog.showModal();
}

export function confirmDialog(title: string, body: string, confirmLabel: string, danger = false): Promise<boolean> {
  return new Promise((resolve) => {
    openDialog(
      () => (
        <form method="dialog">
          <h2>{title}</h2>
          <p>{body}</p>
          <div class="dialog-actions">
            <button value="cancel" class="btn ghost">
              Cancel
            </button>
            <button value="ok" class={danger ? "btn danger" : "btn primary"} autofocus>
              {confirmLabel}
            </button>
          </div>
        </form>
      ),
      (dialog) => resolve(dialog.returnValue === "ok"),
    );
  });
}

export interface Field {
  name: string;
  label: string;
  value?: string;
  placeholder?: string;
  hint?: string;
  list?: string[];
  required?: boolean;
  pattern?: string;
}

function FieldInput(props: { field: Field }) {
  const field = props.field;
  const id = `f-${field.name}-${Math.random().toString(36).slice(2, 8)}`;
  const options = [...new Set(field.list ?? [])];
  const listId = options.length ? `${id}-list` : null;
  return (
    <label class="field" for={id}>
      <span>{field.label}</span>
      <input
        id={id}
        name={field.name}
        value={field.value ?? ""}
        placeholder={field.placeholder ?? null}
        required={field.required ?? false}
        pattern={field.pattern ?? null}
        autocomplete="off"
        spellcheck="false"
        list={listId}
      />
      <Show when={options.length > 0}>
        <datalist id={listId}>
          <For each={options} key={(option) => option}>
            {(option) => <option value={option} />}
          </For>
        </datalist>
      </Show>
      <Show when={field.hint}>
        <small>{field.hint}</small>
      </Show>
    </label>
  );
}

/** Native dialog form submission; Reactre owns every element and cleanup. */
export function formDialog(
  title: string,
  fields: Field[],
  submitLabel: string,
): Promise<Record<string, string> | null> {
  return new Promise((resolve) => {
    openDialog(
      () => (
        <form method="dialog">
          <h2>{title}</h2>
          <For each={fields} key={(field) => field.name}>
            {(field) => <FieldInput field={field} />}
          </For>
          <div class="dialog-actions">
            <button value="cancel" class="btn ghost" formnovalidate>
              Cancel
            </button>
            <button value="ok" class="btn primary">
              {submitLabel}
            </button>
          </div>
        </form>
      ),
      (dialog) => {
        const form = dialog.querySelector("form");
        resolve(
          dialog.returnValue !== "ok" || !form
            ? null
            : Object.fromEntries([...new FormData(form)].map(([key, value]) => [key, String(value)])),
        );
      },
    );
  });
}
