/** Equality for data already checked by immutableData. Preserve the former
 * wire equality (including property order and -0/0) without encoding strings. */
export function equalData(left: unknown, right: unknown): boolean {
  if (left === right) return true;
  if (left === null || right === null || typeof left !== "object" || typeof right !== "object") return false;
  if (Array.isArray(left) !== Array.isArray(right)) return false;
  const a = Object.keys(left),
    b = Object.keys(right);
  return (
    a.length === b.length &&
    a.every((key, i) => key === b[i] && equalData(Reflect.get(left, key), Reflect.get(right, key)))
  );
}
