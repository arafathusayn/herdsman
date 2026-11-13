# Web development

The app builds with Bun 1.4.4. Initialize the dependency once from the repository root:

```sh
git submodule update --init web/vendor/reactre
make web
```

`vendor/reactre` is the upstream Reactre Git submodule, pinned to main commit
`f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27` (compiler hosts, structural JSON data,
static template text holes, lifecycle examples, last-reader allocation reduction
and selector equality fixes, Reactre PRs #2–#6). It retains
upstream's [Apache-2.0 license](vendor/reactre/LICENSE). Core, DOM and state
runtime/compiler sources are read directly; upstream `dist` and `node_modules`
are unused. Do not install or build the upstream checkout.

The state compiler's OXC 0.156.0 parser is also vendored. The build and checks
install only local file dependencies with `bun install --offline --frozen-lockfile`.
No registry or warmed cache is needed. See [parser provenance and updates](vendor/oxc/VENDOR.md)
for licenses, checksums, source revision and supported build hosts.

## Author components in TSX

Write `.tsx` files with normal Reactre imports:

```tsx
import { valueState } from "./state/value.ts";

export function Counter() {
  const [count, setCount] = valueState(0);
  return <button onClick={() => setCount((n) => n + 1)}>{count()}</button>;
}
```

Mount a component with `render(Component, element)`, or `attach(Component, element)`
when the caller needs a disposer. Components run once; actors own changing values,
while memos derive values and effects connect resources. The TSX compiler creates templates and bindings
during `make web`. Author no generated JavaScript or template helper calls.

`tsconfig.json` resolves Reactre's types to source. `reactre.ts` resolves the
runtime's imports to source too, including from inside the submodule's own
TypeScript config, and loads the public upstream `reactreCompiler` plugin. Its thin
`sourceCompiler` adapter supplies `analysis.resolve`: plain imported
data must not be treated as possible stores because package resolution failed.
The same analysis selects the core runtime features. Builds fail if the graph
cannot be resolved (`analysis.strict`); runtime tests without entrypoints use
per-file analysis. The resolver distinguishes source paths, explicitly external
imports (`false`) and unresolved imports (`null`). Upstream owns graph analysis,
diagnostics, runtime selection and compilation; HTML entries were already supported.
The compiled integration test checks that selecting a row does not re-read its
static label. `build.ts` builds both
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
excluded from the app's checks.

## Application ownership and state

The app, dialogs, route creation, terminal panel and docs controls use native
Reactre TSX components. All application and UI state belongs to `@reactre/state`
actors. `app-state.ts` exposes the shared snapshot; `state/value.ts` owns local
data; `state/request.ts` owns async phases/results. Specialized `.machine.ts`
modules model workflows. Core signals in the three subscription adapters only
notify readers. Never add a competing store, context container or direct core
signal to a component.

Cached controllers use explicit `controllerScope` owners. Tab exit cancels work;
replacing a controller disposes it. Requests abort on replacement/cleanup and
drop late results. URL/browser storage are persistence boundaries. The application
snapshot uses `app.machine.ts` with typed nested JSON payloads, including optional
SDK fields and string-keyed records. `immutableData` copies/freezes incoming patches;
unchanged domains retain identity and equal patches do not notify. No string
encode/decode or decoded memo is needed on that path. Collection-valued local
state and generic requests still use `wire`: Map/Set, bigint, explicit undefined
and SDK error classes need its lossless representation. Unknown data still needs
schema validation before the immutable boundary. Restored JSON is copied/frozen
again at app initialization; persisted machine-format changes need migrations.
See [the ownership inventory and statecharts](../docs/adr/0036-actor-owned-web-state.md)
for cleanup rules, diagrams and tradeoffs.

Use `<For>` for keyed DOM lists. When server snapshots replace row objects,
use Reactre's keyed `map` to expose a stable accessor per row, then render those
accessors through `<For>` so changes update the existing component. This keeps
focused fields, open details, scroll positions and loaded preview frames.
Wrap nullable state in `<Show>` and read its child accessor inside that owner.

`test-dom-core.ts` and `testdom.ts` provide the dependency-free fake DOM;
`test-mount.ts` mounts components with automatic cleanup. `make web-check`
runs the normal and additional strict app checks, the compiler integration
tests and app regressions. `make web-e2e` also builds and checks the app in
headless Chrome, including typing, keyboard navigation, polling, preview
sandboxing, terminal input, route creation and the docs controls.

The retired renderer is preserved only under `bench/web/herdsman-ui-cases/legacy`
for the existing benchmark baselines. Its strict type checks and unit/property
tests still run in `make web-check`; production app code does not import it.

## Update Reactre

Fetch upstream in `vendor/reactre`, check out a reviewed exact commit there, then
stage the new submodule commit in the parent repository. Update the version
and commit documented here, review the state compiler's parser requirement,
and run `make web-check web-e2e build` before committing.
After pulling a parent commit that updates Reactre, run
`git submodule update --init web/vendor/reactre` to use its recorded version.
