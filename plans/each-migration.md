# Migrating herdsman web lists to `each()`

How to move the web app's lists to keyed rendering with `each()`, why it matters, and how the type checker will eventually enforce it. Status as of 2026-10-27: phase 1 is done (the API exists and the lists rendered through `patch()` use it), and so is phase 2 (plain arrays are a type error; every list in the app is an `each()` or `unkeyed()` list).

## Why

`patch()` keeps the DOM nodes that still match the new render. How well it can match depends on the children:

- **Keyed children** match by identity. A row that moves keeps its node (and its focus, scroll and selection), and the move costs exactly one DOM operation.
- **Unkeyed children** match by content, then by order. Since herdsman-blocks 0.0.5 this is cheap for most edits (a performance fuzzer found at most one DOM write per changed row), but a row whose content changes has no identity: if it also moves, it is matched by position, and state inside it (an open `<details>`, a focused input) can end up on the wrong row.

`each()` makes identity explicit, and it is the only way to get a `Keyed` list, so the type checker can tell keyed lists from accidental arrays.

## The API

```ts
import { each, unkeyed } from "./dom.ts";

each(
  items,
  (item) => item.id,
  (item) => h("li", null, item.label),
); // Keyed
unkeyed(blocks); // Unkeyed: matched by content and order, on purpose
```

- `each(items, key, render)` renders every item, stamps `data-key` on the element `render` returns, and returns a `Keyed` list. `render` must return an `Element` (the key needs somewhere to live).
- The key function receives the item only, never its position. Writing `(item, i) => i` does not compile: an index is not a key, because it changes when rows are inserted, removed or moved.
- `unkeyed(nodes)` marks a list whose items have no identity, such as the blocks of a rendered markdown file or the options of a fixed `<select>`. It is an explicit choice, not a default.
- Literal tuples (`[h("h3", ...), list]`, `cond ? [a, b] : null`) stay as they are: a fixed set of children is not a list.

### Choosing a key

A key must be **stable** (the same item gets the same key on every render) and **unique** among its siblings.

| Data                        | Key used                                                                           |
| --------------------------- | ---------------------------------------------------------------------------------- |
| Agent cards                 | `m.pane_id`                                                                        |
| Checklist lines             | `i.id`, or `text:` plus the text when the line has no id                           |
| Run-state lines             | time and text joined with `\|`                                                     |
| Activity events             | `at`, `kind`, `pane_id`, `status` and `text` joined with `\|` (events carry no id) |
| Team board lanes            | `lane.id` (the role or the status)                                                 |
| Session tab groups          | the tab id                                                                         |
| Routes, workspaces, panes   | `r.id`, `workspace_id`, `pane_id`                                                  |
| Files, changed files, pages | the path (pages: root and path, `artifactPath`); folders by name                   |
| Messages, hosts, commands   | message id, host id, command id                                                    |

When the data has no id, join the fields that identify an item. Never use the array index or a random value.

### Duplicate keys

Duplicates are not fatal: patch() falls back to order for them, so the page still renders correctly, but rows that share a key may be rewritten instead of moved. In development `each()` warns:

```
each(): duplicate key "w1:p2"; duplicates fall back to order and may rewrite rows
```

