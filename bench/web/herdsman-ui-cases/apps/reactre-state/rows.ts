import { memo } from "@reactre/core";

// Snapshots are flat records. Compare every field without allocating
// two key arrays and a callback for each retained row on every refresh.
function equalFields<T extends object>(a: T, b: T): boolean {
  for (const key in a) {
    if (!Object.hasOwn(b, key) || a[key] !== b[key]) return false;
  }
  for (const key in b) {
    if (!Object.hasOwn(a, key)) return false;
  }
  return true;
}

/** Read-only keyed projection of actor data. Equal individual rows retain
 * identity, just as the other entries reconcile their per-key fields. There
 * is no writer or operation-aware shortcut, and every input row is visited. */
export function rows<T extends object>(
  read: () => readonly T[],
  keyOf: (row: T) => string,
) {
  let previous = new Map<string, T>();
  let list: readonly T[] = [];
  return memo(() => {
    const input = read();
    const next = new Map<string, T>();
    const items: T[] = [];
    let same = input.length === list.length;
    for (let i = 0; i < input.length; i++) {
      const value = input[i]!;
      const key = keyOf(value);
      const old = previous.get(key);
      const row = old && equalFields(value, old) ? old : value;
      next.set(key, row);
      items.push(row);
      if (list[i] !== row) same = false;
    }
    previous = next;
    if (!same) list = items;
    return list;
  });
}
