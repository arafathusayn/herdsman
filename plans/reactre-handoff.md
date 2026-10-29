# reactre: handoff of the remaining work

Status as of 2026-10-29, branch `ccr-ca4b6b2d-jtluel`. First written 2026-10-28; the section "Status at pause" below supersedes the measurement tables where they differ. This note is for whoever picks reactre up next: what exists, what is measured, what is not done, and how to check each item.

## Status at pause (2026-10-29): where to resume

### Standings in the seven-entry field

The field is the top 5, reactre and reactre-compat. Logs are committed in `bench/herdsman-cases/jfb/logs/`, so pool them with `bun jfb/pool.ts <logs>` from `bench/herdsman-cases`. Scores per layout:

| Entry | Interleaved, 5 runs pooled¹ | Official layout, 4 runs pooled² | All 9 runs |
|---|---|---|---|
| vanillajs-lite | 1.027 (1st) | 1.011 (1st) | 1.012 |
| **reactre** | **1.029 (2nd)** | **1.029 (2nd)** | **1.025 (2nd)** |
| thane | 1.033 | 1.043 | 1.029 |
| re | 1.035 | 1.038 | 1.032 |
| doohtml-dp | 1.051 | 1.058 (6th) | 1.047 |
| **reactre-compat** | **1.055 (6th)** | **1.067 (7th)** | **1.057 (7th)** |
| vanillajs | 1.071 (7th) | 1.049 | 1.049 (6th) |

¹ `jfb-f7a`, `f7b`, `f7i1`, `f7i2`, `f7i4`; 75 samples per test and entry.
² `jfb-f7o0`, `f7o3`, `f7o1`, `f7o4`; 60 samples per test and entry.

Against the goals:

- **reactre: top 3 met in both layouts** (2nd in each pool). Per run it placed 2nd, 2nd, 3rd, 4th, 3rd (interleaved) and 4th, 2nd, 1st, 2nd (official).
- **reactre-compat: top 6 met in the interleaved pool only.** It is 7th in the official-layout pool, 0.009 behind doohtml-dp, and 7th overall. Per run it ranged from 1st to 7th.

### Where reactre-compat loses (official-layout pool, against doohtml-dp in 6th)

| Test | compat | doohtml-dp | Weighted gap |
|---|---|---|---|
| clear (×0.42) | 1.13 | 1.04 | ≈ 0.008 of the 0.009 total |
| select (×0.19) | 1.12 | 1.05 | |
| swap (×0.13) | 1.09 | 1.01 | |
| update (×0.56) | 1.07 | 1.05 | |

**Clear is the lever.** Compat's clear script in the window comparison was 44–54 ms against about 42 for reactre and doohtml-dp. Its native clear measured directly (`jfb/native-clear.ts`) was 31.0 ms, against reactre 30.3 and vanillajs 27.1. So most of compat's extra is around the native removal, not in it.

### Tried since the first handoff, and the result

- **Merging `removeAll`'s two loops into one pass** (commit `b0eda87`): A/B on clear showed no proven difference (script 41.5 against 41.7 ms). Reverted in `1e68de4`.
- **Maglev.** `--no-maglev` lowered compat's clear script from 44.3 to 40.2 ms. However, `--trace-opt` and `--trace-deopt` (via the uncommitted scratch script `jfb/.cache/tier.ts`) showed **no compilation or deopt of our functions inside the measured clear**. The Maglev effect is elsewhere, perhaps code installed during warmup. Not a lead yet.
- **Interrupted: per-row expandos on compat's rows.** The plan was to build a variant of reactre-compat with no `FIBER` expando on keyed list rows (keeping `OWN`, which `place()` needs to recognise its nodes). It would be copied to a temporary entry `jfb/.cache/.../frameworks/keyed/compat-noexp`, and `jfb/native-clear.ts` would compare it with `reactre-compat` and `vanillajs`.
  - Every attempt failed with a timeout waiting for rows (`rowsThere`).
  - The last step, running `native-clear.ts` on each entry alone to find which page fails, was not run.
  - First suspects: the copied entry itself (its `index.html` and `dist`), or `native-clear.ts` with these pages after the restart. It smoke-tested fine earlier on vanillajs and herdsman-blocks.
  - The working tree was restored to the committed `herdsman-react.ts`. Dropping `FIBER` would also need a new way for event dispatch to find a row's fiber (a scan of the list, as reactre and the blocks app do).

