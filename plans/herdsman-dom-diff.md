# herdsman-dom-diff: article and design spec

A living document. It explains why herdsman-dom-diff exists and specifies its behavior precisely enough to rebuild it without reading its source. It holds no code; every procedure is written as numbered steps. Update it in the same change as any behavior change, and add a line to the change log at the end.

- Status: draft, on branch `herdsman-dom-diff`
- Last updated: 2026-10-26
- Module: `web/src/herdsman-dom-diff.ts`, used by `web/src/dom.ts`

---

## Part 1: Article

### The problem in herdsman web

The herdsman web app renders in immediate mode. Every time the store emits, a view rebuilds its whole subtree with the `h()` builder and swaps it in with `mount()`, which empties the container and appends the new nodes. That is simple and hard to get wrong, but it costs three things on every update:

1. The browser discards and rebuilds every node, then restyles and relays out the whole subtree, even when one status pill changed.
2. State that lives in the DOM is lost: keyboard focus, the text caret, a text selection, scroll positions inside the view, and whether a `<details>` is open.
3. Event handlers and nodes held elsewhere point at nodes that no longer exist.

### The idea

Keep the immediate-mode style, because views stay plain functions of the store. Change only the last step: instead of replacing the container's children, compare the freshly built tree with the live one and patch the live one until it matches. Nodes that still correspond are kept; only real differences touch the DOM.

The library has three layers, each usable alone:

1. **Morph** patches a live subtree to match a freshly built one. `dom.ts` exposes it as `patch()`, a drop-in for `mount()`.
2. **Keyed lists** reconcile an old ordered list of keyed children with a new one in the minimum number of DOM moves. Morph uses this for every child list.
3. **Blocks** are a faster path for hot, repeated markup: a template is parsed once, each instance is one deep clone, and updates write straight to the few nodes that can change. They suit large tables and logs where even building the fresh tree is too much work.

### Why this algorithm

Reordering keyed children is the hard part of any DOM diff. The minimum number of moves is known exactly: it is the number of surviving children minus the length of the longest run of them that is already in increasing old order (the longest increasing subsequence, LIS). Everything on that run stays put, and everything else moves once. Computing the LIS takes O(n log n) time, and no comparison-based method can compute even its length faster in general.

So the list algorithm aims to never exceed that move count, and to avoid computing the LIS at all in the common cases: appends, prepends, removals, in-place updates, and a swap of two rows. Those are handled by trimming the unchanged prefix and suffix and by a dedicated two-item swap check.

### What the benchmarks say (as of 2026-10-26)

The reference is the js-framework-benchmark Chrome 156 run, published 2026-10-25 (krausest/js-framework-benchmark, release "chrome156", 201 frameworks, keyed only). Recomputed from that run's raw results with the official weights, the top of the keyed table is `re` at 1.014, then `thane` at 1.019, `vanillajs-lite` at 1.021 and `vanillajs` at 1.024, where 1.000 would be the best result on every test. The top ten sit within about 3% of each other.

Our measurements, from a local replica of the official method in headless Chrome 156 (2 CPU cores, about 3.5 times slower than the official machine, with run-to-run spread of up to about 30%), median of 15 interleaved iterations per framework, run on 2026-10-26:

