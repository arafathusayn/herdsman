# herdsman-blocks: article and design spec

A living document. It explains why herdsman-blocks exists and specifies its behavior precisely enough to rebuild it without reading its source. It holds no code; every procedure is written as numbered steps. Update it in the same change as any behavior change, and add a line to the change log at the end.

- Status: draft, on branch `herdsman-blocks`
- Library version: 0.1.0 (the module exports this as `VERSION`)
- Last updated: 2026-10-27
- Modules: `web/src/herdsman-blocks.ts`, the view layer `web/src/herdsman-blocks-view.ts`, which herdsman web renders with, and the React API `web/src/herdsman-blocks-react.ts`, a drop-in client runtime for React apps

---

## Part 1: Article

### The problem in herdsman web

The herdsman web app renders in immediate mode. Every time the store emits, a view rebuilds its whole subtree with the `h()` builder and swaps it in with `mount()`, which empties the container and appends the new nodes. That is simple and hard to get wrong, but it costs three things on every update:

1. The browser discards and rebuilds every node, then restyles and relays out the whole subtree, even when one status pill changed.
2. State that lives in the DOM is lost: keyboard focus, the text caret, a text selection, scroll positions inside the view, and whether a `<details>` is open.
3. Event handlers and nodes held elsewhere point at nodes that no longer exist.

### The idea

Keep the immediate-mode style, because views stay plain functions of the store. Change only the last step: instead of replacing the container's children, compare the freshly built tree with the live one and patch the live one until it matches. Nodes that still correspond are kept; only real differences touch the DOM.

The library has three layers, each usable alone, a declarative view layer on top of blocks and lists, and a React-compatible runtime:

1. **Morph** patches a live subtree to match a freshly built one. herdsman web used it through a `patch()` drop-in for `mount()` until the views migration; it has no app user now.
2. **Keyed lists** reconcile an old ordered list of keyed children with a new one in the minimum number of DOM moves. Morph uses this for every child list.
3. **Blocks** are a faster path for hot, repeated markup: a template is parsed once, each instance is one deep clone, and updates write straight to the few nodes that can change. They suit large tables and logs where even building the fresh tree is too much work.
4. **Views** keep the immediate-mode style on top of blocks: a view function returns light descriptions (a template and its values) on every render, and `render` writes only the holes whose values changed. Optional content and nested views go in child holes, keyed lists in `list()`, and event handlers through per-template trampolines so a fresh closure per render costs no DOM write.
5. **The React API** runs unchanged React apps: the client half of React 19 (`react`, `react-dom`, `react-dom/client`, the JSX and React Compiler runtimes) in one module that an app aliases those imports to. Its placement step reuses the keyed lists' minimum-move algorithm, so a reorder moves the fewest nodes and keeps their focus and state.

### Why this algorithm

Reordering keyed children is the hard part of any DOM diff. The minimum number of moves is known exactly: it is the number of surviving children minus the length of the longest run of them that is already in increasing old order (the longest increasing subsequence, LIS). Everything on that run stays put, and everything else moves once. Computing the LIS takes O(n log n) time, and no comparison-based method can compute even its length faster in general.

So the list algorithm aims to never exceed that move count, and to avoid computing the LIS at all in the common cases: appends, prepends, removals, in-place updates, and a swap of two rows. Those are handled by trimming the unchanged prefix and suffix and by a dedicated two-item swap check.

### What the benchmarks say (as of 2026-10-26)

The reference is the js-framework-benchmark Chrome 156 run, published 2026-10-25 (krausest/js-framework-benchmark, release "chrome156", 201 frameworks, keyed only). Recomputed from that run's raw results with the official weights, the top of the keyed table is `re` at 1.014, then `thane` at 1.019, `vanillajs-lite` at 1.021 and `vanillajs` at 1.024, where 1.000 would be the best result on every test. The top ten sit within about 3% of each other.

Our measurements come from a local replica of the official method in headless Chrome 156 (2 CPU cores, about 3.5 times slower than the official machine, with run-to-run spread of up to about 30%). Frameworks are interleaved within each iteration. Scores are relative to the best of the frameworks in that run on each test, not to the official table.

**Latest run** (2026-10-26, after the allocation work in the change log), median of 12 iterations:

| Test               | re (official #1) | vanillajs-lite (official #3) | vanillajs | herdsman-blocks |
| ------------------ | ---------------: | ---------------------------: | --------: | ----------------: |
| create 1,000 rows  |          74.3 ms |                      68.2 ms |   87.1 ms |           74.2 ms |
| replace 1,000 rows |          75.8 ms |                      76.3 ms |   76.1 ms |           77.6 ms |
| partial update     |          61.3 ms |                      53.7 ms |   54.1 ms |           55.2 ms |
| select row         |          11.1 ms |                      11.2 ms |   12.3 ms |           11.8 ms |
| swap rows          |          66.6 ms |                      62.5 ms |   69.1 ms |           57.9 ms |
| remove row         |          46.7 ms |                      44.2 ms |   44.3 ms |           44.9 ms |
| create 10,000 rows |         771.0 ms |                     734.2 ms |  819.2 ms |          734.7 ms |
| append 1,000 rows  |          87.3 ms |                      81.0 ms |   87.6 ms |           87.6 ms |
| clear 1,000 rows   |          30.2 ms |                      29.3 ms |   29.7 ms |           34.2 ms |
| **weighted score** |        **1.065** |                    **1.004** | **1.080** |         **1.052** |

Assessment: in this replica herdsman-blocks ranks second of four by score, but the noise control below shows that differences under about 4% are not meaningful on this machine, so it is effectively tied with `re` (the official leader) and `vanillajs`, and about 4.8% behind `vanillajs-lite`, a gap at the edge of the noise. It is fastest on swap and ties the fastest on create 10,000 rows. Its one clear loss is clear rows (1.17 times the best): its samples split between about 30 ms and about 50 ms, which points to garbage collection of the per-row block objects landing inside the measured window. Whether these placements hold on the official table can only be settled by the official run.

**0.0.3 run** (2026-10-26, lazy rows and state-preserving moves), median of 12 interleaved iterations, weighted scores: herdsman-blocks 0.0.3 1.036, `vanillajs-lite` 1.045, herdsman-blocks 0.0.2 1.066, `re` 1.070. 0.0.3 ranked first, but its margins over `vanillajs-lite` (0.9%) and over 0.0.2 (3%) are under the 4.3% noise floor below, so this shows no regression and at most a small gain. Clear rows, which looked bound by garbage collection before, took 30.0 ms with one slow sample in twelve (0.0.2: four). A focused rerun of the four tests where 0.0.3 had the slower median (select, swap, create 10,000, append; 15 interleaved iterations) gave 0.0.2 1.020 and 0.0.3 1.011: create 10,000 and append flipped in 0.0.3's favor (707.1 ms against 735.8 ms; 78.0 ms against 78.8 ms), select (12.3 ms against 11.5 ms) and swap (59.9 ms against 58.5 ms) stayed slightly slower, all within the noise floor. No regression.

**Every version against the references** (2026-10-26), median of 10 interleaved iterations, all nine tests, each version built from its own release or commit: herdsman-blocks 0.0.4 1.034, 0.0.1 1.050, `vanillajs-lite` 1.054, herdsman-blocks 0.0.3 1.059, 0.0.2 1.067, 0.0.5 1.073, `re` 1.094, `vanillajs` 1.109. Versions 0.0.4 and 0.0.5 produce byte-for-byte equal benchmark code apart from the version string (0.0.5 changed only morph, which the benchmark does not use), so their 3.9% gap is this run's own noise control. Read with it: the herdsman-blocks versions are indistinguishable from each other and from `vanillajs-lite`, and all scored at or ahead of `re` and `vanillajs` in this run (best version 5.8% ahead of `re`, worst 2%). Earlier runs placed `re` and `vanillajs` higher, so only the official run can settle the order.

**Noise control** (2026-10-26): the same 0.0.1 build run against a byte-identical copy of itself, 10 interleaved iterations, scored 1.052 and 1.009, a 4.3% gap from noise alone; per-test factors between the identical builds reached 1.17. Any replica difference smaller than that is not evidence.

**Earlier run** (same day, before the allocation work), median of 15 iterations, three frameworks: vanillajs 1.029, herdsman-blocks 1.076, `re` 1.086.

### React apps on herdsman-blocks (2026-10-27)

TanStack Redact (0.2.1, announced the same week) replaces React and React DOM in the browser with a smaller runtime that renders synchronously. Layer 5 takes the same approach, aiming for React's exact behavior and a smaller, faster runtime. It was measured against React 19 and Redact 0.2.1 on two points, each run in headless Chrome (Chromium 153, 4 cores):

1. **Same behavior as React.** `bench/herdsman-cases/react-compat.ts` runs 31 scenarios on React 19 and on each runtime, and compares every logged value and the HTML after every step. 25 exercise the API, from host props to hydration of React's own server HTML; 6 use the five React libraries npm downloads most each week (2026-10-27): @radix-ui/react-dialog (61.1 million), zustand (56.4 million), @tanstack/react-query (54.5 million), react-router (49.1 million, its data router and its declarative routes) and framer-motion (45.3 million). herdsman-blocks-react with class support matches React in 31 of 31. Without it (the default), it matches in the 26 that render no class component and stops with its no-classes error in the other 5, which include react-router's data router (its route error boundary is a class) and framer-motion's `AnimatePresence` and layout features. Redact matches in 20. It differs in `<meta>` hoisting, `xlinkHref`, the order of parent and child effects, passive effects around a layout-effect update, `shouldComponentUpdate` calls, events bubbling through portals, enter and leave events, input attribute order, form action URLs, `Children.count` and the Radix dialog.
2. **Size and speed on herdsman's own screens.** The herdsman-cases apps `react`, `react-blocks` and `redact` bundle the same source, `apps/react/main.tsx`, compiled by the React Compiler, with only the runtime swapped. All three pass the correctness gate (the reference markup after every operation) and keep the same nodes. Median of 10 interleaved iterations, factor to the best median (Preact added as a non-React reference):

| benchmark | React 19 | herdsman-blocks-react | Redact 0.2.1 | Preact |
| --- | ---: | ---: | ---: | ---: |
| create | 102.2 ms (1.98) | 52.0 ms (1.01) | 64.4 ms (1.25) | 51.7 ms (1.00) |
| poll (4x) | 15.9 (1.00) | 19.4 (1.22) | 25.4 (1.60) | 19.6 (1.23) |
| status (4x) | 41.1 (1.00) | 46.1 (1.12) | 54.7 (1.33) | 42.9 (1.05) |
| select (4x) | 168.8 (6.39) | 26.4 (1.00) | 41.7 (1.58) | 29.1 (1.10) |
| tab-activity | 85.4 (1.68) | 50.7 (1.00) | 71.0 (1.40) | 55.0 (1.08) |
| event (4x) | 105.4 (1.32) | 84.1 (1.05) | 132.2 (1.66) | 79.8 (1.00) |
| burst (4x) | 135.6 (1.26) | 107.6 (1.00) | 134.0 (1.25) | 118.7 (1.10) |
| tab-checklist | 26.5 (1.16) | 27.0 (1.18) | 29.4 (1.28) | 22.9 (1.00) |
| toggle (4x) | 37.5 (1.40) | 26.8 (1.00) | 41.6 (1.55) | 27.4 (1.02) |
| tab-log | 46.0 (1.00) | 47.5 (1.03) | 63.2 (1.38) | 50.3 (1.09) |
| logline (4x) | 64.3 (1.09) | 59.1 (1.00) | 76.0 (1.29) | 62.8 (1.06) |
| tab-team | 42.5 (1.73) | 25.3 (1.03) | 27.3 (1.11) | 24.5 (1.00) |
| **geometric mean** | **1.477** | **1.051** | **1.380** | **1.060** |
| app JavaScript, gzip | 69.7 KB | 15.3 KB | 30.2 KB | 7.5 KB |

The timings were taken with class support built in; the app has no class components, so it now builds without them (16.0 KB before). A one-component app (a counter in a root) is 65.8 KB gzipped with React, 26.4 KB with Redact (every feature on), 11.6 KB with herdsman-blocks-react and 12.8 KB with its class support. Reading: on this machine the noise floor is about 4% (see the noise control below), so herdsman-blocks-react is tied with Preact, about 24% ahead of Redact and 29% ahead of React by geometric mean, and it is the only React-API runtime that is never more than 1.22 times the best. Its losses are the unchanged re-render (poll) and the one-card status move, where React is fastest.

---

## Part 2: Design spec

### Terms

- **Live** nodes are in the document now. **Fresh** nodes were just built by a view and are not in the document.
- A **key** identifies a child among its siblings. Two children of the same parent never share a key.
- An **entry** is anything a list keeps per item that exposes the item's root DOM node.
- The **window** is the part of an old and a new list left after trimming the matching prefix and suffix.

### Layer 1: Blocks

#### Compiling a template

A template is written as a tagged template literal whose interpolations mark holes; the Nth interpolation is hole number N. An interpolation is either a typed hole marker (see Types below), whose runtime value is a small negative number naming the hole's kind, or a plain non-negative number, an untyped hole. Interpolated values never reach the DOM. Compilation happens once per call site, cached by the identity of the literal's string array. There are two tags: `html` for HTML content and `svg` for SVG content; they differ only in step 4.

1. Walk the literal's string parts in order, tracking whether the scan is inside a tag (an unclosed `<`).
2. For a placeholder inside a tag, the text before it must end with an attribute name, an equals sign and optionally an opening quote. Replace that ending with a marker attribute named `bk` plus the hole number, whose value is the original attribute name (percent-encoded). If a quote was opened, drop the matching closing quote from the next string part. A placeholder inside a tag that is not an attribute value is an error.
3. For a placeholder outside a tag, emit a comment whose text is `bk` plus the hole number.
4. Parse the result once into an inert template element. With the `svg` tag, wrap the markup in an `<svg>` element first and take the wrapper's content, so that a root such as `<g>`, `<path>` or `<circle>` is created in the SVG namespace (parsed bare, the HTML parser would make it an unknown HTML element). It must contain exactly one root element and nothing else, or compilation fails.
5. Walk the parsed tree depth first, recording for each hole its path, the list of child indexes from the root. Before indexing an element's children, remove every text child that is only whitespace and contains a line break, unless the element is a `pre` or a `textarea`. Such text is indentation from a formatter (herdsman's `bun fmt` formats tagged `html` literals as HTML), not content; whitespace within a line, such as the spaces around a hole in "a, hole, b", is kept. Then:
   - A marker attribute is removed. The hole's kind comes from the original name: `class` is a class hole on an HTML element, but an attribute hole on an SVG element, whose class name property is read-only (assigning it throws in strict code); a name starting with `on` or with a dot is a property hole (the dot is stripped, an `on` name is lowercased); anything else is an attribute hole.
   - A marker comment is replaced by an empty text node at the same position, and the hole is a text hole pointing at that text node.