### Next steps, in order

1. Find why `native-clear.ts` timed out (run it on each entry alone). Then measure the no-`FIBER` variant's native clear and in-benchmark clear script. If it closes clear's gap, implement dispatch without the expando, A/B it, and rerun the official-layout pool.
2. If expandos aren't it, profile compat's clear window against reactre's event by event (`jfb/window.ts reactre-compat,reactre 09_clear1k_x8 20 --events --save`), opening the saved traces in DevTools. That shows what else runs in compat's click besides the removal.
3. Re-pool after any change: at least 4 official-layout and 5 interleaved seven-entry runs. Done when reactre-compat is 6th or better in both.
4. Only then rewrite `jfb/REPORT.md` around reactre, per the instruction that the report names reactre only once its results are complete.

## Goals, as set

| Entry | What it is | Target in the js-framework-benchmark replica |
|---|---|---|
| **reactre** | Declarative TSX over signals, compiled to herdsman-blocks templates. Not React's API. | **Top 3** among the official keyed top 5 |
| **reactre with reactre-compat** | React's API, so an unmodified React app runs on it | **Top 6** in the field of the top 5, reactre and reactre-compat; that is, ahead of at least one official entry |

React compatibility is checked by `bench/herdsman-cases/react-compat.ts`, which compares each scenario with React 19 itself.

`jfb/REPORT.md` deliberately does **not** mention reactre until its results are complete; see "Report" below.

## What exists

| File | What it is |
|---|---|
| `web/src/reactre.ts` | The runtime (~550 lines). `signal`, `effect`, `memo`, `batch`, `untrack`, `selector`, `render`, and `For` (a compile-time form). Compiler internals: `__template`, `__for`, `__insert`, `__bind`, `__slot`. |
| `web/src/reactre-compiler.ts` | Bun plugin `reactreCompiler()` and `compileSource()`. Bun transpiles TSX to automatic-runtime `jsx()` calls, then every call is compiled; nothing is left to a JSX runtime. |
| `web/src/reactre.test.ts` | 9 tests on the fake DOM. They cover reactive holes, components running once, slots, batch, memo, untrack, `<For>` (order, node reuse, swap, item update), the selector touching two rows, delegated handlers, signal-reading rows and their disposal, and compile errors. |
| `web/src/reactre-compat.ts`, `reactre-compat-classes.ts` | React's API. These re-export herdsman-react (and its class build). There is **no new implementation**: it is herdsman-react's React layer under the reactre name. |
| `web/src/herdsman-blocks.ts` | The template and keyed-list layer under reactre. Added for it: `List`'s `remove` hook, `skipUnchanged`, fragment insertion, and diffing before allocating. |
| `bench/herdsman-cases/jfb/app/reactre.tsx` | The benchmark app in reactre. |
| `bench/herdsman-cases/jfb/prepare.ts` | Builds the `reactre` entry (compiler plus an alias for `reactre`) and the `reactre-compat` entry: the idiomatic React app `jfb/app/main.jsx`, aliased to reactre-compat and compiled by herdsman-react-compiler. |
| `bench/herdsman-cases/react-alias.ts`, `react-compat.ts` | Know the `reactre-compat` runtime. |

### Model, in short

- **Components run once.** A hole whose expression contains a call (a signal read, most often) becomes an effect; any other hole is written once, at clone time.
- **`<For each key>{(row) => <tr/>}</For>`** becomes a keyed `List`, with `owns` and `detach` when the list is its parent's only content, and `skipUnchanged` always. Row holes are split by what they read:
  - **row only** (`row.label`): written at creation, compared when a key gets a new item;
  - **call** (`isSelected(row.id)`): re-run under a row context. A selector read records the key on the row (one field, no subscription); a signal read makes the row an observer, dropped when the row leaves.
