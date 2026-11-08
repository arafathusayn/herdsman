// A static server over dist/ on a free port. The runner starts it itself;
// `bun serve.ts` serves dist/ for looking at an app by hand.

import { existsSync, statSync } from "node:fs";
import { join, normalize } from "node:path";

export const distDir = join(import.meta.dir, "dist");

export function serve(port = 0): ReturnType<typeof Bun.serve> {
  return Bun.serve({
    port,
    hostname: "127.0.0.1",
    fetch(req) {
      const path = decodeURIComponent(new URL(req.url).pathname);
      if (path === "/favicon.ico") return new Response(null, { status: 204 });
      let file = normalize(join(distDir, path));
      if (!file.startsWith(distDir)) return new Response("forbidden", { status: 403 });
      if (existsSync(file) && statSync(file).isDirectory()) file = join(file, "index.html");
      if (!existsSync(file)) return new Response("not found", { status: 404 });
      return new Response(Bun.file(file), { headers: { "cache-control": "no-store" } });
    },
  });
}

if (import.meta.main) {
  const s = serve(Number(process.env.PORT ?? 0));
  console.log(`serving ${distDir} at ${s.url}`);
}
