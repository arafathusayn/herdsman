import { expect, test } from "bun:test";

import { createRoot } from "@reactre/core/features/owner";

import { bindHost } from "../../sdk/index.ts";
import { executionControls } from "./execution.ts";

test("execution controls survive row disposal and abort with their page", async () => {
  let signal: AbortSignal | null | undefined;
  let finish!: (response: Response) => void;
  let calls = 0;
  let changed = 0;
  bindHost({ url: "http://host", device: "d" }, (_, init) => {
    calls++;
    signal = init.signal;
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  const page = createRoot((dispose) => ({
    controls: executionControls(() => {
      changed++;
    }),
    dispose,
  }));
  try {
    const row = createRoot((dispose) => ({ control: page.controls.get("e1"), dispose }));
    row.control.toggle();
    row.control.edit("retained draft");
    row.dispose();
    const current = page.controls.get("e1");
    expect(current).toBe(row.control);
    expect(current.asking()).toBe(true);
    expect(current.line()).toBe("retained draft");
    current.submit(new Event("submit"));
    current.submit(new Event("submit"));
    await Bun.sleep(0);
    expect(calls).toBe(1);
    expect(current.busy()).toBe(true);
    page.dispose();
    expect(signal?.aborted).toBe(true);
    finish(Response.json({ submitId: "late", state: "typed" }));
    await Bun.sleep(0);
    expect(changed).toBe(0);
    expect(current.done()).toBe("");
  } finally {
    page.dispose();
  }
});