- **Row events are delegated.** There is one listener per event type on the list's parent. It finds the row by scanning the list's entries, then the handler by the element's hole. Handlers are compiled as `(row) => handler`, evaluated at click time.

## What is measured

| Measurement | Result | Status |
|---|---|---|
| Unit tests | 326 of 326 across the web app (9 of them reactre's) | Done |
| React compatibility of reactre-compat | 36/36 with classes, with and without the compiler; 29/36 without classes (by design) | Done |
| reactre against the top 5, interleaved (`--batch 3`), **2 runs pooled** | **1st, 1.030**; vanillajs-lite 1.044, thane 1.045, re 1.046, doohtml-dp 1.056, vanillajs 1.063. Per run: 2nd, 4th. | **Too few runs.** The first run had a two-minute compatibility check overlapping it. |
| Script time against herdsman-blocks (`window.ts --kinds`) | create 6.4 ms against 9.1, append 7.1 against 6.8, swap 2.5 against 2.0, create-many 60.6 against 70.4, update 5.3 against 4.3 (after the per-hole locator) | Indicative, single runs |
| reactre-compat in any field | Not measured. As herdsman-react it was 6th of 6 in every earlier pool: 1.072 interleaved and 1.089 official layout, about 1–3% behind the slowest official entry. | **Open** |
| Seven-entry field (top 5 + reactre + reactre-compat) | 5 interleaved and 4 official-layout runs; see "Status at pause". Logs in `bench/herdsman-cases/jfb/logs/`. | Done |

## Remaining work

Each item says what done looks like. In rough order of what the goals need.

### 1. Place both entries properly

- **Run the seven-entry field to a stable pool.** Do at least 5 interleaved runs and at least 4 official-layout runs, with different `--shift` values:

  ```sh
  cd bench/herdsman-cases
  E=re,thane,vanillajs-lite,vanillajs,doohtml-dp,reactre,reactre-compat
  bun jfb/run.ts --entries $E --batch 3 --shift <s> > f7-int-<s>.log      # s = 0 1 2 3 4
  bun jfb/run.ts --entries $E --shift <s> > f7-off-<s>.log                # s = 0 3 1 4
  bun jfb/pool.ts f7-int-0.log,f7-int-1.log,...   # and likewise for the official-layout logs
  ```

  A seven-entry run takes about 2 hours; `run.ts` has no timeout of its own, so mind any outer one. Keep the machine otherwise idle; drop or rerun any run that overlapped other work.
- **Done when** both pools put reactre in the top 3, and reactre-compat ahead of at least one official entry. Commit the logs (e.g. `bench/herdsman-cases/results/`) so the numbers survive.

### 2. reactre-compat's top-6 gap (likely the hard part)

It has been about 1–3% behind the slowest official entry. Known causes, from profiles of the measured click (`plans/herdsman-blocks.md`, second to fourth rounds):
- React's semantics on each click: re-rendering `Main` and comparing 1,000 memoized rows on select, plus synthetic events and the update queue;
- V8 tiering of once-per-update functions landing on the measured click.

Candidates not yet tried:

- **Slimmer fibers.** A row fiber carries about 22 fields, several unused by rows (`rx`, `tx`, `hc`, `x`). Moving cold fields into a side object cuts per-row memory on create, replace, append and clear. A/B it with `jfb/ab.ts`.
- **Per-row JavaScript state on DOM elements.** herdsman-react sets two symbol expandos (`FIBER`, `OWN`) on every row element. For herdsman-blocks, an expando slowed native clearing by 5–7% (`jfb/native-clear.ts`). Measure herdsman-react with and without them before redesigning: the dispatch walk uses `FIBER`, and placement uses `OWN`.
- **Fewer distinct functions on the update path**, so that less code tiers up during the measured click: for example, fold `flushSoon` and `runMicro` together, and `rerender` into `flushWork`. Expected gain is small; prove it with an A/B.

Rejected, with measurements: element creation through `jsx()`, a fragment in `place()`, `insertBefore` instead of `moveBefore`, the inert template document off, skipping the per-event mark. See the plan for each.

**Done when** item 1's pools show reactre-compat ahead of at least one official entry, with no compatibility regression (36/36 with classes).

### 3. reactre runtime: correctness gaps before real use

These don't affect the benchmark app, but would bite a real app:

- **No ownership or disposal for effects outside lists.** An effect created inside a component, or inside a `__slot`'s content, is never disposed. When a slot replaces its content (`{cond() ? <A/> : <B/>}`), the old content's effects stay subscribed: a leak, and writes to detached nodes. Needed: an owner tree (each effect or slot run owns the effects created in it and disposes them before re-running), plus `onCleanup`. Add a test: toggle a slot, then check the old content's signals have no subscribers.
- **Rows can't hold components, nested `<For>` or JSX expressions.** It's a compile error today ("a <For> row cannot hold ... yet"). Needed for real lists. A row-level slot would need the same ownership as above.
- **Selector.** It records one key per row; a row that reads two selectors, or one selector with two keys, keeps only the last. Observers outside rows (`byKey`) are only removed when their key changes, so one that stops reading a key gets one spurious re-run.
- **Batching.** `pending.includes` makes a long batch quadratic in effects. There is no cycle guard: an effect that writes what it reads loops.
- **Compiler coverage.** Missing:
  - fragments and spread props (both compile errors now);
  - `style` objects;
  - `ref`;
  - SVG (no namespace handling: templates are HTML);
  - boolean DOM properties such as `checked` and `value`, as properties rather than attributes;
  - events that don't bubble (`focus`, `blur`: delegation won't see them inside rows);
  - `key` on a host element outside `<For>` (a compile error, by choice).

  `reactive()` treats any call as reactive and any non-call as static. A hole that reads a plain mutable variable is written once, which is by design but should be documented.
