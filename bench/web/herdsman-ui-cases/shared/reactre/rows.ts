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

/** Read-only keyed projection of snapshot data. Equal individual rows retain
 * identity, just as the other entries reconcile their per-key fields. There
 * is no writer or operation-aware shortcut. Unchanged snapshots still compare
 * every row, but need neither new keys nor a replacement map. */
export function rows<T extends object>(
  read: () => readonly T[],
  keyOf: (row: T) => string,
) {
  let previous: Map<string, T> | undefined;
  let list: readonly T[] | undefined;
  return memo(() => {
    const input = read();
    // A newly mounted view has no old rows to reconcile. Keep an array copy,
    // but defer the key index until an update actually changes its data.
    if (list === undefined) return (list = input.slice());
    if (
      input.length === list.length &&
      input.every((row, i) => equalFields(row, list![i]!))
    )
      return list;
    previous ??= new Map(list.map((row) => [keyOf(row), row]));
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