| Test | vanilla | re (official #1) | herdsman-dom-diff |
|---|---:|---:|---:|
| create 1,000 rows | 69.0 ms | 80.1 ms | 75.2 ms |
| replace 1,000 rows | 75.0 ms | 77.7 ms | 95.9 ms |
| partial update | 62.5 ms | 59.8 ms | 56.5 ms |
| select row | 13.9 ms | 13.3 ms | 16.3 ms |
| swap rows | 66.6 ms | 68.3 ms | 63.9 ms |
| remove row | 43.3 ms | 46.1 ms | 46.3 ms |
| create 10,000 rows | 780.7 ms | 817.6 ms | 831.2 ms |
| append 1,000 rows | 85.4 ms | 87.4 ms | 80.8 ms |
| clear 1,000 rows | 30.8 ms | 36.0 ms | 29.7 ms |
| **weighted score** | **1.029** | **1.086** | **1.076** |

The score is relative to the best of these three on each test, not to the official table.

Assessment: in this replica herdsman-dom-diff scored slightly ahead of the official leader and about 4.6% behind vanilla. With this much noise, the replace gap (1.28 times the best) is the only loss clearly larger than the run-to-run spread; select (1.23) is borderline. Whether the library would beat the official table can only be settled by the official run.

---

## Part 2: Design spec

### Terms

- **Live** nodes are in the document now. **Fresh** nodes were just built by a view and are not in the document.
- A **key** identifies a child among its siblings. Two children of the same parent never share a key.
- An **entry** is anything a list keeps per item that exposes the item's root DOM node.
- The **window** is the part of an old and a new list left after trimming the matching prefix and suffix.

### Layer 1: Blocks

#### Compiling a template

A template is written as a tagged template literal whose interpolations are placeholders only; the value at each position is ignored, and the Nth placeholder is hole number N. Compilation happens once per call site, cached by the identity of the literal's string array.

1. Walk the literal's string parts in order, tracking whether the scan is inside a tag (an unclosed `<`).
2. For a placeholder inside a tag, the text before it must end with an attribute name, an equals sign and optionally an opening quote. Replace that ending with a marker attribute named `bk` plus the hole number, whose value is the original attribute name (percent-encoded). If a quote was opened, drop the matching closing quote from the next string part. A placeholder inside a tag that is not an attribute value is an error.
3. For a placeholder outside a tag, emit a comment whose text is `bk` plus the hole number.
4. Parse the result once into an inert template element. It must contain exactly one root element and nothing else, or compilation fails.
5. Walk the parsed tree depth first, recording for each hole its path, the list of child indexes from the root. Before indexing an element's children, remove every text child that is only whitespace and contains a line break, unless the element is a `pre` or a `textarea`. Such text is indentation from a formatter (herdsman's `bun fmt` formats tagged `html` literals as HTML), not content; whitespace within a line, such as the spaces around a hole in "a, hole, b", is kept. Then:
   - A marker attribute is removed. The hole's kind comes from the original name: `class` is a class hole; a name starting with `on` or with a dot is a property hole (the dot is stripped, an `on` name is lowercased); anything else is an attribute hole.
   - A marker comment is replaced by an empty text node at the same position, and the hole is a text hole pointing at that text node.
6. Every hole must have been found, or compilation fails.
7. Generate a locator: one straight-line function that walks from a root through `firstChild` and `nextSibling` steps to each hole's node, sharing common path prefixes so each node is reached once, and writes the nodes into the first half of a given array. If generating functions at runtime is not allowed (a strict content security policy), fall back to walking each path in a loop.
8. Run the locator once on the parsed root itself, the **prototype**, and keep its hole nodes. Record what the prototype holds in each hole: the empty string for text and class holes, absent (null) for attribute holes.

#### Block state

A block is the template, its root element, and a single state array of length twice the hole count: the first half holds the hole nodes (empty until located), the second half the last value set in each hole. One array per instance, instead of separate node and value arrays, matters when thousands of rows are created at once.

#### Mounting

1. For each hole that is not a property hole, compare the new value with what the prototype holds. If it differs, write it into the prototype's hole node (by the update rules below) and record it.
2. Deep-clone the prototype. The clone is born with the row's text, classes and attributes.
3. Create the block with the clone as root, a fresh state array, and the new values recorded in its second half. Do not locate the clone's hole nodes.
4. Properties do not survive cloning. If any property hole has a non-null value, locate the clone's holes now and set those properties on the clone. Property values are never written to the prototype.

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

1. Build a source array with one slot per new window position, all set to "new item".
2. Index the new window's keys: for a window of more than 16 keys, use a hash map from key to new position; for smaller windows, a linear scan is cheaper.
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