- **Event handlers outside lists** are bound once, as `addEventListener(type, expr)`. A handler expression that reads a signal won't update.

**Done when** each has a test in `reactre.test.ts`, and the benchmark numbers don't move (A/B against the commit before).

### 4. reactre performance left on the table

- **Update** is 5.3 ms against herdsman-blocks' 4.3. `ForList.update` computes the old item's hole values (`fill(old)`) and re-runs the row's reactive holes for every updated row. Options:
  - keep last values only for item holes, which costs memory per row (measure it);
  - skip the reactive re-run when the reactive expressions don't read the item. That needs the compiler to know which parameter each expression reads.
- **Create** allocates a row object of seven fields, plus `dv` when there are reactive holes. herdsman-blocks' app used three. Measure whether dropping `obs` and `sel` from rows that never need them (via a side table) moves create or clear.

### 5. Report

- `bench/herdsman-cases/jfb/REPORT.md` currently names neither reactre nor reactre-compat, by request. **Only once item 1 is done and reactre holds its place** should it be rewritten around reactre: its result, how it differs from the hand-written blocks app, and its reproduction steps.
- Keep the report's standards: pools, not single runs; the caveats (replica, VM noise, Chrome version); and which entry is React-compatible.

### 6. Packaging and naming, to decide

- `reactre` resolves through a build alias (`prepare.ts` and its `reactreAlias`), not a package. Decide on package layout and exports, e.g. `reactre`, `reactre/compiler` and `reactre-compat`.
- `reactre-compat` is a re-export of herdsman-react. Either keep it as a documented alias, or move the code. Either way, say so where it's documented.

## How to work on it

- Setup and every command: `bench/herdsman-cases/jfb/REPORT.md`, "Reproducing it independently".
- **Tests:** `cd web && bun test`. Also run `bun fmt --check` and both type checks:
  - `bun check --no-pretty --typeRoots "$HOME/.bun/install/global/node_modules"`
  - the same with `-p tsconfig.strict.json`.
- **Rebuild after a change:** `bun jfb/prepare.ts --app-only` (from `bench/herdsman-cases`).
- **Prove a runtime change:** `jfb/ab.ts` builds only the herdsman-react app (`jfb/app/main.jsx`). For reactre changes, compare builds with `run.ts --entries` on two prepared copies, or extend `ab.ts` to take an app and compiler.
- **Measure only on an idle machine.** On this VM one click swings up to 2×; judge by pools and by script time, not by one run's durations.
