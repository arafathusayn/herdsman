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

**Which entry.** herdsman-blocks is the app on herdsman-blocks, the declarative template and keyed-list layer, with no React API. The React-compatible entry (herdsman-react, now also shipped as `reactre-compat`) did not reach the top 3; see [the React-compatible entry](#the-react-compatible-entry).

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
- **Discipline.** Measurements were kept apart from other heavy work. Where they were not, the run was dropped or restarted: one full run with tests and type checks running during it was left out of every pool, and one head-to-head was restarted. The first reactre run had a two-minute compatibility check during it, and is read with that in mind.

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

herdsman-react, now also shipped as **`reactre-compat`**, React's API for reactre apps, still passes **36 of 36** React compatibility scenarios against React 19 with classes (29 of 36 without, by design). It gained from this work:

- the compiler's list loop now reports which rows changed, so a swap visits two rows instead of a thousand (swap's script 4.5 → 3.3 ms, proven three times);
- a long append goes in as one fragment.

It remains about 2% behind third place for the reasons above.

## What is still running

Two interleaved runs of **reactre** against the top 5 are in progress. reactre is the declarative TSX layer (signals, a keyed selector, `<For>`) that compiles to the same herdsman-blocks templates and list. Its script times already match or beat the hand-written blocks app on create, append, swap and create-many, and trail on update (5.3 against 4.3 ms). A field with all seven entries (top 5, reactre and reactre-compat) comes next. This report will be updated with those standings.

## Where to look

- `jfb/app/blocks.ts`: the first-place app.
- `web/src/herdsman-blocks.ts`: `List.set`, `skipUnchanged`, the `remove` hook, fragment insertion.
- `web/src/reactre.ts`, `web/src/reactre-compiler.ts`: reactre.
- `web/src/reactre-compat.ts`: the React addon.
- `plans/herdsman-blocks.md`: the rounds in detail ("Second round" to "Fourth round").
- `bench/herdsman-cases/README.md`: how to run `prepare`, `run` (`--shift`, `--batch`), `pool`, `ab`, `profile` and `window`.