6. Every hole must have been found, or compilation fails. For each hole with a typed marker, compare the marker's kind with the hole's position:
   - The position's kind is the kind from step 5, except that a property hole whose name starts with `on` (with or without the dot) is an **event** position.
   - A slot marker at a text position makes the hole a slot.
   - Otherwise the marker's kind must equal the position's kind, or compilation fails with an error naming the hole, the declared kind and the kind its position implies. The error adds a hint for the two cases a type cannot catch: an event position takes only the event marker, and `class` on an SVG element is an attribute.
   - Untyped holes (plain numbers) are not checked.

   This check runs once per template, never per instance. The event kind exists only here: an event hole is a property hole at runtime, so mounting and updating never see it.
7. Generate a locator: one straight-line function that walks from a root through `firstChild` and `nextSibling` steps to each hole's node, sharing common path prefixes so each node is reached once, and writes the nodes into the first half of a given array. If generating functions at runtime is not allowed (a strict content security policy), fall back to walking each path in a loop.
8. Run the locator once on the parsed root itself, the **prototype**, and keep its hole nodes. Record what the prototype holds in each hole: the empty string for text and class holes, absent (null) for attribute holes.

#### Block state

A block is the template, its root element, and a single state array of length twice the hole count: the first half holds the hole nodes (empty until located), the second half the last value set in each hole. One array per instance, instead of separate node and value arrays, matters when thousands of rows are created at once.

#### Mounting

1. For each hole that is not a property hole, compare the new value with what the prototype holds. If it differs, write it into the prototype's hole node (by the update rules below) and record it.
2. Deep-clone the prototype. The clone is born with the row's text, classes and attributes.
3. Create the block with the clone as root, a fresh state array, and the new values recorded in its second half. Do not locate the clone's hole nodes.
4. Properties do not survive cloning. If any property hole has a non-null value, locate the clone's holes now and set those properties on the clone. Property values are never written to the prototype.

#### Mounting without a block

For rows that mostly never change, a second mount returns only the new element and keeps no JavaScript object for the instance:

1. Bake and clone exactly as in steps 1 and 2 above.
2. If any property hole has a non-null value, locate the clone's holes into a temporary array and set those properties.
3. Return the clone.

When such an element first needs an update, ask the template for its block:

1. If the element already carries a block (cached on the element under a private symbol), return it.
2. Otherwise create one with the element as root, hole nodes not yet located, and every hole's last value set to a private "unknown" marker, so the first set of each hole always writes. Cache it on the element.

Such a block does not know the values the element was created with, so its type offers setting holes but not reading them.

Why this shape: in a browser, every DOM node that JavaScript touches gets a JavaScript wrapper object that must later be garbage-collected. Filling a row by walking its cloned nodes touches about six nodes per row; filling the prototype and cloning touches none of the clone's inner nodes. A row that is never updated is never walked. Writes into the prototype only happen where consecutive rows differ, and the prototype is not in the document, so they cost no style or layout work.

#### Updating a hole

1. If the new value is identical (strict equality) to the hole's last value, do nothing.
2. Record the new value.
3. If the block's hole nodes have not been located yet, run the locator on the block's root now (once per block).
4. Write the value by kind:
   - Text: set the text node's data to the value as a string, or the empty string for null or undefined.
   - Class: set the element's class name, or empty for null or undefined.
   - Property: assign the value to the named property.
   - Attribute: remove the attribute for null, undefined or false; set it to the empty string for true; otherwise set it to the value as a string.

Values are never parsed as HTML. A string with angle brackets becomes literal text.

### Layer 2: Keyed lists

A list's entries are either objects exposing the item's root node, or anything else together with a function that returns an entry's root node (for example the root element itself, so the list keeps no object per item). The type requires that function exactly when entries have no root property.

