# How herdsman got to first place in the js-framework-benchmark replica

A report of the work in this branch, 2026-10-28: what was measured, what changed, and what each change was worth.

## Result

The **`herdsman-blocks`** entry ranks **1st** among the official keyed top 5 of the Chrome 156 run (re, thane, vanillajs-lite, vanillajs, doohtml-dp). The run uses the official runner's layout, pooled over four runs with 60 samples per test and entry (100 for select row).

| Rank | Entry | Weighted score |
|---|---|---|
| **1** | **herdsman-blocks** | **1.042** |
| 2 | vanillajs-lite | 1.058 |
| 3 | doohtml-dp | 1.063 |
| 4 | vanillajs | 1.064 |
| 5 | thane | 1.066 |
| 6 | re | 1.069 |

The score is the official weighted geometric mean of each test's factor to the best median, so 1.000 means fastest in every test. herdsman-blocks' factors in this pool:

| create | replace | update | select | swap | remove | create 10k | append | clear |
|---|---|---|---|---|---|---|---|---|
| 1.04 | 1.01 | 1.08 | **1.00** | **1.00** | 1.14 | 1.01 | **1.00** | 1.06 |

Per run, it placed 3rd, 2nd (tied), 1st and 2nd.

**What "1st" means here.** This is a replica of the benchmark on a 4-core cloud VM, not the official results page. It runs the official tests, warmups, CPU throttling, forced GC, trace-based durations and weights. Two qualifications:

- **The other layout.** A second view of the same field interleaves the entries in fresh-browser batches (below). Over five such runs, herdsman-blocks is **2nd** (1.022), behind thane (1.011) and ahead of doohtml-dp (1.028), re (1.031), vanillajs (1.040) and vanillajs-lite (1.041). It placed 1st in two of those five runs.
- **The noise.** On this VM one click's time swings by up to 2×. Places that differ by less than about 0.01 in a pool are within noise. herdsman-blocks' lead in the official-layout pool is 0.016.

