// Vue 3.6 Vapor mode: one `<script setup vapor>` SFC mounted with
// createVaporApp, so the vdom runtime is never used; render() hands it each
// fresh snapshot.
import { createVaporApp } from "vue";
import { start } from "../../shared/driver.ts";
import type { State } from "../../shared/state.ts";
import App from "./App.vue";

start((root, onSelect) => {
  // Vapor reads a component's raw props through getters, the root's included.
  const view = createVaporApp(App, { onSelect: () => onSelect }).mount(root) as unknown as { render(s: State): void };
  return { render: (s) => view.render(s) };
});
