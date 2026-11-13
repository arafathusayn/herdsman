import { memo } from "@reactre/core";

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
      let row = value;
      if (old) {
        const fields = Object.keys(value) as (keyof T)[];
        if (
          fields.length === Object.keys(old).length &&
          fields.every((field) => old[field] === value[field])
        )
          row = old;
      }
      next.set(key, row);
      items.push(row);
      if (list[i] !== row) same = false;
    }
    previous = next;
    if (!same) list = items;
    return list;
  });
}
