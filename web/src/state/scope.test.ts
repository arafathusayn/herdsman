import { expect, test } from "bun:test";

import { createRoot } from "@reactre/core/features/owner";

import { createRequest } from "./request.ts";
import { controllerScope } from "./scope.ts";
import { valueState } from "./value.ts";

test("cached controller survives tab ownership and disposes its active work explicitly", async () => {
  const tab = createRoot((dispose) => ({
    dispose,
    controller: controllerScope(() => {
      const [read, write] = valueState("before");
      const request = createRequest(
        async (_: void, signal) =>
          new Promise<string>((_, reject) => {
            signal.addEventListener("abort", () => reject(signal.reason), { once: true });
          }),
      );
      return { read, write, request };
    }),
  }));
  tab.dispose();
  tab.controller.write("after");
  expect(tab.controller.read()).toBe("after");
  const pending = tab.controller.request.run(undefined);
  expect(tab.controller.request.loading()).toBe(true);
  tab.controller.dispose();
  expect(await pending).toEqual({ status: "cancelled" });
  expect(await tab.controller.request.run(undefined)).toEqual({ status: "cancelled" });
});
