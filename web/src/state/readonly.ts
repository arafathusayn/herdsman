/** A decoded snapshot is a read-only projection, never another mutable store. */
export function readonlyValue<T>(value: T): T {
  if (value === null || typeof value !== "object" || Object.isFrozen(value)) return value;
  const refuse = () => {
    throw new TypeError("Update UI state by sending an actor event");
  };
  if (value instanceof Map) {
    for (const [key, item] of value) {
      readonlyValue(key);
      readonlyValue(item);
    }
    Object.defineProperties(value, { set: { value: refuse }, delete: { value: refuse }, clear: { value: refuse } });
  } else if (value instanceof Set) {
    for (const item of value) readonlyValue(item);
    Object.defineProperties(value, { add: { value: refuse }, delete: { value: refuse }, clear: { value: refuse } });
  } else {
    for (const item of Object.values(value)) readonlyValue(item);
  }
  return Object.freeze(value);
}
