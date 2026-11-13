import { start } from "../../shared/driver.ts";

import { createApp } from "./app.tsx";

start((root, onSelect) => {
  const app = createApp(root, onSelect);
  window.addEventListener("pagehide", app.dispose, { once: true });
  return app;
});