A list is bound to a parent node and, optionally, an anchor (a node before which the list lives; none means the end of the parent). It is configured with a key function, a create function (item to entry), an update function (entry and item), an `owns` flag (the list is the parent's only content) and a `detach` flag. It remembers the keys and entries of the last render.

`owns` only applies without an anchor. `detach` only applies with `owns`.

#### Clearing

If the list owns its parent, set the parent's text content to empty (one native operation). Otherwise remove each entry's root. Forget all keys and entries.

#### Filling (building into a parent that holds none of the list's entries)

1. If `detach` is on and the parent is in the document, remember the parent's parent and next sibling, and remove the parent from the document.
2. Create an entry for each item in order and insert its root before the anchor.
3. If the parent was removed, put it back at the remembered position.

Detaching lets the browser do its style and layout bookkeeping once instead of per inserted row. It resets the parent's own scroll position and drops focus inside it, which is why it is opt-in.

#### Reconciling to a new item list

Let `m` be the old length and `n` the new length.

1. If `n` is zero, clear (if there was anything) and stop.
2. Compute the new keys.
3. If `m` is zero, fill and stop.
4. Trim the prefix: walking from the start while both lists have items and the keys match, keep the old entry for that position and update it with the new item.
5. Trim the suffix the same way from the ends.
6. Now exactly one of these holds for the window that is left:
   - **Only insertions** (the old window is empty): create entries for the new window and insert each before the root of the entry just after the window, or before the anchor.
   - **Only removals** (the new window is empty): remove the roots of the old window.
   - **Swap of the two ends** (both windows have the same length of at least two, the old first key is the new last key, the old last key is the new first key, and every key in between is unchanged): move the old last root before the old first root. If the two were not adjacent, also move the old first root to where the old last root used to be, which is before the old last root's original next sibling. Update all window entries with their new items. This costs one move for adjacent items and two otherwise, which is minimal, and it needs no map and no LIS.
   - **General case**: described next.
7. Remember the new keys and entries.

#### The general case

1. Take this nesting depth's scratch buffers (see below) and set the first slots of its source array, one per new window position, to "new item".
2. Index the new window's keys, choosing one of three lookups:
   - **Scan** for a window of 16 keys or fewer: a linear search is cheaper than any index.
   - **Direct-address table** when every key in the window is a non-negative integer and the keys' range is at most 8 times the window size plus 64 (and at most 4,194,304 wide): a shared integer array maps key minus the smallest key to the new position. A shared stamp array marks which slots the current reconcile wrote, using a generation counter that increases with every reconcile, so the table is never cleared. Both arrays grow on demand (doubling, up to 32 MB together at the cap) and are reused by every list. Row ids, which are usually dense integers, take this path.
   - **Hash map** otherwise (strings, sparse or negative or fractional numbers).
3. For each old window entry in order, look up its key among the new window. If found, store the old index in the source slot of its new position and adopt the old entry there. Track whether the found positions ever decrease (the "moved" flag), and count survivors.
4. If nothing survived, the list owns its parent, and the window spans both whole lists: set the parent's text content to empty and fill. Stop.
5. Remove the roots of old entries whose key is not in the new window.
6. Update every adopted entry with its new item.
7. If the moved flag is set, compute the LIS of the source array, ignoring "new item" slots. It is the set of survivors that will not move.
8. Walk the new window from right to left. The node to insert before is the root of the next new entry, or the anchor at the end:
   - A "new item" slot: create its entry and insert it there.
   - A survivor, when moved is set: if it is the next LIS member counting from the right, leave it and step the LIS cursor; otherwise move it there.
   - When moved is not set, survivors are already in order and nothing moves.

Walking right to left guarantees the reference node is already in its final place when it is used.

#### Moving and placing nodes

Every move of a surviving entry (the swap path and the general case) uses the state-preserving move when the browser has it and the parent is in the document, and plain insertion otherwise. The state-preserving move (`moveBefore`, Chrome 133+) keeps focus, iframe, video and CSS animation state that insertion resets; it throws when the parent is not in the document, which is the case while `detach` fills it. Inserting a new entry's node uses the state-preserving move only when that node is already in the document elsewhere (a persistent element a view reuses), and plain insertion otherwise.

#### Scratch buffers and nesting

The general case needs a source array and two LIS work arrays. Allocating them on every reconcile was 14% of the reconciler's time in profiling, so each is a reused integer array, grown on demand and never shrunk. The reconciler calls user code while it still needs them (the update callback, and the create callback during the right-to-left walk), and that code can run a nested reconcile, as morph does for every kept element. So buffers are kept per nesting depth: a depth counter rises on entering the general case and falls on leaving it (also on an exception), and each depth has its own set. The direct-address table needs no such care: every lookup into it finishes before any user code runs, and a nested reconcile's new generation simply invalidates the outer one's entries after the outer reconcile is done with them.

#### Longest increasing subsequence

Use patience sorting with predecessor links, O(n log n):

1. Keep an array of "tails": the tail at index k is the position whose value ends the best increasing run of length k + 1 found so far. Keep a predecessor for each position.
2. For each position in order, skip "new item" slots. If its value is larger than the value at the last tail, link its predecessor to that tail and append it. Otherwise binary-search the first tail whose value is not smaller, link the predecessor to the tail before it (or none), and replace that tail.
3. Rebuild the run by following predecessor links back from the last tail. Return positions in ascending order.

Values are strictly increasing; old indexes are distinct, so ties never occur.

### Layer 3: Morph

Morph makes a live parent's children match a list of fresh nodes. It is what the former `patch()` in `dom.ts` ran (the herdsman-cases reference app keeps a copy).

#### Keys for arbitrary nodes

Keys are computed for the differing window of both sides together, so they can depend on what both sides contain:

1. **Keyed elements:** an element with a `data-key` attribute gets its node name plus that value; a repeated value falls back to adding an occurrence number, so keys stay unique.
2. **Content keys:** every other node gets a content signature, its node name plus its text (`textContent` for elements, the data for text and comments). When a signature occurs on both sides, the first occurrences up to the smaller of the two counts are keyed by the signature plus an occurrence number. A moved row then keeps its node and costs one move.
3. **Leftovers:** remaining unkeyed nodes (new content, changed content, extra repeats) are keyed by node name plus their order among the leftovers of that name. A row whose text changed therefore pairs with the old row in the same leftover position and is patched in place, not replaced.

The node name is part of every key, so a key never matches a node of a different tag; a changed tag is a removal plus an insertion.

#### Morphing a child list

1. Collect the live children.
2. **Trim identical ends.** Walking from the start while both sides have children, keep any live child that is the same node as its fresh partner or deeply equal to it (the DOM's native `isEqualNode`). Do the same from the end. A kept pair gets no diff: only the keep step below.
3. If nothing is left on either side, stop.
4. **Key the differing window** on both sides (see Keys for arbitrary nodes).
5. Seed a keyed list over the window: its entries are the live window's children themselves, its keys the computed old keys. It owns the parent only when no ends were trimmed; otherwise it is anchored before the first trimmed suffix child.
6. Reconcile it to the fresh window. Its create step adopts the fresh node itself, moving it into the live tree with no copy. Its update step morphs the matched live node toward its fresh partner.

**The keep step** for an identical pair: a structural compare cannot see event handlers or form-field properties, so walk both subtrees in lockstep, call the keep hook (which hands over handlers) for every element pair, skip below `data-morph-skip` elements, and sync form state after each element's children.

#### Morphing one node

1. If the live node is the fresh node itself (a persistent element a view reuses), stop.
2. For text and comments, copy the fresh data if it differs, and stop.
3. For elements:
   1. Sync attributes: set each fresh attribute whose live value differs; remove each live attribute the fresh element lacks.
   2. Call the keep hook with the live and fresh elements (dom.ts uses it to move event handlers).
   3. Unless the live element has `data-morph-skip`, snapshot the fresh element's children and morph the live element's children to them.
   4. Sync form state, after children so a select already has its options. Inputs, text areas and selects copy `value` unless the live element has focus, which keeps a user's in-progress typing. Inputs also copy `checked`. Options copy `selected`.

`data-morph-skip` is for elements whose children another component manages, such as a terminal.

### Layer 4: Views

Module: `web/src/herdsman-blocks-view.ts`. Views are a declarative layer over blocks and keyed lists: a view function rebuilds light descriptions of its whole screen on every render, and rendering writes only the holes whose values changed. No hand-written per-hole update code is needed.

#### Views and descriptions

1. A view is made with one of the view module's own tagged template tags, `html` or `svg`. Each compiles the literal into a block template exactly as Layer 1 does with the tag of the same name (once per call site, cached by the string array) and accepts the same hole markers plus the child marker. Every hole of a view must have a typed marker; untyped holes are a compile error in views.
2. Once per template the view records, for each hole, how it is written: a **child** hole (a slot), a **delegated** handler hole (a property hole whose name starts with `on`, which only the event marker may type, for an event type in the delegated set below), a **direct** handler hole (the same, for any other event type), a **property** hole (any other property hole), or a plain **write** hole (text, class, attribute). If any hole is a child, direct handler or property hole, mounting must locate those holes (the view is **eager**); write and delegated handler holes are located lazily, each group on its first use, by a locator generated for just that group (Layer 1 offers a locator for any subset of holes). Hole paths are valid only in the pristine clone: once a child hole puts content before its anchor, a path through that parent's children points at the wrong node. A view with any child hole therefore locates all its holes at mount, before any child content goes in; only views without child holes, whose clone's structure never changes, defer.
3. The **delegated set** is the bubbling events: auxclick, beforeinput, change, click, compositionend, compositionstart, compositionupdate, contextmenu, dblclick, focusin, focusout, input, keydown, keyup, mousedown, mousemove, mouseout, mouseover, mouseup, pointerdown, pointermove, pointerout, pointerover, pointerup and submit. Events that do not bubble (focus, blur, mouseenter, mouseleave, load, error, scroll, and so on) and those whose listeners affect scrolling (touch and wheel events) are direct. Each delegated event type a template uses is added, once, to a module-wide list of used types.
4. Calling a view with its value tuple returns a description: the view's recorded shape and the values array itself. A description is inert; nothing is created until it is rendered. The values array is kept by the instance it is rendered into, not copied, so it must not be changed after the call. The view also exposes its compiled template.

#### Slot holes (Layer 1 addition)

A slot hole is typed with the slot marker and must sit where a text hole would (element content); compilation then records it as a slot instead of a text hole. Its node is the empty text node the comment marker became. Blocks never write a slot: mounting leaves it empty, baking skips it, and setting it only records the value. The template exposes, for layers above, a bake-and-clone that leaves property and slot holes unwritten, the kind of each hole, a write of one value into one located hole node by its kind, and a locator generated for any subset of the holes (it fills only those entries of the node array).

#### Mounting a description

1. Bake the values into the prototype and deep-clone it (Layer 1, steps 1 and 2).
2. The mounted **instance** is the shape, the root element, the description's values array (kept as the last values) and a node array, absent until some hole is located. If the view has any direct handler hole (or it is mounted direct, see `renderDirect`), store the instance on its root element under a private symbol, for the trampolines. Views whose handlers are all delegated store nothing on their elements: a row with only text, class, attribute and delegated handler holes costs its clone and one instance object.
3. If the view is eager, create the node array, locate the eager holes into it, then for each eager hole in order:
   - Direct handler: set the handler property to the hole's **trampoline** (below).
   - Property: if the value is not null or undefined, assign it.
   - Child: wrap the slot's text node in a **child slot** (below), keep that in the hole's node position, and set the child to the value.
4. Delegated handler holes are not touched at mount: an instance whose view has only write and delegated holes walks no node of its clone.

#### Event handler trampolines

1. Each direct handler hole of a view has one trampoline function, made once per template. It is set as the element's `on` property when the element has one, and otherwise added as a listener for the event type (compositionstart has no property); either way once per element. When called: when called as an event handler, it walks from its element up to the nearest node holding an instance (the template's own nodes lie between them, so that is the element's instance), reads the hole's current value from the instance's last values and, if that value is a function, calls it with the element as `this` and the event; otherwise it does nothing.
2. Every instance's element gets that same trampoline at mount and keeps it. Updating a handler hole only records the new value; it costs no DOM write. A view can therefore pass a new closure on every render.

#### Delegated events

1. Every `render` call first makes sure its container listens for every type in the used list: it adds one shared listener per type it does not yet listen for, and records how many it listens for, so the check is one comparison when no new type appeared.
2. When the listener runs on a container for an event, walk from the event's target up to the container, excluding it, recording the path (and include the container when it is a root made by `mount`).
3. Find the instances on the path by descent, not by marks on elements: start with the container's own instance (a `mount` root) and then the instance `render` put in it, whose root must be on the path; going down the path, a node that is the root of a child instance of the current instance (the view in one of its child holes whose parent is the node's parent, an entry of a keyed list there, or a view among its runs) becomes the current instance. Content that is not an instance (a DOM node in a child hole, such as another render root) ends the descent, so a render root inside another is left to its own listener and each handler runs once. A list entry is found by scanning that list's entries, once per event.
4. For each instance found, for each delegated hole of its view whose event type is the event's, locate the view's delegated holes if they were never located, and if the hole's element is on the path between the target and the instance's root, record the element, the instance and the hole, ordered by the element's position on the path (innermost first). Instances mounted direct are skipped.
5. Run the recorded handlers innermost first. Before moving on to a different element, stop if propagation was stopped. For each, read the hole's current value; if it is a function, make the event's `currentTarget` read as the element, call the function with the element as `this` and the event, then restore `currentTarget`.
6. Visible differences from a direct handler: the element's `on*` property is never set; an element outside a render root (detached, or moved out of it) gets no delegated events; delegated handlers run when the event reaches the render root, after native listeners on nodes between the element and the root and before listeners above the root, so stopping propagation in a handler still keeps the event from document and window listeners.

#### Patching an instance toward a description

1. If the description is of a different view than the instance, mount it fresh, replace the instance's root with the new root in the instance's parent, and move the new shape, root, values and nodes into the same instance object (and store that object on the new root), so lists and parents that hold the instance need no update. Stop.
2. If the description's values array is the instance's last values array (the same description rendered again), stop: nothing can have changed.
3. Otherwise keep the new values array as the last values, and for each hole whose new value is not strictly equal to the old one, by kind: a handler does nothing (handlers read the last values when they run); a child sets the child slot to the value; any other hole locates the write holes first if they were never located, then writes the value by Layer 1's update rules.

#### Child holes

A child hole accepts text (a string or number), nothing (null, undefined or false), a DOM node, a description, a keyed list description, or a runs description. Its content always sits just before the slot's text node, the **anchor**, which never moves. Text is written into the anchor itself.

1. Classify the new value: nothing, text, node, view description, list description or runs description.
2. If the class is the same as the current content's:
   - Text: set the anchor's data to the value as a string.
   - Node: if it is the same node, do nothing. Otherwise replace the old node with the new one when the old one is still in the slot's parent, or insert the new one before the anchor when the old one was moved elsewhere.
   - View: patch the held instance toward the description (which replaces it when the view differs).
   - List: reconcile the list (below).
   - Runs: reconcile the runs (below).
3. Otherwise clear the current content (text: empty the anchor; node: remove it if still in the parent; view: remove the instance's root; list: clear the list, then put the anchor back at the end of the parent if an owning list removed it; runs: remove every run's node), then set the new content: write text into the anchor, insert the node before the anchor, mount the description and insert its root before the anchor, create a list, or start empty runs and reconcile them.

#### Keyed list descriptions

`list(items, key, render)` describes a keyed list: the items, a key function on the item (never its index; keys unique among siblings) and a render function from item to description.

1. Creating the list in a slot: if the anchor has no siblings, the list **owns** the parent (Layer 2: clearing is one native text-content write, and the anchor is dropped while the list is shown); otherwise the list is anchored before the slot's anchor, so content around the hole is never touched.
2. The Layer 2 list's create mounts `render(item)`, its update patches the entry's instance toward `render(item)`, and its key calls the key function. Entries are view instances. These callbacks read the slot's current key and render functions, which are replaced on every reconcile, so closures passed by the latest render are the ones used.
3. Reconciling: store the description's key and render functions on the slot and set the list to the items (Layer 2). Kept items are patched; moved items are moved with the minimum number of DOM moves; an item whose render returns a different view is replaced where it stands.

#### Runs descriptions

`runs(items)` describes unkeyed inline content: a positional sequence of text (strings or numbers) and descriptions, with no wrapper element per run, such as text between `<mark>` elements or inline markdown. It is the view-layer counterpart of the former `dom.ts`'s `unkeyed`. The slot keeps, per run in order, either its text node or its view instance; all of them sit before the anchor.

1. For each position i of the new items:
   - A description: if run i is an instance, patch it toward the description (which replaces it when the view differs). Otherwise mount the description and put its root in place of run i's text node, or insert it before the anchor if there is no run i.
   - Text: if run i is a text node, set its data when it differs. Otherwise create a text node with the text and put it in place of run i's root, or insert it before the anchor if there is no run i.
2. Remove the nodes of runs past the new length, and forget them.
3. Types: `runs` is generic in the description type N of its views and returns a branded runs description of N; a child hole typed for runs of N takes only runs whose views are N.

#### SVG views

1. A view made with `svg` has its elements in the SVG namespace. Its descriptions go into child holes inside SVG content: an HTML view's `<svg>` element or another SVG view.
2. Mounting, patching, child holes and keyed lists work the same as for HTML views; no step depends on the namespace.
3. In SVG content `class` is an attribute hole, written with the attribute rules (null, undefined and false remove it).
4. A headless Chrome check (`bench/herdsman-cases/check-svg.ts`) runs this against the browser's own parser: an `svg` root is an SVG element while the same markup through `html` is not, SVG list items render with a non-zero box and keep their nodes across a reorder, the latest handler runs with the SVG element as `this`, assigning a class name on an SVG element throws, a class marker on one fails at compile time, and typed property and event holes work on a real input.

#### render(container, description)

1. Make the container listen for every used delegated event type (Delegated events, step 1).
2. If the container already holds a rendered instance (kept on the container under a private symbol), patch it toward the description and stop.
3. Otherwise mount the description, empty the container (one text-content write), append the instance's root, and keep the instance on the container.

#### mount(description) and update(root, description)

For a component that places its root element itself (no container and no wrapper element), such as a panel whose root another view or library positions.

1. `mount`: mount the description (any view, child holes included) with delegation, keep the instance on its root element under a private symbol, make the root listen for every used delegated event type (it is its own render root), and return the root.
2. When the root's listener runs, the walk of Delegated events, step 2, includes the root itself (it is an instance root whose handlers count) and the descent starts from its instance.
3. `update`: the root must carry an instance from `mount` (otherwise it throws), and the description must be of the same view (otherwise it throws: a different view needs a different root element, which the caller holds). Make the root listen for any newly used types, then patch the instance toward the description.
4. A mounted root placed inside another render root is a render root below it, so its handlers run once, from its own listener.

#### renderDirect(container, description)

For code that moves a view's nodes out of its container, such as a bridge that renders into a scratch container and moves the roots into a tree another library manages, where root-delegated handlers would be lost.

1. Render exactly as `render` does (steps 2 and 3), except that the container listens for nothing and, for the duration of the call (including nested mounts by child holes, lists and replacements), every instance is mounted **direct**: its delegated handler holes are located at mount and set to the hole's trampoline like direct handler holes, and the instance is marked direct.
2. The delegated listener of any render root skips direct instances, so a direct instance moved under a render root runs each handler once, from its own element.
3. A container is used with one of the two functions.

### Layer 5: The React API

`web/src/herdsman-blocks-react.ts` implements the client half of React 19: every export of `react`, `react/jsx-runtime`, `react/jsx-dev-runtime`, `react/compiler-runtime`, `react-dom` and `react-dom/client`, except `react-dom`'s internals object for libraries (`__DOM_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE`), which is left out on purpose. One module answers all six specifiers, so an app switches by aliasing them at build time (a Bun plugin's `onResolve`, Vite's `resolve.alias`) and changes no code; libraries that import React get the same module. The app keeps `@types/react` for type checking. It reuses Layer 2's longest increasing subsequence and its rule for moves; it does not use templates, because React describes markup at run time.

What it is for: React apps that want a smaller and faster client runtime, or a reorder that keeps focus and state. What it is not: a server renderer (use React's own on the server, and `hydrateRoot` on the client), development warnings, or concurrent rendering.

#### Class components are a build option

Class components are left out by default: rendering one throws an error that names the component and the option. `web/src/herdsman-blocks-react-classes.ts` is the build with them. It re-exports every export of the default module and, when it loads, installs the class runtime (lifecycles, `setState` and `forceUpdate`, error boundaries) through one hook the default module exports. To include classes, alias the six specifiers to that module instead; the bench's alias plugin takes this as its `classes` option. A build constant cannot do this: Bun keeps code that a constant disables, so the choice is which module the alias points to.

1. `Component` and `PureComponent` are always exported, so a library that extends them still loads; only rendering a class needs the option.
2. Apps that render no class component, and use no library that does, should leave it out: it adds 1.2 KB gzipped. React's error boundaries can only be written as classes, and some libraries render classes inside (react-router's data router, framer-motion's `AnimatePresence` and layout animations), so those apps need it.

#### Elements

1. `jsx(type, config, key)` makes an element from the automatic runtime's arguments. When `config` has no `key` property, `config` itself becomes the props object (no copy). Otherwise the props are a copy without `key`, and `key` comes from `config`. The key is a string or null.
2. `createElement(type, config, ...children)` copies `config` into new props, minus `key`, `__self` and `__source`. One child becomes `props.children` as is, more than one an array.
3. A class with `defaultProps` gets them for every prop that is undefined, at element creation.
4. An element is `{ $$typeof, type, key, ref, props }` with React 19's `Symbol.for("react.transitional.element")`; `ref` mirrors `props.ref` (React 19 passes `ref` as a prop). Fragment, StrictMode, Profiler, Suspense, SuspenseList, Activity, ViewTransition, context, consumer, memo, forwardRef, lazy and portal objects carry React's own `Symbol.for` tags, so code that checks `$$typeof` (react-is, libraries) recognizes them.
5. `Children.map`, `forEach`, `count`, `toArray` and `only` follow React's traversal: empty nodes count and are passed to the callback, nested arrays and iterables are flattened, and mapped elements get React's keys (`.0`, `.$key`, `.0:1`, a mapped key prefixed with `key/`).

#### Fibers

A fiber is kept per rendered child: an element, a text, an array, or a portal. It records its kind (host, text, function, class, fragment, provider, consumer, portal, suspense, activity, lazy, memo wrapper, root), its type, its key, its props, the element it last rendered, its parent, its children in order, its depth, its root, its DOM node (or container), its hooks, and flags. Its kind comes from its type once, when it is created:

1. A string is a host element; a function with `prototype.isReactComponent` is a class; another function is a function component.
2. A memo of a plain function or of a forwardRef is a function fiber that also holds the memo's compare (shallow equality by default); a memo of anything else is a wrapper with one child.
3. A forwardRef is a function fiber whose render function takes `(props without ref, ref)`.

#### Reconciling a child list

1. Turn the children into items: an array as is, another iterable through `Array.from`, anything else as a list of one. null, undefined and booleans are holes: they produce no fiber but keep their position. A promise is read with `use` (it suspends until settled).
2. Each item's key is its element key (a string) when it has one, otherwise its position (a number). The two kinds never collide, and no key string is built per render.
3. Walk old fibers and items together while keys and types match, updating in place. This covers re-renders that change no structure, with no map.
4. If anything is left, map the remaining old fibers by key. For each remaining item, reuse the old fiber with its key and the same type, or create a fiber. A reused fiber whose old index is below the highest old index reused so far has moved.
5. Unmount every old fiber left in the map. When every old child goes and nothing new comes, and the children were all of a host's content, empty the host with one `textContent` write instead of one removal per node.
6. If anything was created or moved, flag the nearest host (element, root or portal) as needing placement.
7. If a child throws (an error or a thenable), the parent keeps every live fiber, the new ones made so far and the old ones not yet reached, marks itself dirty and rethrows. The boundary that catches can then retry or unmount the whole subtree. A fiber that throws while mounting is unmounted first: nothing of a failed mount is committed.

An element update is skipped when the element is the same object as last time and the fiber has no pending update; this skips the whole subtree. Hoisted JSX, `children` passed through, and the React Compiler's memo cache all produce such elements. A memo fiber is also skipped when its compare says the props are equal and the ref is the same.

#### Placing DOM nodes

Only hosts place nodes. A component or fragment adds no DOM node of its own, so a host's DOM children are the top-level nodes of its child fibers, in order, skipping portals (they place their own) and hoisted metadata. After a host renders, and after a component re-renders from the update queue, a host flagged for placement brings its live children to that list:

1. Collect the wanted list. Skip live nodes that this host did not place (a portal's, another root's, a library's); each placed node records its host.
2. Keep the common prefix and suffix of the live and wanted lists. The rest of the live list is the old window.
3. If the old window is empty, insert the wanted window before the first node of the suffix, or append when there is no suffix.
4. Otherwise number the old window's nodes. For each wanted node, record its old number, or -1 for a new node. Remove old nodes that are not wanted.
5. Keep the longest increasing subsequence of those numbers in place. Walking right to left, insert each new node and move each other node before its right neighbour, which is already final. That is the minimum number of moves.
6. A move uses `moveBefore` when the parent is in the document, so the moved node keeps focus, selection, a playing video and running animations. React moves with `insertBefore`, which resets them.

#### Host elements

1. A new element is created in the namespace of its host: SVG under `<svg>` (HTML again under `<foreignObject>`), MathML under `<math>`. A `<select multiple>` is made multiple at creation, as React does.
2. `<title>`, `<meta>` and `<link>` (a stylesheet link only with `precedence`; none with `itemProp`, `onLoad` or `onError`) go into `document.head`, React 19's document metadata.
3. Props are diffed against the last props. Removed props are cleared first, then changed props are set, in props order. `className` is `class`, `htmlFor` is `for`, `httpEquiv` is `http-equiv` and `acceptCharset` is `accept-charset`. Every other HTML prop is set as an attribute by its own name, which HTML lowercases.
4. On SVG elements, camelCase props become kebab-case except the attributes SVG spells in camelCase (`viewBox`, `preserveAspectRatio`, `gradientUnits` and the rest of React's list). `xlink*` and `xml*` props get their namespaces.
5. null, undefined, functions and symbols remove an attribute. Booleans: React's boolean attributes are present (empty) or absent; `data-*`, `aria-*` and the "booleanish" attributes (`draggable`, `spellCheck`, `contentEditable`, `value`) are written as "true" or "false"; any other attribute is removed.
6. `style` objects are diffed by property. Numbers get `px` except unitless properties and custom properties (`--x`); a custom property is set with `setProperty`.
7. `dangerouslySetInnerHTML` writes `innerHTML` when its `__html` changes. A single string or number child is the element's text, updated in place, with no text fiber.
8. Inputs set every other prop first, then `type`, `value` (also as the `value` attribute, as React syncs it), `checked` and `name`, in React's order. Textareas take `value` or `defaultValue` as their text. Selects take `value` or `defaultValue` after their options exist.
9. A function `action` on a form or `formAction` on a button stores React's placeholder `javascript:` URL in the attribute and listens for submit.
10. `autoFocus` focuses the element with the layout effects of its mount. A host's `ref` is detached when it changes and attached with layout effects. A callback ref may return a cleanup, which runs instead of calling it with null.

#### Components

1. A function component runs with the fiber as the current one and the hook index at zero. A state update during its own render reruns it at once (up to 25 times), which is React's "derive state while rendering" pattern.
2. Its effects whose deps changed are collected during the render and committed only after its children rendered without throwing. Insertion effects run then; layout and passive effects are queued, children before parents.
3. A class component follows React's lifecycle order: constructor, `getDerivedStateFromProps` (or the legacy will-mount and will-receive hooks when neither it nor `getSnapshotBeforeUpdate` exists), `shouldComponentUpdate` (or the pure shallow compare; a forced update or a changed context skips it), the will-update hook, `getSnapshotBeforeUpdate`, render, then `componentDidMount` or `componentDidUpdate` and the `setState` callbacks with layout effects. Class props exclude `ref`; the ref gets the instance.
4. A class with `getDerivedStateFromError` or `componentDidCatch` is an error boundary. When its subtree throws a non-thenable, it unmounts its children, merges the derived state, queues `componentDidCatch` (and the root's `onCaughtError`, default `console.error`), and renders again. An error with no boundary unmounts the root's content and goes to the root's `onUncaughtError` (default `reportError`).

#### Context

1. A context is its own provider (`<Ctx value>`); `Ctx.Provider` is the same object, and `Ctx.Consumer` takes a render function.
2. Reading a context walks up the fiber parents to the nearest provider of it and subscribes the reader there; with no provider the reader gets the default value.
3. When a provider renders with a value that is not `Object.is` the last one, it schedules every live subscriber. Readers behind a memo or an unchanged element therefore update without their ancestors rendering.

#### Hooks

`useState` and `useReducer` apply an update to the hook's value when it is dispatched and schedule the fiber, or skip it when the value is `Object.is` the old one. `useEffect`, `useLayoutEffect` and `useInsertionEffect` compare deps with `Object.is`. `useMemo`, `useCallback`, `useRef`, `useImperativeHandle` (a layout effect), `useId` (a per-root counter, `_r_<n>_` with the root's `identifierPrefix`), `useDebugValue` (nothing), `useSyncExternalStore` (and the selector version libraries import), `useEffectEvent`, `use` (a context, or a thenable tracked through its `status`, `value` and `reason` fields as React 19 does), `useTransition`, `useDeferredValue`, `useOptimistic`, `useActionState` and the React Compiler's `c(size)` cache (filled with React's memo sentinel) are all there, and `react-dom`'s `useFormStatus`.

Transitions render synchronously. Each transition keeps a list of what to do when it finishes; an async transition finishes when its promise, and every action queued inside it, settles. That is when `useTransition`'s pending flag clears, optimistic values added inside it are dropped, and a form whose action ran in it is reset.

#### Updates and effects

1. An update marks its fiber dirty and queues it once. Outside an event, the queue renders in a microtask. Inside an event handler it is batched until the handler returns, then rendered in a microtask that also runs the passive effects (React's sync lane). If the event's target is a controlled field, the queue renders at once and the field is shown its props again (a rejected keystroke disappears).
2. `flushSync(fn)` runs `fn` and renders the queue, layout and passive effects before returning.
3. Rendering takes queued fibers in depth order. A fiber is rendered in place (with the namespace and placement host of its position) unless an ancestor already rendered it in the same pass.
4. Before rendering queued work, the passive effects of the last commit run. After the queue empties, layout effects run (all cleanups first, then all creates). Updates they make render before the browser paints.
5. Passive effects run after paint, posted through a `MessageChannel`, unless a render needs them first.
6. Unmounting runs layout and insertion cleanups and `componentWillUnmount` at once, parents first, detaches refs, and queues passive cleanups ahead of the next passive effects. Dead fibers' queued effects never run.

#### Suspense and Activity

1. A Suspense boundary renders its children inside one fragment fiber. When they throw a thenable, the boundary shows `fallback` in a second fiber after them.
2. If the children had never been shown, they are unmounted, as React discards a suspended mount. If they had, their top-level elements get `display: none !important` and their texts are emptied: the old content stays mounted, with its state, its DOM and its effects. The fibers on the path to the thrower stay dirty.
3. When the thenable settles, the boundary renders again. The dirty path re-renders even where elements did not change. Once the children render, they are shown again (with their own `display` restored) and the fallback is unmounted. React 19 waits up to 300 ms before revealing content after a fallback; this layer reveals it at once.
4. `lazy` suspends until its loader settles, then renders the module's default export. `use(promise)` and promises rendered as children suspend in the same way.
5. `<Activity mode="hidden">` hides its content like a suspended boundary and runs its effects' cleanups. Becoming visible again shows the content and queues its layout and passive effects again.

#### Events

1. A prop `onX` (or `onXCapture`) makes every root and portal container listen for the native event type, once per type, added when a prop first uses it. `onDoubleClick` is `dblclick`, `onFocus` and `onBlur` are `focusin` and `focusout`.
2. A dispatch walks up the DOM from the target to the first element this root rendered, then up the fiber parents, so events bubble through portals as in React. Capture handlers run outermost first, bubble handlers innermost first, and `stopPropagation` stops after the current element. An event seen by one root's listener is not dispatched by the same root again.
3. Handlers get a synthetic event: the native event's fields, with its methods bound to it, plus `nativeEvent`, `currentTarget`, `isDefaultPrevented`, `isPropagationStopped` and `persist`.
4. `onChange` follows `input` events on text fields and textareas, `change` on selects and file inputs, and `click` on checkboxes and radios when their checked state changed since last seen.
5. `onMouseEnter`, `onMouseLeave`, `onPointerEnter` and `onPointerLeave` are derived from over and out events, as React does: leave fires on each element left, innermost first, and enter on each element entered, outermost first.
6. Events that do not bubble (scroll, load, error, toggle, invalid, media events, dialog close and cancel) get one listener on the element.
7. A submit whose form (or submitter) has a function action prevents the default, sets the form's status for `useFormStatus` to pending, and runs the action with the form's `FormData` in a transition. When the transition finishes, the status returns to idle and the form resets.

#### Roots and hydration

1. `createRoot(container)` empties the container on its first render. `render` queues it; `unmount` removes everything at once and stops listening.
2. `hydrateRoot(container, element)` renders at once and claims the server's nodes instead of creating them. A host keeps a cursor over its server children, skipping comments and doctypes. A new element claims the next node when its tag matches, and creates a node otherwise. Text claims the next text node, splitting it when the server merged adjacent texts, or patches its data. Attributes and text that differ are patched in place. Server nodes nobody claimed are removed (comments, such as React's `<!-- -->` separators, stay).
3. `createPortal(children, container)` renders into `container`, appending its nodes and listening there.

#### What is left out or differs

1. Rendering is synchronous: no time slicing, priorities or interruptible renders. A transition renders at once, and its pending state is visible only while an async action runs. A suspended update shows the fallback, keeping the old content hidden rather than keeping it on screen.
2. No server rendering, no Server Components and no development checks or warnings. `act` flushes everything; `captureOwnerStack` returns null.
3. `useId` values are unique and stable but do not follow React's tree-position scheme, so they match server-rendered ids only by chance.
4. Hydration patches mismatches in place instead of re-rendering a Suspense boundary on the client, and does not replay events that arrive before it.
5. ViewTransition, SuspenseList ordering and Profiler timings render their children only; `addTransitionType` does nothing.
6. Class components need the class build (above).

### Integration with herdsman web

Since the views migration (2026-10-27) herdsman web renders with the view layer only: 24 modules use `html` views, `render` for containers it owns, `mount` and `update` for persistent components that place their own root (panels, the messages and changes views, the team board), `list` for every keyed list and `runs` for inline content of data-driven length (search highlights, inline markdown, highlighted code). `dom.ts` became `icons.ts`, which only builds icon views; it no longer has `h()`, `patch()` or `each()`.

1. Morph (Layer 3) and `renderDirect` have no user in the app. Both stay exported: morph for code that patches a tree it did not render, `renderDirect` for nodes moved out of their container.
2. The `herdsman-patch` app of the herdsman-cases benchmark keeps its own copy of the old `h()`, `patch()` and `each()` as the reference renderer the other apps are checked against.
3. A browser suite (`make web-e2e`) runs the built app and its forms in headless Chrome; view-layer changes run it before they are committed.

The history of the earlier `patch()` integration (handler transfer in `h()`, `each` and `unkeyed`, and the call sites converted step by step) is in this document's change log and in `plans/each-migration.md`.

---

### Types

The types are designed to make wrong uses fail to compile while erasing completely: nothing on the hot paths (mount, update, list reconciliation) runs for the sake of types.

#### Hole markers

There is one marker per hole kind: text, class, attribute, property and event, plus a slot marker for a child hole that blocks leave empty for the view layer. Each is a generic function of type arguments only that returns a branded number. At runtime the function returns a small negative code and is called once per template call site, when the literal is evaluated. At compile time the brand records two things: the value type the hole accepts and the kind of position it must sit in. Value types are bounded by kind:

1. Text holes accept strings, numbers, bigints, booleans, null and undefined (null and undefined write empty text); string by default.
2. Class holes accept strings, null and undefined; string by default. They belong on HTML elements only (see compiling, step 5).
3. Attribute holes accept strings, numbers, bigints, booleans, null and undefined (null, undefined and false remove the attribute; true sets it empty); string by default. The type argument narrows them, for example to the two strings of an ARIA state.
4. Property holes are typed by an element type E and a property name P. P must be a writable property of E that is not a function and does not start with `on`; read-only properties, methods and handler properties are rejected. The hole accepts exactly E's type for P, nothing wider (null only where the property itself takes null). Both type arguments are required; with P left out the marker does not compile, and with both left out the hole accepts nothing. A property the DOM types do not declare, such as a custom element's, is typed by describing the element as an intersection of an element type and an object type that has the property; there is no untyped escape in views.
5. Event holes are typed by an event name K and an element type E (HTMLElement by default). K must be a name for which E has an `on` handler property, or a name in E's event map (the HTML, SVG or plain element event map) that has none, such as compositionstart. Only declared properties count: an index signature on the element type (a form's named-control access) is ignored, so a form's handler names are found. The hole accepts a function called with the element (of type E) as `this` and the event type that handler property receives (a pointer event for a click, an input event for input, a keyboard event for keydown), or, for a name without a property, the event map's type for it (a composition event), or null. K must be a single name: left out, or given as a union, the hole accepts nothing. The event marker is the only marker an `on` name accepts, and it is accepted nowhere else (compiling, step 6).
6. Slot holes accept any type; unknown by default. The view layer's child marker is a slot hole (see View types).

A plain number in a hole position is an untyped hole that accepts unknown, so untyped block templates keep compiling; view templates do not accept them.

#### What is inferred

The tag function captures its interpolations as a constant tuple type and maps each element to its value type, giving the template's value tuple, a readonly tuple with one entry per hole. From that tuple:

1. Mounting takes exactly that tuple: a missing value, an extra value, or a value of the wrong type is a compile error.
2. A block's valid hole indexes are the tuple's numeric keys, so setting or reading hole number n, where n is past the last hole, is a compile error.
3. Setting hole i takes exactly the i-th value type, and reading hole i returns exactly the i-th value type.
4. A helper type gives the block type a template mounts, so code that stores blocks (a list's entries, a selected row) names it without repeating the tuple.
5. A keyed list is generic in its item type, its entry type and its key type; the key function must return the declared key type, and the reconciler's key arrays and index map carry that type. When the entry type has no root property, a root function is required.
6. A block obtained for an element made without one exposes setting and patching but not reading values.

#### What cannot be inferred, and how it is covered

1. TypeScript sees a tagged template literal's text only as an untyped string array, so it cannot see where a hole sits. The position-to-kind rule is therefore checked when the template compiles (step 6 of compiling), once per template, which catches a class marker placed in text, a property marker placed on a plain attribute or an `on` name, an event marker placed anywhere but an `on` name, and a class marker on an SVG element, the first time the code runs.
2. The event name in the markup is not compared with the event marker's name: the marker is a number at runtime and its name exists only in its type, so an `oninput` attribute typed with a click marker compiles and runs the handler on input.
3. Whether a property name in the markup matches the property marker's name is likewise unchecked.
4. Two views whose value tuples are identical have the same description type: a type cannot tell the call sites of two tagged templates apart.
5. Whether a child hole sits inside SVG content is not tracked: a plain child hole accepts HTML and SVG descriptions alike. A hole inside SVG content is typed with the SVG child type to reject HTML views.

#### View types

1. A view is typed by its value tuple and its namespace (HTML or SVG), inferred from its markers and its tag; calling it with a wrong tuple is a compile error.
2. Calling a view returns a description typed by exactly that value tuple and namespace. Descriptions are invariant in the tuple: a description of a view with another tuple, narrower or wider, is another type. The description type with no type arguments is any description, of either namespace; one with only the SVG namespace is any SVG description. A helper type gives the description type of a view. The brand is a phantom: nothing at runtime carries it, and neither descriptions nor list descriptions can be built by hand.
3. `list(items, key, render)` infers the item type from the items alone (the key and render functions cannot widen it), the key type from the key function's return, and the description type from the render function's return. The key function takes one parameter, so a function of the item and its index does not type-check: an index can never be a key. The list description carries the description type and the key type, covariantly.
4. The child marker takes a type argument bounded by what a child hole accepts (text, nothing, a DOM node, any description, any list description) and defaulting to all of it. Narrowing it gives holes that accept, for example, only text, only a list of one view's descriptions keyed by strings, only one view's description or nothing, or only SVG content (text, nothing, SVG elements, SVG descriptions and lists of them). A plain array is never a child.
5. A handler hole is an event marker; a property hole is a property marker typed by element and property, as above.

#### Zero runtime cost

The types erase completely, and the markers stay constant functions evaluated once per template literal:

1. Nothing new runs per render: the event marker shares the property hole's runtime kind, so the view's per-template shape, mounting, patching, child holes and list reconciliation are unchanged. The only new runtime work is in compiling (once per template): choosing the SVG wrapper, the namespace test for `class`, and the event-position comparison.
2. Proof, 2026-10-27: the declarative benchmark app (`bench/herdsman-cases/apps/herdsman-blocks-declarative`) and the lower-layer app were bundled before and after with syntax and whitespace minification but names kept, and split into statements. The only differing statements were the marker functions (the event marker replacing the property marker the apps had used for handlers, and the marker-name list for error messages), the template constructor's SVG parse and position check, the attribute walk's namespace test for `class`, and the call sites' marker names. Every statement of mounting, patching, child holes, the trampoline and list reconciliation was identical. The fully minified declarative bundle grew from 18,308 to 18,624 bytes (7,561 to 7,725 gzipped), all of it in those once-per-template parts.
3. The benchmark runner's DOM gate passed for both apps after the change.

#### Compiler settings

The library compiles under herdsman's settings (strict, unchecked index access, exact optional property types, no implicit override, no unused locals or parameters, no switch fall-through, verbatim module syntax, isolated modules) plus a stricter set applied only to the library's files: isolated declarations (every export has an explicit type, so declarations follow from syntax alone), erasable syntax only (no enums, no namespaces with values, no constructor parameter properties, so the source runs under any type-stripping runtime), no property access from index signatures, no implicit returns, no unreachable code, no unused labels, unchecked side-effect imports reported, strict built-in iterator returns, every file treated as a module, consistent file name casing, and no replacement of the standard library's types by installed packages. Kind codes are plain top-level constants rather than an enum, which also lets minifiers inline them.

#### Type tests

Two test files assert exact inferred types with a compile-time equality check and list mistakes that must not compile, each marked as an expected error. An expected-error line that starts compiling makes the type check fail.

1. Blocks: the template type, the hole index union, the block type and the untyped value tuple are exact; the event marker's handler type (element as `this`, the handler property's event type) and the property marker's value type (a boolean, a number) are exact; read-only properties, methods and handler properties are not property names, and an SVG element's class name is not one.
2. Blocks mistakes: a wrong value type, an index past the last hole, a short or long value tuple, a non-function for a handler, a number for a class marker, an object for a text or attribute marker, a value outside a narrowed attribute type, a handler for the wrong event type, a handler whose `this` is another element type, an unknown event name, an event the element has no handler for, a handler for an event-map event (compositionstart) taking the wrong event type, a submit handler on a form taking the wrong event type, a misspelled event on a form, an event marker without its event or with a union of events, a wrong value for a property, null for a property that does not take it, a read-only property, a method, a handler property, an unknown property, a property marker with one type argument or none, a wrong value for a described custom property, a key function returning the wrong key type, a list entry without a root, and reading values from a block made without one.
3. Views: wrong arity, a wrong value type, `true` as a child, an untyped hole, a handler for the wrong event type or element type, a wrong property value; descriptions are exact in their tuple in both directions, carry their namespace, and are children; the list description type is inferred; child holes narrowed to a list of one view reject text, a list of another view and a single view; a text-only hole rejects a view and nothing; a hole for one view rejects another view and a list; an SVG hole rejects an HTML view; the child marker rejects non-child types; a plain array is not a child; a key from the index (named or inline), a render that does not describe a view, a key that is not a string or number, a render for another item type, and a list keyed by numbers for a hole that wants string keys do not compile.
4. At runtime, the test files check that every marker in a wrong position throws when its template compiles, including a property, attribute or text marker at an `on` name and an event marker at an attribute, property or text position, that a class marker on an SVG element throws exactly once (at compile time) under both tags, and that `svg` templates and views create SVG elements (the fake DOM records namespaces and, like a browser, refuses a class name assignment on an SVG element).

Weakening the update method to accept any value made the type check fail on the expected-error line for a wrong value type; restoring it made it pass, so the type tests do catch loosened types.

## Part 3: Verification

### Correctness properties the tests check

Tests run on a small linked-list fake DOM with counters for inserts, moves, removes, clones and class writes, so they can assert exact DOM work without a browser. Its markup parser decodes character references in text and attribute values as a browser does (numeric ones and the common named ones; an unknown name stays as written), so templates with `&lt;` and the like behave the same in tests and in Chrome.

1. The LIS routine returns a strictly increasing run whose length equals a brute-force reference, over 2,000 random arrays with "new item" slots mixed in.
2. Over 3,000 random list updates (removals, shuffles, swaps, insertions, with and without `owns`): the final DOM order equals the new items; every surviving item keeps its original entry; only new keys are created; and the number of moves equals survivors minus their LIS length, the proven minimum.
3. Every pairwise swap in a twelve-item list uses one move for neighbors and two otherwise.
4. Edge cases: empty lists, a full reverse (n minus one moves), appends and prepends with zero moves, a single removal with one remove and zero moves, clear.
5. `detach` builds with the parent out of the document, puts it back between the same siblings, and does not detach when appending to a live list.
6. An anchor confines the list; other children of the parent are never touched.
7. Templates fill text, class, attribute and property holes; skip blank writes on mount; skip identical writes on update; treat hole values as text, never HTML; compile once per call site; ignore formatter indentation but keep spaces within lines and inside `pre`; and reject templates without exactly one root element.
8. Values baked into the prototype never leak: an earlier instance keeps its values after later mounts and after the prototype changes, and a later instance's first update locates its own holes. Property holes land on the clone, never on the prototype.
9. Morph keeps matching nodes, patches text and attributes, moves keyed children with the fewest moves, replaces a changed tag, leaves a persistent reused node and `data-morph-skip` children alone, and keeps a focused field's value.
10. `patch()` keeps the live element and runs only the newest handler for each event type, including a type added or dropped between renders.
11. Moves use the state-preserving move when the parent is in the document (every move of a reversed twenty-item list), and plain insertion when it is not; move counts stay minimal either way.
12. A list whose entries are the nodes themselves reorders them and keeps their identity.
13. An element made without a block gets one on demand, cached on the element; the first set of each hole writes, the second identical set does not; instances stay independent; property holes are set on the clone.
14. Slot holes: a slot marker in content compiles to a slot, blocks never write it, and a slot marker in an attribute position is rejected.
15. SVG: `svg` templates and views are in the SVG namespace, SVG views render and reorder in child holes inside SVG content, and `class` there is an attribute; checked on the fake DOM and in headless Chrome.
16. Views: every child kind renders in place and switching kinds replaces the content; the same view patches and another view replaces, at a child hole and at the root; unchanged values write nothing; a keyed list reorders with every node kept and the minimum number of moves, stays between its siblings, owns and clears its parent and gives the slot back; an item whose view changes is replaced where it stands; nested lists move cards between lanes; a new handler each render costs no DOM write and the latest one runs, with the element as `this` and `currentTarget`; delegated handlers run innermost first and `stopPropagation` stops outer ones; a stopped keydown never reaches a listener above the render root; a render root inside another dispatches its handlers once; a non-bubbling event gets a direct trampoline; `mount` makes a root that delegates its own and its descendants' handlers wherever it is placed, `update` patches it and rejects another view or a root not made by `mount`; `renderDirect` sets handlers on the elements, so nodes moved under a delegating root run each handler once; runs render in order without wrappers between siblings, keep text nodes and same views by position and replace the rest, and clear when the hole switches kind; a focused input keeps its node and typed value across renders; a text hole after a child hole's content is written to its own node.
17. A property test renders random view templates that mix child holes (text, nothing, nodes, views, keyed lists, runs) before and after text, class, attribute and event holes, nested up to two levels, through two to four random value sets, and checks that the result equals a fresh render of the last values: the same DOM (tags, classes, attributes, merged text) and the same handlers reached by a click on every element, every list item's and nested view's handler being reached (the views in child holes, lists and runs carry delegated handlers). With deferred location in views that have child holes it finds the bug at once and shrinks it to a child hole followed by an attribute hole.
18. The benchmark runner's gate: the declarative benchmark app's DOM is identical to the reference app's after every step.

The same behaviors were also checked in real Chrome 156 (not the fake DOM): a focused input kept its node, focus, typed value and caret position through a `patch()` that reordered siblings; the kept button ran the newest handler; keyed rows kept their nodes; an unfocused input re-synced from the store; an indented template inside a table compiled correctly; and a blank class hole produced no class attribute.

### Profiling

Bun's CPU profiler with markdown output profiles the benchmark app over the fake DOM, where DOM work is pointer updates, so the profile shows the library's own JavaScript. After the changes above, the library's own functions account for a small share of samples; the rest is fake-DOM node construction.

Typing the library (0.0.1 to 0.0.2) did not slow it down. Over three profiling runs each, the medians of every operation were the same or lower afterwards: create 10,000 rows 11.8 ms before and 10.7 ms after, replace 1.43 ms and 1.25 ms, append 0.86 ms and 0.69 ms, create 1,000 rows 1.45 ms both; operations that take microseconds were within rounding. The minified benchmark bundle grew by 261 bytes (8,115 to 8,376), the marker functions and the position-check message.

In Chrome, an A/B of the 0.0.1 build against the typed build (10 interleaved iterations, all 9 tests) scored 1.006 and 1.044, with the per-test direction changing between tests and the largest test (create 10,000 rows) within 0.25%. That 3.8% gap is smaller than the 4.3% gap the noise control found between two identical builds, so it is noise. The minified hot paths of both builds were compared directly and do the same work: kind codes are inlined as literals, and the marker functions run once at module load.

### Benchmark method (replica of the official one)

For each test and iteration, in a fresh browser context:

1. Load the page, run the test's warmup clicks and wait for each to finish.
2. Apply CPU throttling where the official suite does: 4 times for partial update, select, swap and clear; 2 times for remove.
3. Start a trace (timeline categories), force a garbage collection, perform the measured click, wait for it to finish, stop the trace.
4. Duration runs from the start of the click's event dispatch to the end of the first compositor commit that follows the last script, timer, animation frame or layout event after the click, all on the click's process.

Frameworks are interleaved within each iteration, rotating which goes first, so machine drift lands on all of them. Headless Chrome on the benchmark machine (Apple M5 Pro) is not software-rendered: with the harness's flags (`--headless=new`, `--disable-frame-rate-limit`), chrome://gpu reports hardware-accelerated rasterization and compositing, with Skia Graphite on the ANGLE Metal renderer. Each test's median is divided by the best median for that test, and the score is the geometric mean of those factors weighted by the official per-test weights.

---

### Per-phase findings (2026-10-26)

The runner can also report, per test, how the click-to-commit window splits into script, style, layout, pre-paint, paint, commit and garbage collection (medians).

1. Replace 1,000 rows: with phases measured, herdsman-blocks took 73.9 ms against vanilla's 79.1 ms, with every phase equal or lower. The earlier 1.28 times gap was machine noise.
2. Create rows: the remaining cost was JavaScript allocation per row. Before the changes, script was 10.6 ms against vanilla's 6.2 ms on 1,000 rows; on 10,000 rows garbage collection was 96 ms for us, 74 ms for vanilla and 44 ms for vanillajs-lite, which never touches a cloned row's inner nodes. After merging block state into one array, reusing the argument array, dropping a per-row expando, and baking values into the prototype with lazy hole lookup, create 1,000 rows scored 1.01 times the best of vanilla, vanillajs-lite and herdsman-blocks (vanillajs-lite 1.07), and create 10,000 rows 1.02 times. Garbage collection on 10,000 rows was still 88 ms, so per-row objects (the block and its state array) remain the next target.

### Profiling the reconciler alone (2026-10-26)

With a null DOM whose inserts and removes do nothing, Bun's CPU profiler (sampling every 100 microseconds) attributed the reconciler's time, over edits of 1,000 to 100,000 keyed items, to: building the key-to-position hash map, 44%; allocating the new key and entry arrays, 27%; allocating the LIS work arrays, 14%; the LIS loop itself, about 1%. The arithmetic was never the cost; hashing and allocation were. That led to the direct-address table and the scratch buffers.

Effect, measured as the median time per reconcile over many back-and-forth reconciles of the same edit (null DOM, integer keys unless stated):

| Edit                                 | Chrome 156 before | Chrome 156 after | Bun before | Bun after |
| ------------------------------------ | ----------------: | ---------------: | ---------: | --------: |
| 1,000 items, reverse                 |           85.4 µs |          28.3 µs |    49.5 µs |   23.3 µs |
| 1,000 items, shuffle                 |           92.6 µs |          38.5 µs |    87.0 µs |   42.8 µs |
| 1,000 items, replace all             |           61.0 µs |          21.5 µs |    36.4 µs |   14.7 µs |
| 10,000 items, reverse                |          842.5 µs |         373.8 µs |   918.8 µs |  921.8 µs |
| 10,000 items, replace all            |          991.2 µs |         282.5 µs | 1,278.0 µs |  721.1 µs |
| 100,000 items, reverse               |           10.2 ms |           3.1 ms |    14.5 ms |    3.9 ms |
| 100,000 items, replace all           |           12.5 ms |           3.0 ms |    18.8 ms |    3.6 ms |
| 1,000 string keys, shuffle (control) |          155.3 µs |         157.2 µs |   179.7 µs |  181.6 µs |

In Chrome the reconciler became 2 to 4 times faster for integer keys at every size; string keys, which still use the hash map, were unchanged, as expected. JavaScriptCore gained at 1,000 and 100,000 items but not for shuffles and reverses of 10,000. In absolute terms the saving is about 50 microseconds per 1,000-row operation, small next to the browser's rendering work in the benchmark, and 0.5 to 10 milliseconds per update for lists of 10,000 to 100,000 items.

### Property-based testing (2026-10-26)

The library ships a small property testing engine for its own tests, with no dependencies: properties are executable statements checked on generated inputs (after QuickCheck, Claessen and Hughes, ICFP 2000), and every random decision a generator makes is recorded, so a failure is reduced by shrinking that sequence of choices and re-running the generator (internal test-case reduction, as in Hypothesis: MacIver and Donaldson, ECOOP 2020). Reduced cases are therefore always valid inputs (a key list never gains duplicates). Shrinking deletes chunks of choices, largest first, then lowers single choices, keeping any change that still fails, within 4,000 attempts and 5 seconds.

Properties checked, each over 1,500 to 3,000 generated cases per run: keyed reconcile output, entry reuse and exactly minimal moves across every lookup mode, with and without `owns`, anchors and an attached parent (where every move must be state-preserving); setting the same items twice does no DOM work; the LIS matches an O(n²) reference; eager and lazy blocks end in the same DOM as a fresh mount of their final values; morph turns any live tree into any fresh tree (random tags, keys, attributes and text, up to three levels), and morphing again does no DOM work. A planted bug (two moves for an adjacent swap) shrinks to the two-item list zero, one becoming one, zero within a second.

Two lessons recorded: a property's measurement must not include building its own input (twice a test counted fresh-tree construction as the library's work), and a shrinker must be bounded by attempts, not by successful reductions, or a failing slow property hangs.

### Performance fuzzing (2026-10-26)

A mutational fuzzer in the style of PerfFuzz (Lemieux, Padhye, Sen and Song, ISSTA 2018) searched edits of sibling rows (keyed, unkeyed and mixed, with insertions, removals, moves and swaps) for the largest DOM work per unit of required change, where the required change is inserted plus removed plus moved rows (moved: survivors outside the longest run already in order). It keeps any input that sets a new maximum for any counter (all writes, text writes, attribute writes, moves, time).

1. **Before:** an unkeyed prepend of one row to 60 cost 61 writes (every row shifted its order-based match and had its text rewritten); among mixed `li` and `p` rows, one prepend cost 18 moves and 20 text writes. Keyed lists were already optimal.
2. **After trimming identical ends:** the worst case fell to 29 writes per unit of change: an unkeyed row moved to the front still shifted every match inside the window.
3. **After content keys:** over 30,000 inputs the worst case was 1 write per unit of change: a prepend is one insert, a move one move, a changed row one text write.

In Chrome 156, the new morph on 1,000 rows (median of 25, two rounds): unchanged unkeyed re-render 0.7 to 0.8 ms (before 5.7 to 8.6 ms), one text change 0.8 ms (4.4 to 7.0), unkeyed prepend 0.8 to 0.9 ms (4.6 to 6.3), unkeyed move 2.4 to 3.5 ms (5.1 to 6.1), unchanged keyed 0.7 ms (5.7 to 7.0), keyed reverse 3.3 ms (6.2 to 6.5).

### Branchless and bitwise techniques (measured, not adopted, 2026-10-26)

Each variant passed the full test suite and was timed against 0.0.4 with the same null-DOM harness, in Bun and in Chrome 156, two interleaved rounds each; "noise" below is the baseline's own difference between its two rounds.

1. **All at once** (a branchless lower-bound search in the LIS that always halves and picks the step with a mask, a bitwise append test, an unsigned single-compare range check and a mask instead of a ternary for the table lookup, minimum and maximum instead of compare-and-branch in the integer-range scan, and an OR'd moved flag with a running maximum): no case became meaningfully faster on either engine, and nearly sorted input became much slower (LIS 2.3 to 7.5 times, moving one item 1.8 to 2.8 times). Cause: the rewrite also removed the append fast path, a branch that is almost always taken on nearly sorted input and lets the search be skipped.
2. **Only the bitwise lookup, range and flag changes**: no consistent gain; often slower, up to 1.5 to 1.7 times in Bun.
3. **Only the branchless search, keeping the append fast path**: 15 to 25% faster on large random reorders (100,000-item shuffle 0.81 in Chrome and 0.77 in Bun; random LIS of 100,000 0.75 and 0.79), but slower on small random input (1,000-item random LIS 2.35 and 3.06 times) and on nearly sorted input (1.1 to 1.6 times).
4. **Hybrid** (branchless search only when the tails array is longer than 128): the only gains beyond noise were 100,000-item random reorders (shuffle 0.78 in Chrome with 6% noise, 0.73 in Bun with 11% noise); nearly sorted input in Bun was 1.33 to 1.38 times slower.

Why branches mostly won here: in the prefix and suffix loops, the hole-kind switch and the swap check, every branch goes the same way for a whole operation, so prediction is nearly perfect; only a binary search over long random tails mispredicts, and that is a small share of the reconciler. The library keeps the classic search, because the one reliable win (huge random shuffles) is rare in a UI, while nearly sorted reorders (moving one card, inserting, re-sorting an already sorted list) are common.

### Microbenchmarks in Chrome 156 (2026-10-26)

1. **Batched cloning** (cloning a fragment of 10, 50 or 250 prefilled rows at once, as vanillajs-lite does) against one clone per row, timed to forced layout, medians of 41 runs for 1,000 rows and 15 runs for 10,000 rows: 1,000 rows took 34.6 ms singly and 36.1 to 39.6 ms batched; 10,000 rows took 409.6 ms singly and 407.6 to 422.3 ms batched. Batching brought no gain, so the library clones one row at a time.
2. **State-preserving move against insertion**, 1,000 rows, medians of 31 runs: swapping two rows took 2.6 ms with `moveBefore` and 2.3 ms with `insertBefore`; reversing 999 rows took 29.2 ms and 31.6 ms. Same speed within noise. A focused input inside a moved row kept focus with `moveBefore` and lost it with `insertBefore`; `moveBefore` on a parent outside the document threw a hierarchy request error.

## Part 4: Versioning and packaging

The version starts at 0.0.1 and lives in one place, the module's exported `VERSION` constant; the package's `VERSION` file and change log repeat it. Bump the patch number for fixes and speedups that keep behavior, the minor number for new behavior or options, and record each release in this document's change log and the package's.

Each release is a zip named after the library and version, holding one folder of the same name with:

1. The library source as a single TypeScript module, with no dependencies and no package.json.
2. A minified ES module build of it for JavaScript consumers, with a declaration file beside it that re-exports the types from the TypeScript source in the same package. No separate declaration build is needed: the source is what `bun check` verifies, and code importing the minified module is type-checked against it.
3. The library's tests and the fake DOM they run on, runnable with Bun alone.
4. The benchmark tools: the js-framework-benchmark app built with the library, its page and entry, the site builder, the Chrome runner, the Bun profiling script, and the raw result files of the runs this document reports.
5. This document as the package's spec, a README with usage and checks, a change log, the GPL-3.0 license of the herdsman repository it comes from, and a `VERSION` file.

Type checking uses `bun check` (Bun 1.4.4, the latest release on 2026-10-26), never `tsc`: once with the herdsman settings and once with the stricter library settings. A release is ready when, from a fresh unzip, formatting of the authored files passes, both type checks pass, the tests pass, and the built module imports and reports the same version as the source.

## Part 5: Research notes (2026-10-26)

What published work and current browser engineering say about the costs this library targets.

1. **General tree diff is too slow for UI.** Tree edit distance takes cubic time even with the best decomposition algorithms: Demaine, Mozes, Rossman and Weimann give an O(n³) algorithm and prove it optimal within that class (earlier bounds: Zhang and Shasha O(n⁴), Klein O(n³ log n)) ([arXiv cs/0604037](https://arxiv.org/pdf/cs/0604037)). UI libraries therefore restrict the problem to keyed child lists and known dynamic holes, as this library does.
2. **Keyed moves.** Million.js (Bai, arXiv 2202.08409, ACM SAC 2023) pairs a compiler that flattens and hoists static structure with keyed moves based on O(ND) difference algorithms ([arXiv 2202.08409](https://ar5iv.labs.arxiv.org/html/2202.08409)). This library takes the minimum-move route instead: the longest increasing subsequence gives the exact minimum number of moves.
3. **Why per-row objects cost main-thread time.** Blink's DOM objects and V8's JavaScript objects form one traced heap; sweeping moved to background threads in Chrome 78, but destructors of dead DOM objects still run on the main thread ([V8 blog, May 2020](https://v8.dev/blog/high-performance-cpp-gc)). Creating fewer JavaScript objects and touching fewer DOM nodes from JavaScript reduces that work, which is what lazy hole lookup and mounting without a block aim at.
4. **State-preserving moves.** `moveBefore()` shipped in Chrome 133 (February 2025) and moves a node without resetting focus, iframe, video, animation, fullscreen or dialog state ([Chrome for Developers](https://developer.chrome.com/blog/movebefore-api)). The library uses it for every move when available (measured above: same speed as insertion).
5. **Native template instantiation is not here yet.** As of the W3C web components meeting of 2025-03-26, DOM Parts and template instantiation were still proposals; an experiment showed mixed performance, some benchmarks faster and others 10 to 20% slower ([minutes](https://www.w3.org/2025/03/26-webcomponents-minutes.html)).
6. **Benchmark rules.** The official benchmark notes, but does not reject, explicit event delegation in client code (note 801), manual DOM manipulation (note 772) and per-row selection flags (note 800); frameworks themselves may delegate events, and the HTML must match the vanillajs reference ([README](https://github.com/krausest/js-framework-benchmark)). The current leader's entry carries notes 772, 801 and 1261. Built-in event delegation in the library would let an entry avoid note 801.
7. **Rendering work outside any diff.** In our traces, script was about 11 to 12% of the measured window for creating rows; style, layout and paint were the rest. CSS containment and `content-visibility: auto` let the browser skip layout and paint for offscreen content ([web.dev](https://web.dev/articles/content-visibility)); that is where herdsman's long views can gain more than from any diff algorithm.

## Open questions

1. Mounting without a block (0.0.3) removes the per-row block object; its effect on garbage collection and on the clear rows test is being measured.
2. Should the library offer built-in event delegation (for `on*` holes), which would avoid per-row handler closures and benchmark note 801?
3. Which herdsman views benefit from `content-visibility: auto` and `contain: content`?
4. Morph's per-node cost on large herdsman views has not been measured yet.
5. Whether `patch()` should become the default for all of herdsman's re-rendered views, or stay per call site.
6. The other top entries of the official table that need an npm install and their own build step (thane, doohtml, sonnet, deleight, alien-signals, mikado) have not been run in the replica; installing and running their build tools needs the owner's approval.

## Change log

Newest first.

- 2026-10-27: Class components move to a build option (`herdsman-blocks-react-classes.ts`); the default module leaves them out (1.2 KB gzipped) and names the option when a class renders. `addTransitionType` exported (it has no effect, as no view transitions start). The React compatibility check adds scenarios with the five React libraries npm downloads most and runs both builds: 31 of 31 identical to React with classes, and without them every scenario that renders no class. Part of 0.1.0.
- 2026-10-27: Version 0.1.0. Layer 5, the React API (`herdsman-blocks-react.ts`): React 19's client exports for `react`, `react-dom`, `react-dom/client` and the JSX and React Compiler runtimes, in one module apps alias those imports to. Placement reuses Layer 2's minimum-move algorithm and moves with `moveBefore`. Verified against React 19 by a differential check in headless Chrome (`bench/herdsman-cases/react-compat.ts`, 25 of 25 scenarios identical; `make react-compat`, also run in CI) and by unit tests on the fake DOM. Benchmarked on herdsman's screens against React 19 and TanStack Redact 0.2.1 (Part 1).

- 2026-10-27: Version 0.0.9. Delegated events find instances by descent from the render root along the event's path (child holes, list entries, runs), so views whose handlers are all delegated put no mark on their elements. Official benchmark, Chrome script time, 3 interleaved rounds with an A/A pair (15 samples): create 10,000 rows 13.5 to 12.9 ms (95% CI of the ratio 0.92 to 0.97), clear 7.0 to 6.6; other tests unchanged.
- 2026-10-27: "Integration with dom.ts" rewritten as "Integration with herdsman web": the app uses views only; morph and `renderDirect` have no app user and stay exported.
- 2026-10-27: Event names ignore index signatures, so `on<"submit", HTMLFormElement>()` types `this` as the form and the event as a submit event. Types only. Part of 0.0.8.
- 2026-10-27: The fake DOM decodes character references in template markup (numeric and common named ones), as browsers do. Test infrastructure only.
- 2026-10-27: `mount(view)` creates a view's root element as its own delegating render root, and `update(root, view)` patches it, for components that place their root themselves; any view works, child holes included. Part of 0.0.8.
- 2026-10-27: Event holes take events with no `on` property on the element, typed from the element's event map (compositionstart, compositionupdate and compositionend, which are delegated); a direct trampoline for such an event is added as a listener. The fake DOM's elements carry the common `on` properties (null when unset), like a browser's. Part of 0.0.8.
- 2026-10-27: Fix (0.0.8): a view with child holes deferred locating its text, class, attribute and delegated holes, but by the first update a child hole's content could stand before them, so the update wrote to the wrong node. Such views now locate every hole at mount. Unit test with the reported case and a property test over random templates with child holes, checked against a fresh render.
- 2026-10-27: `runs(items)`: unkeyed inline content for child holes, text and views reconciled by position with no wrapper elements; typed `RunsNode<N>` in `Child` and `SvgChild`. Part of 0.0.8.
- 2026-10-27: `renderDirect(container, view)`: renders with every handler on its own element and no listener on the container, for bridges that move a view's nodes elsewhere; render roots skip such instances when delegating. Part of 0.0.8.
- 2026-10-27: Version 0.0.8. Views delegate handlers of bubbling events to the render root (one listener per event type on the `render` container; `this` and `currentTarget` are the element; innermost first; `stopPropagation` honored), so mounting a row with handlers walks none of its nodes; non-bubbling events keep direct trampolines, which now find their instance on the template root. Instances keep the description's values array instead of copying it, skip a description rendered again, and locate write, eager and delegated holes separately (Layer 1 gains `Template.locator` for a subset of holes). Official benchmark, Chrome script time pooled over 10 samples against 0.0.7's layer: create 10,000 rows 18.7 to 13.5 ms (raw blocks 12.0), create 1,000 rows 2.1 to 1.4 (raw 1.4).
- 2026-10-27: Version 0.0.7. Strict view types at no runtime cost: an `on<event, element>()` marker is the only one an `on*` name takes (a property marker there now fails at compile time); `prop<element, property>()` takes exactly that property's type; `svg` templates and views parse inside an `<svg>`, and `class` on SVG elements is an attribute hole; descriptions carry their view's exact value tuple and namespace, list descriptions their description and key types; `child<C>()` narrows what a hole takes; views reject untyped holes. Three more strict compiler settings. Headless Chrome check for SVG and typed holes. Bundles before and after differ only in markers and once-per-template compiling.
- 2026-10-27: Version 0.0.6. Layer 4, views (`herdsman-blocks-view.ts`): templates called with values return descriptions, `render` patches only changed holes, child holes take text, nothing, nodes, views or keyed `list()`s, and handlers go through per-template trampolines. Blocks gain the slot hole kind and, for layers above, `Template.clone`, `kind` and `write`. Declarative benchmark app added.
- 2026-10-27: Phase 2 of the `each()` migration: plain arrays are a type error in `dom.ts` children. The 67 sites in herdsman web use `each` (keyed by the data's identity), `unkeyed`, or a literal tuple. No change to the library.
- 2026-10-27: Renamed from herdsman-dom-diff to herdsman-blocks: the module, its tests, this spec and the release name. No behavior change; the version stays 0.0.5.
- 2026-10-26: `each()` and `unkeyed()` in herdsman's `dom.ts`, with branded `Keyed` and `Unkeyed` types and a duplicate-key check removed from production builds by a build-time constant. Five patched lists migrated; migration guide in `plans/each-migration.md`. Compatibility verified against the real `herdsman web` with a fake herdr in Chrome 156.
- 2026-10-26: Full run of every version (0.0.1 to 0.0.5) against `re`, `vanillajs` and `vanillajs-lite`, with 0.0.4 and 0.0.5 as an identical-code control.
- 2026-10-26: Version 0.0.5. Morph trims deeply identical children at both ends (native compare, handlers and form state still carried over) and keys the differing window by content for unkeyed nodes, falling back to order among leftovers. Found by a PerfFuzz-style fuzzer (worst DOM work per unit of change: 61 before, 1 after); unchanged re-renders of 1,000 rows about 8 times faster in Chrome. Property-based tests with a choice-sequence shrinker.
- 2026-10-26: Branchless and bitwise variants of the reconciler and the LIS measured in Bun and Chrome; not adopted (results in the spec).
- 2026-10-26: Version 0.0.4. Direct-address table for dense integer keys in the general case (hash map otherwise, scan for small windows); scratch buffers per nesting depth for the source and LIS arrays. The reconciler alone became 2 to 4 times faster in Chrome for integer keys. Tests for every lookup mode and for a reconcile nested inside update.
- 2026-10-26: Version 0.0.3. Mounting without a block (`mountRoot`) with on-demand blocks (`block`) that cannot read values; list entries may be any value given a root function; morph keeps no wrapper per node; moves use `moveBefore` when the parent is in the document. Batched cloning measured and rejected. Research notes added.
- 2026-10-26: Chrome A/B of 0.0.1 against 0.0.2 and an A/A noise control: no measurable runtime cost from the types. Corrected the head-to-head reading: within this machine's noise, herdsman-blocks is tied with `re`, not ahead.
- 2026-10-26: Version 0.0.2. Strict types: typed hole markers for text, class, attribute and property holes; the value tuple, hole indexes and per-hole value types inferred from them; a once-per-template position check for markers; keyed lists generic in their key type; the library compiled under an extra strict settings file that herdsman's web check now runs; type tests with expected errors. Kind codes are plain constants and constructor parameter properties are gone (erasable syntax only). The package's declaration file re-exports the source's types instead of a `tsc` build. No runtime slowdown in profiling.
- 2026-10-26: Version 0.0.1 packaged as a zip. `VERSION` exported. The `patch()` tests moved to `dom.test.ts` so the library's own tests run without herdsman's `dom.ts`.
- 2026-10-26: Head-to-head replica run against `re`, `vanillajs` and `vanillajs-lite`: herdsman-blocks 1.052, second of four, ahead of `re`.
- 2026-10-26: Templates ignore formatter indentation (herdsman's `bun fmt` reformats tagged templates). Morph call sites in herdsman web keyed by tab and view. Block state merged into one array; mount bakes values into the prototype and looks up holes lazily. Real-Chrome verification of `patch()`. Per-phase benchmark findings.
- 2026-10-26: First version. Blocks, keyed lists with prefix and suffix trimming, the two-item swap path and the LIS general case; blank-write skipping on mount; `detach`; morph and `patch()` with handler transfer for herdsman web; test, profiling and benchmark method; first replica results.
