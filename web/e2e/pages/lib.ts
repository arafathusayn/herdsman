// What a page script uses to report: check() records a result, done() hands
// them to the runner (pages.ts), which waits for window.__results.
const results: { name: string; ok: boolean; detail: string }[] = [];

export function check(name: string, ok: boolean, detail = ""): void {
  results.push({ name, ok, detail });
}

/** Checks that two values are equal, with both in the detail when not. */
export function equal(name: string, got: unknown, want: unknown): void {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  check(name, g === w, `got ${g}, want ${w}`);
}

export function done(): void {
  (window as unknown as { __results: typeof results }).__results = results;
}

/** Runs the page's checks, records a throw as a failure, and reports. */
export async function run(checks: () => Promise<void> | void): Promise<void> {
  try {
    await checks();
  } catch (e) {
    check("threw", false, e instanceof Error ? (e.stack ?? e.message) : String(e));
  }
  done();
}

/** Sets a field's value as typing does and fires its input event. */
export function typeIn(el: HTMLInputElement | HTMLTextAreaElement, v: string): void {
  el.value = v;
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

/** Picks a select's option and fires its change event. */
export function pick(el: HTMLSelectElement, v: string): void {
  el.value = v;
  el.dispatchEvent(new Event("change", { bubbles: true }));
}

export const flush = (): Promise<void> => new Promise((r) => setTimeout(r, 0));

/** The element that matches, or a thrown error naming the selector. */
export function q<E extends Element = HTMLElement>(root: ParentNode, sel: string): E {
  const el = root.querySelector<E>(sel);
  if (!el) throw new Error(`nothing matches ${sel}`);
  return el;
}
