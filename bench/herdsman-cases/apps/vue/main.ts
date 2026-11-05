// Vue 3 (stable) as an expert would write herdsman's screens: one SFC with a
// compiled template, mounted once; render() hands it each fresh snapshot.
import { createApp } from "vue";
import { start } from "../../shared/driver.ts";
import type { State } from "../../shared/state.ts";
import App from "./App.vue";

start((root, onSelect) => {
  const view = createApp(App, { onSelect }).mount(root) as unknown as { render(s: State): void };
  return { render: (s) => view.render(s) };
});
