# Migrating herdsman web lists to `each()`

How to move the web app's lists to keyed rendering with `each()`, why it matters, and how the type checker will eventually enforce it. Status as of 2026-10-26: phase 1 is done (the API exists and the lists rendered through `patch()` use it); phase 2 (making plain arrays a type error) is ready to start.

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

| Data            | Key used                                                                           |
| --------------- | ---------------------------------------------------------------------------------- |
| Agent cards     | `m.pane_id`                                                                        |
| Checklist lines | `i.id`, or `text:` plus the text when the line has no id                           |
| Run-state lines | time and text joined with `\|`                                                     |
| Activity events | `at`, `kind`, `pane_id`, `status` and `text` joined with `\|` (events carry no id) |

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

## Phase 2: make plain arrays a type error

Change one line in `dom.ts`:

```ts
// from
type Child = Node | string | number | false | null | undefined | Keyed | Unkeyed | Child[];
// to
type Child = Node | string | number | false | null | undefined | Keyed | Unkeyed | readonly [Child, ...Child[]];
```

Literal tuples still type-check (TypeScript infers a tuple for an array literal where a tuple is expected), but any `.map()` result, which is a plain array, no longer does. Run `bun check` and fix each error with `each()` or `unkeyed()`.

A trial flip on 2026-10-26 reported 67 sites:

| File                  | Sites | Likely fix                                                                     |
| --------------------- | ----: | ------------------------------------------------------------------------------ |
| `markdown.ts`         |    11 | `unkeyed()` for rendered blocks and inline runs                                |
| `views/diff.ts`       |    10 | `each()` by file path for file rows; `unkeyed()` for diff lines                |
| `views/route-form.ts` |     7 | `unkeyed()` for fixed `<option>` lists; `each()` by value for data-driven ones |
| `main.ts`             |     7 | `each()` for route items (by route id) and nav sections                        |
| `views/files.ts`      |     5 | `each()` by path                                                               |
| `views/artifacts.ts`  |     5 | `each()` by artifact path                                                      |
| `views/messages.ts`   |     4 | `each()` by message id                                                         |
| `views/agent.ts`      |     4 | case by case                                                                   |
| `dom.test.ts`         |     4 | the old `patch()` tests pass arrays on purpose: wrap in `unkeyed()`            |
| `ui.ts`               |     3 | case by case                                                                   |
| `views/team.ts`       |     2 | the lane columns: `each()` by lane status or role                              |
| `views/appearance.ts` |     2 | fixed swatches: `unkeyed()`                                                    |
| `palette.ts`          |     2 | `each()` by command id                                                         |
| `views/hosts.ts`      |     1 | `each()` by host URL                                                           |

Suggested order: files that `patch()` renders first (they gain identity), then the rest one file per change, so each change is small and its `bun check` output short. Flip the type last, when `bun check` is clean with it.

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