The warning runs only when the `HERDSMAN_DEV` global is `true` (tests set it). The production build defines `HERDSMAN_DEV` as `false` (`--define HERDSMAN_DEV=false` in the Makefile's `BUN_BUILD`), so the bundler folds the condition and drops the check: the production bundle contains no trace of it (verified: 0 occurrences of the warning, 431 bytes smaller). Keep the constant inline in the `if`; behind a helper function the bundler cannot remove it.

## Phase 1: done

These lists are rendered through `patch()` and now use `each()`:

1. `views/team.ts`: the cards in each Team board lane (the old `dataset.key` on the card is gone; `each()` stamps it).
2. `main.ts`, session view: the cards in each tab group.
3. `main.ts`, `checklistView`: the checklist lines.
4. `main.ts`, `logView`: the run-state lines.
5. `main.ts`, `activityView`: the activity events.

Verified against the real app: `herdsman web` served against a fake herdr with three agents, driven in Chrome 156. When the server changed one agent from idle to working, every card kept its DOM node, the changed card updated, a click on that kept card opened its panel (the latest handler ran), and the page logged no errors.

### Before and after

```ts
// Before: a plain array; rows matched by content and order.
h(
  "div",
  { class: "cards" },
  ms.map((m) => card(m, app)),
);

// After: keyed by the pane.
h(
  "div",
  { class: "cards" },
  each(
    ms,
    (m) => m.pane_id,
    (m) => card(m, app),
  ),
);
```

## Phase 2: done (2026-10-27)

One line in `dom.ts` changed:

```ts
// from
type Child = Node | string | number | false | null | undefined | Keyed | Unkeyed | Child[];
// to
type Child = Node | string | number | false | null | undefined | Keyed | Unkeyed | readonly [Child, ...Child[]];
```

Literal tuples still type-check (TypeScript infers a tuple for an array literal where a tuple is expected), but any `.map()` result, which is a plain array, no longer does. A fresh trial flip found the same 67 sites in the same 14 files as the 2026-10-26 trial. Each was fixed before the flip landed, so `bun check` was clean with it:

| File                  | Sites | Fix                                                                                                                                                                                                                                                 |
| --------------------- | ----: | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `markdown.ts`         |    11 | `inline()` returns an `Unkeyed` run, which covers 8 sites; table header cells, rows and cells are `unkeyed()`                                                                                                                                       |
| `views/diff.ts`       |    10 | `each()` for sources (by id) and file rows (by path); `unkeyed()` for diff lines, the layout switch and agent name chips; `highlight()` (in `fuzzy.ts`) returns `Unkeyed`; the split view's cell pair is a tuple                                    |
| `views/route-form.ts` |     7 | `each()` for `<select>` options (by value) and folder suggestions (by path); `unkeyed()` for the fixed channel checklist, checkpointer errors, skill warnings and the start steps; the harness fields are a tuple (its nulls are dropped as before) |
| `main.ts`             |     7 | `each()` for routes (by id), workspace groups (by workspace id), route tabs (by tab id), session tab groups (by tab id), idle workspaces and their panes; the activity body is a tuple                                                              |
| `views/files.ts`      |     5 | `each()` for folders (by name) and files (by path); `highlight()` as above                                                                                                                                                                          |
| `views/artifacts.ts`  |     5 | `each()` for root folders (by root) and pages (by `artifactPath`); `highlight()` as above                                                                                                                                                           |
| `views/messages.ts`   |     4 | `each()` for the thread (by message id), holds and inboxes (by name); `unkeyed()` for health chips, flags, alerts and a message's fields                                                                                                            |
| `views/agent.ts`      |     4 | `unkeyed()` for the fixed key buttons, arrow keys and view switch; the meta rows are tuples                                                                                                                                                         |
| `dom.test.ts`         |     4 | the old `patch()` tests wrap their arrays in `unkeyed()`; a new type test checks that a plain array is rejected                                                                                                                                     |
| `ui.ts`               |     3 | `unkeyed()` for the theme switch, a form's fields and its suggestions                                                                                                                                                                               |
| `views/team.ts`       |     2 | `each()` for the lanes (by lane id); `unkeyed()` for the column switch                                                                                                                                                                              |
| `views/appearance.ts` |     2 | `unkeyed()` for the fixed swatches and choices                                                                                                                                                                                                      |
| `palette.ts`          |     2 | `each()` by command id; `highlight()` as above                                                                                                                                                                                                      |
| `views/hosts.ts`      |     1 | `each()` by host id                                                                                                                                                                                                                                 |

In all, 25 lists became `each()`, 26 became `unkeyed()` (two of them helpers, `inline()` and `highlight()`, that every caller shares), and 4 fixed sets became tuples. Where a row needs its position (the palette's and the search results' active row), the items are mapped to carry their index before `each()`, which keys them by identity all the same.

Behavior is unchanged apart from the `data-key` attribute on newly keyed elements; most of these lists are still rebuilt by `mount()`, where keys only document identity. Two newly keyed lists are rendered through `patch()` and move: the Team board's lanes and the session view's tab groups. `views/team.test.ts` renders the board in the fake DOM, adds a lane ahead of the others and a card to a lane, and checks that the lanes and the kept card are the same nodes (with every lane given the same key, it fails).

Keys chosen with some doubt:

1. The agent name chips on a Changes source are `unkeyed()`: two agents can share a display name, so the name is not an identity.
2. The start steps of a new route are `unkeyed()`: a fixed procedure, rendered once.
3. Folder suggestions are keyed by path; `knownFolders()` removes duplicates, so the keys are unique.

## Checklist for each change

1. Pick the key from the data's identity; never an index.
2. Make sure `render` returns one element per item.
3. `bun fmt`, `bun check`, `bun test` (or `make web-check`, which also runs the stricter library check).
4. For a list that moves or holds focus, add a test with the fake DOM in `herdsman-blocks.testdom.ts`: render, reorder, and assert the nodes are the same objects.
5. `make web` and confirm the bundle still has no development checks (`grep -c "duplicate key" internal/web/dist/*.js` prints 0).

## Questions

**Does `each()` cost anything at runtime?** One `setAttribute` per item, which is the information `patch()` needs, plus a type assertion that compiles to nothing. The duplicate check is removed from production builds.

**Can a keyed list share a parent with other children?** Yes. Keyed and unkeyed siblings are matched together; keep the list's items keyed and the rest by content.

**What about `mount()`?** It still rebuilds from scratch. Use `patch()` for views that re-render on store updates; keep `mount()` for one-off content and for persistent components that own their element.