#### Longest increasing subsequence

Use patience sorting with predecessor links, O(n log n):

1. Keep an array of "tails": the tail at index k is the position whose value ends the best increasing run of length k + 1 found so far. Keep a predecessor for each position.
2. For each position in order, skip "new item" slots. If its value is larger than the value at the last tail, link its predecessor to that tail and append it. Otherwise binary-search the first tail whose value is not smaller, link the predecessor to the tail before it (or none), and replace that tail.
3. Rebuild the run by following predecessor links back from the last tail. Return positions in ascending order.

Values are strictly increasing; old indexes are distinct, so ties never occur.

### Layer 3: Morph

Morph makes a live parent's children match a list of fresh nodes. It is what `patch()` in `dom.ts` runs.

#### Keys for arbitrary nodes

Within one child list, each child gets a key from its node name plus:

- its `data-key` attribute when it has one, giving a key unique to that value; or
- for children without one, an ordinal: the first unkeyed `DIV` is "DIV number 0", the next "DIV number 1", and so on, separately per node name (text nodes and comments included).

A repeated `data-key` value falls back to the ordinal form so keys stay unique. The node name is part of every key, so a key never matches a node of a different tag; a changed tag is a removal plus an insertion.

The same rule computes the live children's keys and the fresh children's keys, so unkeyed children match by tag and order, and keyed children match by identity wherever they move.

#### Morphing a child list

1. Seed a keyed list over the live parent with the live children as its current entries and their computed keys. The list owns the parent.
2. Reconcile it to the fresh children. Its create step adopts the fresh node itself, moving it into the live tree with no copy. Its update step morphs the matched live node toward its fresh partner.

#### Morphing one node

1. If the live node is the fresh node itself (a persistent element a view reuses), stop.
2. For text and comments, copy the fresh data if it differs, and stop.
3. For elements:
   1. Sync attributes: set each fresh attribute whose live value differs; remove each live attribute the fresh element lacks.
   2. Call the keep hook with the live and fresh elements (dom.ts uses it to move event handlers).
   3. Unless the live element has `data-morph-skip`, snapshot the fresh element's children and morph the live element's children to them.
   4. Sync form state, after children so a select already has its options. Inputs, text areas and selects copy `value` unless the live element has focus, which keeps a user's in-progress typing. Inputs also copy `checked`. Options copy `selected`.

`data-morph-skip` is for elements whose children another component manages, such as a terminal.

### Integration with dom.ts

`patch(el, children...)` takes the same arguments as `mount()`. It flattens them the same way (nested arrays, strings and numbers to text nodes, null, undefined and false dropped) and morphs `el` toward the result.

Event handlers need one change in `h()`, because a listener added with `addEventListener` cannot be read back or compared. Instead:

1. Each element has a handler map from event type to the current handler, held in a weak map so it never keeps an element alive.
2. The first handler for a type adds a single real listener that looks up the element's current handler for that type and calls it with the element as `this`.
3. When morph keeps a live element, the keep hook gives it the fresh element's handlers: every type the live element already listens for is set to the fresh handler or to nothing, and any new type gets its own listener.

The result is that a kept button runs the closure the view just built, never a stale one from an earlier render.

Views opt in one call site at a time by replacing `mount(container, view)` with `patch(container, view)`, and by giving list items a `key` in their dataset when items can reorder, appear or disappear.

#### Rule: key a view root whenever its container also holds other elements

Unkeyed children match by tag and order, so a container that sometimes holds a persistent component's element (one a class owns and updates itself) and sometimes a rebuilt view would have the component's element morphed into the view when both are the same tag. Giving the rebuilt view's root a key that the persistent element never carries makes them never match: switching between them is a replacement, never a morph.

#### Call sites converted in herdsman web

1. The route tab body for Team, Checklist, Run state and Activity. Its root is keyed by tab ("tab-" plus the tab id), because the same container also holds the persistent Docs, Artifacts, Messages and Changes elements.
2. The session view in the main area. Its root is keyed "session", because the same container also holds the persistent route shell.
3. Team cards are keyed by pane id, so a card that changes lanes, or moves within one, keeps its node.

