import { attach, For, selector, Show, signal } from "@reactre/core";

export function mount(root: Element) {
  const [count, setCount] = signal(0);
  const [rows, setRows] = signal([
    { id: 1, label: "one" },
    { id: 2, label: "two" },
  ]);
  const [selected, setSelected] = signal(1);
  const isSelected = selector(selected);
  const App = () => (
    <section>
      <button onClick={() => setCount((n) => n + 1)}>{count()}</button>
      <Show when={count() > 0}>
        <p>updated</p>
      </Show>
      <ul>
        <For each={rows()} key={(row) => row.id}>
          {(row) => <li class={isSelected(row.id) ? "selected" : ""}>{row.label}</li>}
        </For>
      </ul>
    </section>
  );
  const dispose = attach(App, root);
  return { dispose, setCount, setRows, setSelected };
}
