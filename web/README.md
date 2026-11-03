# Web development

The app builds with Bun 1.4.4. Initialize the dependency once from the repository root:

```sh
git submodule update --init web/vendor/reactre
make web
```

`vendor/reactre` is the upstream Reactre Git submodule, pinned to **v0.0.4**,
commit `2f72c572568b8bbc704cc65a1ec1fabd0ae5e57b`. It retains upstream's
[Apache-2.0 license](vendor/reactre/LICENSE). There is no web package manifest
or package install. The build reads `packages/core/src`, `packages/dom/src`,
and `packages/compiler/src` directly from the submodule. Do not run its build
or install its dependencies; its `dist` and `node_modules` are unused.

## Author components in TSX

Write `.tsx` files with normal Reactre imports:

```tsx
import { signal } from "@reactre/core";

export function Counter() {
  const [count, setCount] = signal(0);
  return <button onClick={() => setCount((n) => n + 1)}>{count()}</button>;
}
```

Mount a component with `render(Component, element)`, or `attach(Component, element)`
when the caller needs a disposer. Components run once; use signals for values
that change. Reactre's TSX compiler creates templates and reactive bindings
during `make web`. Author no generated JavaScript or template helper calls.

`tsconfig.json` resolves Reactre's types to source. `reactre.ts` resolves the
runtime's imports to source too, including from inside the submodule's own
TypeScript config, and loads the upstream compiler. `build.ts` builds both
HTML pages into `internal/web/dist` for Go to embed. The browser receives the
normal JavaScript bundle; Reactre does not need a separate build.

`make web-check` checks TSX types and runs the app tests. The test preload
registers Reactre's compiler and loads the runtime's TypeScript sources, so
tests can import and render `.tsx` components directly. Application `.ts`
modules are compiled too, including `features/memo`'s `lazy` and `unobserved`
options. Vendored modules are excluded from the application compiler filter.
`@reactre/core/server` resolves to its browser implementation in builds and tests.

`build.test.ts`
compiles a TSX fixture with the same plugins as the production build, then
tests its events, reactive updates, keyed node reuse and disposal on the
existing fake DOM. The browser page checks use the same plugins and include
a TSX regression page. Upstream submodule tests and formatting are
excluded from the app's checks. The existing views still use herdsman-blocks;
they have not been migrated.

## Update Reactre

Fetch upstream tags in `vendor/reactre`, check out a reviewed tag there, then
stage the new submodule commit in the parent repository. Update the version
and commit documented here and run `make web-check web` before committing.
After pulling a parent commit that updates Reactre, run
`git submodule update --init web/vendor/reactre` to use its recorded version.