Every other `mount()` call still rebuilds, unchanged.

---

## Part 3: Verification

### Correctness properties the tests check

Tests run on a small linked-list fake DOM with counters for inserts, moves, removes, clones and class writes, so they can assert exact DOM work without a browser.

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

The same behaviors were also checked in real Chrome 156 (not the fake DOM): a focused input kept its node, focus, typed value and caret position through a `patch()` that reordered siblings; the kept button ran the newest handler; keyed rows kept their nodes; an unfocused input re-synced from the store; an indented template inside a table compiled correctly; and a blank class hole produced no class attribute.

### Profiling

Bun's CPU profiler with markdown output profiles the benchmark app over the fake DOM, where DOM work is pointer updates, so the profile shows the library's own JavaScript. After the changes above, the library's own functions account for a small share of samples; the rest is fake-DOM node construction.

### Benchmark method (replica of the official one)

For each test and iteration, in a fresh browser context:

1. Load the page, run the test's warmup clicks and wait for each to finish.
2. Apply CPU throttling where the official suite does: 4 times for partial update, select, swap and clear; 2 times for remove.
3. Start a trace (timeline categories), force a garbage collection, perform the measured click, wait for it to finish, stop the trace.
4. Duration runs from the start of the click's event dispatch to the end of the first compositor commit that follows the last script, timer, animation frame or layout event after the click, all on the click's process.

Frameworks are interleaved within each iteration, rotating which goes first, so machine drift lands on all of them. Each test's median is divided by the best median for that test, and the score is the geometric mean of those factors weighted by the official per-test weights.

---

### Per-phase findings (2026-10-26)

The runner can also report, per test, how the click-to-commit window splits into script, style, layout, pre-paint, paint, commit and garbage collection (medians).

1. Replace 1,000 rows: with phases measured, herdsman-dom-diff took 73.9 ms against vanilla's 79.1 ms, with every phase equal or lower. The earlier 1.28 times gap was machine noise.
2. Create rows: the remaining cost was JavaScript allocation per row. Before the changes, script was 10.6 ms against vanilla's 6.2 ms on 1,000 rows; on 10,000 rows garbage collection was 96 ms for us, 74 ms for vanilla and 44 ms for vanillajs-lite, which never touches a cloned row's inner nodes. After merging block state into one array, reusing the argument array, dropping a per-row expando, and baking values into the prototype with lazy hole lookup, create 1,000 rows scored 1.01 times the best of vanilla, vanillajs-lite and herdsman-dom-diff (vanillajs-lite 1.07), and create 10,000 rows 1.02 times. Garbage collection on 10,000 rows was still 88 ms, so per-row objects (the block and its state array) remain the next target.

## Open questions

1. Can rows that are never updated avoid the per-row block object entirely (for example, created on first update), to bring garbage collection down to vanillajs-lite's level?
2. Morph's per-node cost on large herdsman views has not been measured yet.
3. Whether `patch()` should become the default for all of herdsman's re-rendered views, or stay per call site.
4. The other top entries of the official table that need an npm install and their own build step (thane, doohtml, sonnet, deleight, alien-signals, mikado) have not been run in the replica; installing and running their build tools needs the owner's approval.

## Change log

- 2026-10-26: First version. Blocks, keyed lists with prefix and suffix trimming, the two-item swap path and the LIS general case; blank-write skipping on mount; `detach`; morph and `patch()` with handler transfer for herdsman web; test, profiling and benchmark method; first replica results.
- 2026-10-26: Templates ignore formatter indentation (herdsman's `bun fmt` reformats tagged templates). Morph call sites in herdsman web keyed by tab and view. Block state merged into one array; mount bakes values into the prototype and looks up holes lazily. Real-Chrome verification of `patch()`. Per-phase benchmark findings.
