// Server HTML for the hydration scenario, rendered by React's own server
// renderer in the browser (built against React only).
import { renderToString } from "react-dom/server.browser";
import { HydrateApp } from "./cases.tsx";

(window as unknown as { __ssr: Record<string, string> }).__ssr = { hydrate: renderToString(<HydrateApp />) };
