import { effect, signal } from "@reactre/core";
import { memo } from "@reactre/core/features/memo";
import { createRoot } from "@reactre/core/features/owner";
export { isServer } from "@reactre/core/server";

export function makeMemo() {
  const [value, setValue] = signal(1);
  let runs = 0,
    releases = 0,
    seen = 0;
  const read = memo(
    () => {
      runs++;
      return value() * 2;
    },
    {
      lazy: true,
      unobserved: () => {
        releases++;
      },
    },
  );
  return {
    read,
    setValue,
    get runs() {
      return runs;
    },
    get releases() {
      return releases;
    },
    get seen() {
      return seen;
    },
    subscribe: () =>
      createRoot((dispose) => {
        effect(() => {
          seen = read();
        });
        return dispose;
      }),
  };
}
