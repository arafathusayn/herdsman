import { createRoot, runWithOwner } from "@reactre/core/features/owner";

/** Cached controllers outlive a tab. Their caller must dispose the scope when
 * replacing the controller; hiding a tab only cancels its active work. */
export function controllerScope<T extends object>(create: () => T): T & { dispose(): void } {
  return runWithOwner(null, () => createRoot((dispose) => ({ ...create(), dispose })));
}