**Which entry.** herdsman-blocks is the app on herdsman-blocks, the declarative template and keyed-list layer, with no React API. The React-compatible entry (herdsman-react) did not reach the top 3; see [the React-compatible entry](#the-react-compatible-entry).

## Where we started

The first entry was **herdsman-react**: the idiomatic React app (`jfb/app/main.jsx`, a reducer, memoized rows, handlers that dispatch) on herdsman-react, a React 19-compatible runtime, with its JSX compiled to templates. After two rounds of profiling it settled at **4th to 6th**:

| Pool | Score | Place | 3rd place |
|---|---|---|---|
| 5 official-layout runs | 1.089 | 6th | 1.059 |
| 3 interleaved runs | 1.072 | 6th | 1.055 |

Its consistent deficits were select (1.24 to 1.38), clear, replace and remove. What it lost was script time in the measured click, about 2 to 4 ms at 4× throttling:

- **Work React's semantics require.** Selecting a row re-renders the list's parent and compares all 1,000 memoized rows, and events go through synthetic events and the update queue. The hand-written entries change two class names.
- **V8 tiering during the measured click.** The measured click is about the eighth call of the code that runs once per update (`Main`, the scheduler, the hooks). That is exactly when V8 allocates its feedback and compiles it with Sparkplug. With V8's threshold lowered from 8 calls to 4, select's script fell from 3.9 to 2.5 ms. There is no clean way for a framework to move that cost.

A React app written for speed, with the selection in a store each row reads through `useSyncExternalStore`, made select cheaper but create and replace dearer: a hook, a subscription and an effect per row put replace at 152 ms against 125. It was dropped as a contender, and remains in the replica as `react-fast` and `herdsman-react-fast`.

## The step that mattered: leaving React's model for the hot path

Once the second entry no longer had to be React, it was rebuilt on **herdsman-blocks** (`jfb/app/blocks.ts`):

- a row template with typed holes;
- a keyed `List` that renders the rows array, reusing, moving and removing rows by key;
- every action makes a new rows array and renders the list;
- a new selection sets two class holes;
- row clicks are delegated to the table body.

No test is special-cased; every structural change goes through the same list render.

The first version came **6th** (1.129). Its script was already below herdsman-react's, but it had its own waste. These changes moved it to first:

| Change | Measured effect (script time at the test's throttling, unless noted) |
|---|---|
| `List.set` diffs the common prefix and suffix **before** allocating. Keys are computed only where an item differs from last render's at that place; removals are spliced out of the kept arrays; a render that changes no keys allocates nothing. | update 6.0 → 5.2 ms, remove 2.3 → 1.9 ms |
| New `skipUnchanged` option: an entry given the same item object again is not updated. | Part of the line above |
| A row is a baked template clone and nothing more. Values come from one reused array, with no `Block` object per row; the label's text node is looked up only when the label first changes. | create 9.1 → 6.6 ms (vanillajs-lite: 7.1) |
| Remove a row by splicing a copy of the rows, not `filter` (1,000 callback calls in cold code). | remove 1.9 → 1.2 ms |
| Find a clicked row by scanning the list's entries, which are in the rows' order. The same scan gives its index in the rows. | remove 1.2 → 0.9 ms |
| **No expando on row elements.** See below. | Native clear of 1,000 rows: 40.8 → 32.3 ms, against vanillajs 31.7 |
| A long insertion into a live list goes in as **one fragment**. | Append: 179 → 128 ms in the next run |

### The expando finding

Clear was the one test where herdsman-blocks stayed slow after its script was fixed. Called directly in each app's page, after the benchmark's warmups and its last-resort GC, `tbody.textContent = ""` cost:

| Rows carry | Native clear of 1,000 rows (4×) | vanillajs |
|---|---|---|
| a `WeakMap` entry each | 40.8 ms | 32.1 ms |
| a string-keyed property | 34.7 ms | 31.2 ms |
| a symbol-keyed property | 33.4 ms | 31.3 ms |
| nothing | **32.3 ms** | 31.7 ms |

DOM node count, `class` attributes, the inert-document origin of the clones, event listeners and incremental marking were each measured and were neutral. Removing the per-row JavaScript attachment was what closed the gap.

## How the numbers were made trustworthy

Most of the session's effort went into measurement, because this VM's noise is larger than most of the effects:

- **Official layout.** Each test and entry gets a fresh browser running all of that entry's samples, as `webdriver-ts` does. An earlier replica interleaved every entry in one browser; entries shared a renderer and V8 heap and aged each other's compiled code.
- **Pooling.** One run swings an entry's score by about 0.05. Standings are pooled over all samples of several runs (`jfb/pool.ts`).
- **Order shifts (`--shift`).** Every run had put each entry in the same place in each test's order, so pooling could not even out drift that favors a place. Runs now rotate the order differently.
- **Interleaved batches (`--batch`).** One click swings from about 95 to 175 ms in spells, and an entry's 15 back-to-back samples catch whatever spell it ran in. With `--batch 3`, the entries take turns, three samples each in a fresh browser, so slow spells land on all of them.
- **A/B proof (`jfb/ab.ts`).** A change to the runtime is kept only if an A/B shows it proven faster on some test and slower on none. The A/B builds the app from a base revision and from the working tree and alternates fresh-browser batches, 80 to 96 samples per side. The verdict is a 95% bootstrap interval of the difference of medians, for the whole window and for script time, which is far less noisy.
- **Profiles of the measured click** (`jfb/profile.ts --cold`). These are CPU profiles of exactly the click the benchmark measures, after its warmups, throttling and forced GC, with flame graphs and line-level hot spots. `jfb/window.ts` splits the official window by kind (script, style, layout, paint) and compares entries.
- **Discipline.** Measurements were kept apart from other heavy work. Where they were not, the run was dropped or restarted: one full run with tests and type checks running during it was left out of every pool, and one head-to-head was restarted.

### Measured and rejected

Each of these was measured against the version before it and dropped:

- **Not proven:**
  - skipping the per-event dedup mark;
  - a fragment for replacing a whole list;
  - batched clones.
- **Proven slower:**
  - building component elements through `jsx()` instead of inline literals: update's script +2.0 ms;
  - `insertBefore` instead of `moveBefore` for moves: swap's script +0.9 to +1.2 ms;
  - turning off the inert template document: create-many's script +10.8 ms.
- **Below the noise:** a structure-of-arrays fiber store in one typed array, saving about 0.04 ms per 1,000 rows.

Not every row with no proven difference was dropped: herdsman-react's list reports (below) were kept because swap was proven faster and nothing was proven slower.

## The React-compatible entry

herdsman-react still passes **36 of 36** React compatibility scenarios against React 19 with classes (29 of 36 without, by design). It gained from this work:

- the compiler's list loop now reports which rows changed, so a swap visits two rows instead of a thousand (swap's script 4.5 → 3.3 ms, proven three times);
- a long append goes in as one fragment.

It remains about 2% behind third place for the reasons above.

## Reproducing it independently

Everything above can be rerun from this repository; nothing depends on this session's machine. Expect your absolute times to differ, and on a quieter machine your spread to be smaller. What should hold is the order of the scores, within the noise described below.

### 1. What you need

- **Bun 1.4.4** (the version used here; `bun --version`).
- **Chrome or Chromium.** Set `CHROME` to its executable, or it is looked for in the usual macOS and Linux places (`chrome.ts`). The runs here used Chrome for Testing 153.0.8010.12. The official results the top 5 come from were run on Chrome 156; a different version can shift every entry, and sometimes their order.
- **Network access once**, to clone js-framework-benchmark and install the top-5 entries' packages.
- **Several hours of an otherwise idle machine.** One full run of six entries took about 1¾ hours here; the official-layout pool is four of them and the interleaved pool five. Anything else running on the machine during a run (builds, tests, a browser) shows up in its numbers.

### 2. Get the code and install

```sh
git clone https://github.com/arafathusayn/herdsman.git
cd herdsman
git checkout ccr-ca4b6b2d-jtluel
cd bench/herdsman-cases
bun install --ignore-scripts                          # puppeteer-core
(cd apps/react && bun install --ignore-scripts)       # React 19: only for the react and react-fast entries
(cd react-compat && bun install --ignore-scripts)     # only for the React compatibility check
```

No package install script is run anywhere (`--ignore-scripts`), here or in `prepare.ts`.

### 3. Build the entries

```sh
bun jfb/prepare.ts
```

This does three things:

- makes a sparse clone of krausest/js-framework-benchmark at commit `ca8715d9ae84916fd24de3a79457d8c328f9b766` into `jfb/.cache` (not committed);
- builds the five entries as their own `build-prod` scripts do;
- builds our entries, among them `herdsman-blocks` and `herdsman-react`, and the React apps.

It prints `ready: ...` with every entry it built. After changing our code, `bun jfb/prepare.ts --app-only` rebuilds only ours.

### 4. Check that the code under test is the code described

```sh
(cd ../../web && bun test)            # 326 tests, 0 failures, at the time of writing
bun react-compat.ts --runtimes herdsman-react+classes,herdsman-react+classes+compiler,herdsman-react
```

The second command should print 36/36 for both class builds, and 29/36 for the build without classes, listing the seven class scenarios.

### 5. The official-layout pool (the 1st place)

Four runs with different order shifts, each saved to its own log, then pooled:

```sh
E=re,thane,vanillajs-lite,vanillajs,doohtml-dp,herdsman-blocks
for s in 0 3 1 4; do bun jfb/run.ts --entries $E --shift $s > off-$s.log 2>&1; done
bun jfb/pool.ts off-0.log,off-3.log,off-1.log,off-4.log
```

`pool.ts` prints each entry's pooled weighted score with its factor per test, best first. The result to compare against is in [Result](#result). Single runs swing by about 0.05 here, so judge by the pool, not one run.

### 6. The interleaved pool

The same field, with the entries taking turns in batches of three samples, each in a fresh browser:

```sh
for s in 2 3 4 5 0; do bun jfb/run.ts --entries $E --batch 3 --shift $s > int-$s.log 2>&1; done
bun jfb/pool.ts int-2.log,int-3.log,int-4.log,int-5.log,int-0.log
```

This should come out with thane first and herdsman-blocks second (1.011 and 1.022 here), the other four within about 0.02 of them.

### 7. Single findings

| Finding | Command |
|---|---|
| The native-clear table ([The expando finding](#the-expando-finding)) | `bun jfb/native-clear.ts vanillajs,herdsman-blocks 40`. To see an expando's cost, give each row element a property in `jfb/app/blocks.ts`'s `create`, run `prepare.ts --app-only`, and run it again. |
| A change's effect, as an A/B (runtime and compiler changes under `web/src`) | `bun jfb/ab.ts --base <git ref> --benchmarks 05_swap1k,03_update10th1k_x16 --rounds 12 --batch 8 [--phases]`. It builds the herdsman-react app from `<ref>` and from the working tree. Example: `--base b740ea7` against `25631b3` shows swap's script proven faster by the list reports. |
| Script time by test, entry against entry | `bun jfb/window.ts herdsman-blocks,vanillajs-lite 01_run1k 12 --kinds` |
| Where a click spends its time | `bun jfb/profile.ts select 30 --cold --entry herdsman-react`. This writes a `.cpuprofile` for the DevTools Performance panel and a flame graph to `jfb/.cache/profiles`. |
| V8 tiering in the measured click | `JFB_JS_FLAGS=--invocation-count-for-feedback-allocation=4 bun jfb/window.ts herdsman-react 04_select1k 25 --kinds`. Compare it with the same command without the variable. |

### 8. What to expect from the noise

- **One click.** On this VM the same click varies up to 2× between samples, in spells that last minutes.
- **One run.** A run's weighted score moves by about 0.05.
- **A pool.** Pools of four or five runs moved entries by about 0.01 to 0.02 between pools.
- **Herdsman-blocks' lead.** It leads the official-layout pool by 0.016. In single runs it placed between 1st and 4th.

A reproduction that puts herdsman-blocks anywhere in the top two or three, with the scores this close, is consistent with this report. A reproduction that puts it last by a wide margin is not, and would point at a difference in machine or browser worth reporting.

## Where to look

- `jfb/app/blocks.ts`: the first-place app.
- `web/src/herdsman-blocks.ts`: `List.set`, `skipUnchanged`, the `remove` hook, fragment insertion.
- `plans/herdsman-blocks.md`: the rounds in detail ("Second round" to "Fourth round").
- `bench/herdsman-cases/README.md`: how to run `prepare`, `run` (`--shift`, `--batch`), `pool`, `ab`, `profile` and `window`.
