# Migrating herdsman web to views

How every part of the web app moved from `h()` + `patch()` (dom.ts) to herdsman-blocks' declarative view layer (`web/src/herdsman-blocks-view.ts`, spec in `plans/herdsman-blocks.md`, Layer 4 and Types), and the conventions the views follow. Status as of 2026-10-27: **the migration is complete.** Stage 3 removed `h()`, `patch()`, `each()`, the bridge and `mountElement`; dom.ts is now `icons.ts` (the icon path data and the icon and logo views). See [Stage 3: what replaced what](#stage-3-what-replaced-what).

## Why

`patch()` rebuilds a fresh DOM tree on every render and morphs the live tree toward it: every render allocates every node, and matching is a heuristic (keys, then content, then order). A view is a compiled template plus a value tuple. `render()` writes only the holes whose values changed, a keyed list moves only what moved, and the type checker sees every hole's value type. No fresh tree, no morph and no hand-written update code.

## The API in one screen

```ts
import { attr, child, cls, html, list, type ListNode, on, prop, render, svg, text, type ViewNodeOf } from "./herdsman-blocks-view.ts";

const Row = html`<li class=${cls()} title=${attr<string | null>()}>${text()}</li>`;
const Rows = html`<ul onclick=${on<"click">()}>${child<ListNode<ViewNodeOf<typeof Row>, string>>()}</ul>`;

render(container, Rows([() => pick(), list(items, (it) => it.id, (it) => Row([it.cls, it.tip ?? null, it.label]))]));
```

| Marker               | Sits                        | Takes                                                                    |
| -------------------- | --------------------------- | ------------------------------------------------------------------------ |
| `text<V>()`          | element content             | string by default; number, boolean, null (empty) when V says so         |
| `cls<V>()`           | `class=` on an HTML element | the whole className; null clears it                                      |
| `attr<V>()`          | any other attribute         | string by default; null, undefined and false remove it, true sets it "" |
| `prop<E, P>()`       | `.name=`                    | exactly `E[P]`; both type arguments required                             |
| `on<K, E>()`         | `onname=`                   | `(this: E, ev: EventOf<E, K>) => void`, or null; the only marker for on* |
| `child<C>()`         | element content             | C, a subset of: text, nothing, a Node, a description, a list, runs       |

Calling a view with its value tuple returns a description (`ViewNodeOf<typeof Row>`); `render(container, description)` mounts it the first time and patches it after. `svg` is `html` for SVG fragments. A child hole takes three kinds of sequence: `list(items, key, render)` (keyed, for items with identity), `runs([...])` (positional text and descriptions with no wrapper, for inline content) and nothing else: a plain array is never a child.

`render` delegates the handlers of bubbling events to its container (one listener per event type), so a view's elements carry no `on*` property. `mount(description)` makes a view's root on its own, as its own delegating render root, for a component that places the root itself; `update(root, description)` patches it (convention 8). `renderDirect` (every handler on its own element) was for the bridge and has no user in the app any more.

## Conventions

### 1. One template constant per element shape, one view function per component

Templates are module-level constants named in PascalCase, next to the function that fills them. A component is a function from data to a description; its return type is the exported description type, so other modules can type their child holes with it.

Before (`ui.ts`):

```ts
export function pill(status: Member["status"]): HTMLElement {
  return h("span", { class: `pill s-${status}` }, h("span", { class: "dot", "aria-hidden": "true" }), statusLabel[status]);
}
```

After:

```ts
const Pill = html`<span class=${cls()}><span class="dot" aria-hidden="true"></span>${text()}</span>`;

/** What `pill` returns. */
export type PillNode = ViewNodeOf<typeof Pill>;

export function pill(status: Member["status"]): PillNode {
  return Pill([`pill s-${status}`, statusLabel[status]]);
}
```

Rules:

1. The value tuple is in hole order, top to bottom, left to right. Keep the tuple short by giving one element one class hole and computing the string in the function; put long tuples one value per line (bun fmt does this) with the markup in the same order.
2. Every hole has a typed marker. Narrow it where the data is narrow: `attr<"true" | "false">()` for an ARIA state, `child<PillNode>()` for a hole that only ever holds a pill.
3. Templates compile when their module loads, so a document must exist then. The browser always has one; tests get one from `web/src/test-setup.ts`, which `web/bunfig.toml` preloads. Never build a template inside a function per call: `html` caches by call site, so a template literal inside a function is fine, but a constant is clearer and its type can be named.
4. No casts. If a type does not fit, narrow the marker or the data; a cast that is truly unavoidable gets a comment saying why. `element()` (convention 8) narrows the mounted root with a checked type predicate, never `as`.

### 2. Conditionals: a child hole and null

A part that is sometimes absent is a child hole whose type admits null, filled with a description or null.

Before (`ui.ts` formDialog):

```ts
listId ? h("datalist", { id: listId }, unkeyed((f.list ?? []).map((v) => h("option", { value: v })))) : null,
f.hint ? h("small", null, f.hint) : null,
```

After:

```ts
const Hint = html`<small>${text()}</small>`;
// in FieldRow:
${child<ViewNodeOf<typeof Suggestions> | null>()}${child<ViewNodeOf<typeof Hint> | null>()}
// in fieldRow():
listId === null ? null : Suggestions([listId, list(...)]),
f.hint ? Hint([f.hint]) : null,
```

The same view in the same hole patches in place; another view, or null, replaces the content. When two shapes alternate in one place (a card that is a link or a button), give each its own template; do not make one template with every part optional.

### 3. Optional attributes: attr() with null

`attr<string | null>()` with null removes the attribute; `attr<boolean>()` gives boolean attributes (`required`, `disabled`, `hidden`): true sets them empty, false removes them.

```ts
<input placeholder=${attr<string | null>()} required=${attr<boolean>()} pattern=${attr<string | null>()} />
// values: f.placeholder ?? null, f.required ?? false, f.pattern ?? null
```

Use `prop<E, P>()` instead only for state that lives in a property and not in an attribute: `.value`, `.checked`, `.selectedIndex`, `.indeterminate`.

### 4. Keyed lists: list() with identity keys

A list whose items come from data is `list(items, key, render)` in a child hole typed `child<ListNode<ViewNodeOf<typeof Row>, K>>()`.

1. The key comes from the item: an id, a path, or the fields that identify it joined with `|` (the table in `plans/each-migration.md` lists the keys already chosen for every list in the app; reuse them). Never the index (the key function takes one parameter, so `(x, i) => i` does not compile) and never a random value.
2. Keys must be unique among siblings. Where the data may repeat (suggestion lists), dedupe first: `list([...new Set(f.list)], (v) => v, ...)`.
3. The item type comes from `items` only (`NoInfer`), so the key and render functions cannot widen it.
4. A list that is a fixed set of different parts is not a list: the card's top, task, subtitle and foot are separate holes in one template. A constant array that drives identical markup (the three theme buttons, the two board-switch buttons) is a `list` keyed by its id.
5. A list item whose render returns another view is replaced where it stands; one that returns the same view is patched.

Before (`ui.ts` themeSwitch):

```ts
unkeyed(themes.map((t) => h("button", { role: "radio", "aria-checked": String(t.id === current), class: t.id === current ? "active" : "", title: `${t.label} theme`, onclick: () => onChange(t.id) }, icon(t.glyph), h("span", null, t.label))))
```

After:

```ts
const ThemeOption = html`<button
  role="radio"
  aria-checked=${attr<"true" | "false">()}
  class=${cls()}
  title=${attr()}
  onclick=${on<"click">()}
>
  ${child<IconNode>()}<span>${text()}</span>
</button>`;
const ThemeSwitch = html`<div class="theme-switch" role="radiogroup" aria-label="Theme">
  ${child<ListNode<ViewNodeOf<typeof ThemeOption>, Theme>>()}
</div>`;

export function themeSwitch(current: Theme, onChange: (t: Theme) => void): ThemeSwitchNode {
  return ThemeSwitch([
    list(
      themes,
      (t) => t.id,
      (t) =>
        ThemeOption([
          t.id === current ? "true" : "false",
          t.id === current ? "active" : "",
          `${t.label} theme`,
          () => onChange(t.id),
          icon(t.glyph),
          t.label,
        ]),
    ),
  ]);
}
```

### 5. Event handlers: on() holes, a new closure per render is fine

Every handler is an `on<K, E>()` hole; K is the event name, E the element type (HTMLElement by default). The handler is called with the element as `this` and the event type the DOM declares for K: `on<"click">()` gets a `PointerEvent`, `on<"keydown">()` a `KeyboardEvent`, `on<"close", HTMLDialogElement>()` an `Event` with `this: HTMLDialogElement`.

1. Pass a fresh closure every render: a handler hole records the new function and writes nothing to the DOM.
2. The markup's `onname` is not checked against K (TypeScript sees the markup only as strings): keep them identical by eye.
3. Handlers of bubbling events (click, input, change, submit, keydown, pointer and mouse events, and the rest of the delegated set in plans/herdsman-blocks.md) are delegated: the `render()` container listens once per event type and calls the handlers on the path from the target, innermost first, with the element as `this` and `currentTarget`; `stopPropagation()` stops the outer ones. Non-bubbling events (focus, blur, mouseenter, scroll, close) get a handler on the element itself. So:
   - Never read or assign `el.onclick`: it is not set.
   - An element moved out of its render container gets no delegated events. Do not move a view's nodes; render where they live, or mount a root of its own (convention 8) for an element its owner places.
   - A delegated handler runs when the event reaches the container: after native listeners on elements between, before listeners above it. A native listener inside the view that stops propagation hides the event from the view's handlers.
4. Never `addEventListener` on an element a view renders, except for listeners that come and go with an interaction (a drag's `pointermove` and `pointerup`), added in the handler that starts it and removed when it ends (`resize.ts`).
5. A root made with `mount()` (or `element()`, `ElementView`, convention 8) is its own render root: it delegates its own and its descendants' handlers wherever it is placed, and inside another render root each handler still runs once.
6. Do not reuse or mutate a value tuple between renders: a description rendered again with the same array is skipped as unchanged.

### 6. Text safety

Values are never parsed as HTML: a text hole sets a text node's data, an attribute hole calls setAttribute, a class hole sets className. The template markup is code, written in the source as a literal; never pass a computed string to `html` or `svg` as markup, and never build markup from data. Data that looks like markup (`<b>`, `&amp;`) shows as typed. Attribute values that are URLs are still URLs: check schemes as `markdown.ts` does (`^https?://`) before putting data in an `href`.

### 7. SVG

1. A whole icon or graphic whose root is `<svg>` is an `html` template: the HTML parser puts `<svg>` and everything in it in the SVG namespace.
2. A fragment that goes inside SVG content (a `<path>`, a `<g>`, a `<circle>`) is an `svg` template, so its root is an SVG element even though it is parsed on its own.
3. `class` on an SVG element is a static attribute or an `attr()` hole, never `cls()` (className is read-only on SVG elements; a class marker there fails when the template compiles).
4. A child hole inside SVG content is typed with `SvgChild` (or a narrower type) so HTML views cannot go in it.

After (`icons.ts`, then dom.ts):

```ts
const IconPath = svg`<path d=${attr()} />`;
const Icon = html`<svg viewBox="0 0 24 24" class="icon" fill="none" stroke="currentColor" ...
  aria-label=${attr<string | null>()} aria-hidden=${attr<"true" | null>()}>
  ${child<ListNode<ViewNodeOf<typeof IconPath>, string>>()}
</svg>`;

export function icon(paths: readonly string[], label?: string): IconNode {
  return Icon([label || null, label ? null : "true", list(paths, (d) => d, (d) => IconPath([d]))]);
}
```

`icons` stays path data (`string[]` per icon), so commands and menus keep passing glyphs around as data; `icon(glyph)` makes the view. Path data is unique within an icon (a test checks), so it keys the paths, and swapping one icon for another in place keeps the paths they share.

### 7a. Inline content: runs()

Text and inline elements side by side, of data-driven length, with no wrapper element: `runs(items)` with items that are strings, numbers or descriptions, in a hole typed `child<RunsNode<ViewNodeOf<typeof Mark>>>()` (or a union of inline views). Runs are positional, like `unkeyed()`: text stays the same text node with its data updated, a description patches the view at its position (or replaces it for another view). Use them for content with no identity; anything with identity is a `list`. Markdown blocks are runs of block views too: they have no identity, and a re-render patches them by position.

Before (`fuzzy.ts`):

```ts
export function highlight(text: string, hits: number[]): Unkeyed {
  // ...
  out.push(inHit ? h("mark", null, run) : run);
  return unkeyed(out);
}
```

After:

```ts
const Mark = html`<mark>${text()}</mark>`;
export type HighlightNode = RunsNode<ViewNodeOf<typeof Mark>>;

export function highlight(value: string, hits: readonly number[]): HighlightNode {
  if (!hits.length) return runs<ViewNodeOf<typeof Mark>>([value]);
  // ...
  out.push(inHit ? Mark([run]) : run);
  return runs(out);
}
// in a view: html`<span class="name">${child<HighlightNode>()}</span>`
```

Give `runs` its type argument where the items are all text (`runs<V>([value])`), or it infers runs of any view. Markdown (`markdown.ts`) shows the larger case: `InlineNode` is runs of text and `<code>`, `<strong>` and link views; `CodeNode` is a string or runs of text and token spans; a document is runs of heading, list, table, quote, paragraph and code views. Templates whose root is `<tr>`, `<th>` or `<td>` compile fine (a template element parses table parts on their own).

### 8. Components that own an element

Some elements are made once and then changed by their owner rather than re-rendered: dialogs, toasts, resize handles, the terminal, the agent panel. Two tools:

1. **Making the element**: `element(tag, View(vals))` (`views/element-view.ts`) mounts a view's root with herdsman-blocks' `mount()` and returns it typed by tag (`HTMLDialogElement` for "dialog"). Any view works, child holes included. The root is its own render root: it delegates its handlers (and its descendants'), so it works wherever its owner puts it. Patch it with `update(el, View(newVals))`, a description of the same view (another view throws: it would need another element). `ElementView(tag, description)` wraps the pair and holds updates to the first view's exact type. Content that changes inside an owned element can also be rendered into it with `render(el, ContentView(...))`.

   Before (stage 1, `mountElement`, now removed): `mountElement("dialog", Dialog, [onClose])` wrote the holes at creation and set handlers on the element.

   After:

   ```ts
   const Dialog = html`<dialog class="dialog" onclose=${on<"close", HTMLDialogElement>()}></dialog>`;
   const dlg = element("dialog", Dialog([
     function () {
       resolve(this.returnValue === "ok");
       this.remove();
     },
   ]));
   render(dlg, ConfirmForm([title, body, danger ? "btn danger" : "btn primary", confirmLabel]));
   ```

   Components whose whole markup is one view (the agent panel, Messages, Files, Artifacts, Changes, the Hosts dialog, the route form) mount it once (`this.el = element("aside", this.describe())`) and patch it on every change (`update(this.el, this.describe())`). Before stage 3 they rendered into a scratch holder with `renderDirect` and moved the root out.

2. **Placing the element in a view**: a component object that owns an element is put into a parent view through a `child<HTMLElement>()` hole (or a narrower element type) by passing the element itself. The slot inserts that node and keeps it; it never morphs it into anything. Pass the same node every render; passing a new node replaces the old one.

### 9. Forms and focus

1. Form state goes through property holes: `.value=${prop<HTMLInputElement, "value">()}`, `.checked=${prop<HTMLInputElement, "checked">()}`.
2. A property hole is written only when its value differs from the value the last render passed, not from what the element holds. While the user types, the state the view reads can lag or stay the same, and the field is not overwritten. To reset a field the user edited back to a value the view already passed, change the state through another value (or write the property in the handler that resets it).
3. Focus, caret and selection survive because nodes survive: a patched view keeps its elements, and a keyed list moves rows with their nodes. They are lost where an element is replaced: a different template in a hole, a list item whose render switches views, or a node passed fresh through a child hole. Keep a focused field in a stable template.

### 10. Class composition

`cls()` sets the whole className, so one element has either a static `class="..."` or one class hole, never both. Compute the string in the view function, static part included:

```ts
const sel = app.view.pane === m.pane_id ? " selected" : "";
Card([`card r-${m.role} s-${m.status}${sel}`, ...]);
```

For several optional classes, join the truthy ones: `["btn", danger ? "danger" : "primary", busy && "busy"].filter(Boolean).join(" ")`. An empty class is `""` (or null) and leaves no class attribute.

### 11. Formatting hazard

bun fmt formats the markup of `html` and `svg` literals as HTML. It may put two adjacent holes on one line separated by a space, which adds a text node with that space: write adjacent holes with nothing between them (`${child<A>()}${child<B>()}`), and compare a migrated view's DOM with the old one in a test (convention 12). Line breaks with indentation between tags are formatting and are dropped by the compiler (not inside `<pre>` or `<textarea>`).

### 12. Testing with the fake DOM

1. Tests run on herdsman-blocks' linked-list fake DOM (`herdsman-blocks.testdom.ts`, installed by `installFakeDocument()`), which parses templates, clones, records namespaces, dispatches bubbling events (`dispatchEvent`) and counts DOM writes (`ops`, `resetOps()`). `testdom.ts` extends it with what the app's forms need (form properties, `classList`, readable `dataset`) and keeps `all`, `find` and `byName`.
2. Render into a scratch container: `const box = document.createElement("div"); render(box, view(...));` and inspect `box.firstChild`.
3. Markup tests compare `dump()` (testdom.ts) of a view's DOM with literal markup: tags, SVG namespace, sorted attributes, text, with empty text (slot anchors) left out. Write the expected markup as a string, or across lines with testdom.ts's `markup` tag, where a line break and the indentation after it are layout (so a line must not start or end inside text that begins or ends with a space). Put in `${}` holes only what the test cannot fix: an icon's markup (`iconMarkup(icons.x)`, as icons.test.ts checks icons), a time or an age that follows the clock or the time zone, keys that differ on a Mac. Where the data is large or generated (every theme, 3000 diff rows), build the expected string from the data with plain string code and say why in a comment. `ui.test.ts` (pill, theme switch, dialogs), `fuzzy.test.ts` (highlight) and `markdown.test.ts` (every block kind, then each block alone) show the pattern. The literals were captured from the pre-views builders, so they are the markup the app drew before views.
4. Test behavior through events and data, and assert node identity (`toBe`) to prove a patch kept nodes. Fire events only with `testdom.ts`'s `dispatch(el, type, { submitter })` (the submitter may be any element, a view's included; `all`, `find` and `byName` see view-rendered elements too): it dispatches a bubbling event from the element, so the render container's delegated listener sees it as in a browser. Never call `el.onclick` in a test: delegated handlers are not there.
5. Use `ops` to prove that an unchanged render writes nothing.

### What views cannot express

1. **DocumentFragments in child holes**: a fragment's nodes move out when it is inserted, so the slot cannot remove them later. Never pass a fragment to a child hole; inline sequences are `runs()`.

## The temporary bridge (stage 1 to stage 3, removed)

While files migrated in parallel, views and `h()` trees nested in both directions. dom.ts carried a bridge that rendered a view given as an `h()`, `mount()`, `patch()` or `each()` child into a scratch element with `renderDirect` and moved its nodes out, stamping each moved root with a `data-key` (`view:<n>`) so `patch()` replaced it instead of morphing it. Stage 3 deleted it with `h()`: nothing in the app builds DOM outside views now, so no node is rendered in one place and moved to another, and no `data-key` stamps remain.

## Migration order

1. **Stage 1, foundation (done)**: dom.ts `icon()`, `logoMark()` and `mountElement()`; ui.ts `pill()`, toasts, `confirmDialog()`, `formDialog()`, `themeSwitch()`; resize.ts handles; fuzzy.ts `highlight()`; markdown.ts `renderMarkdown()` and `codeBlock()`; the bridge; the test DOM.
2. **Stage 2, views, leaves before containers (done)** (files went in parallel; each agent owned its files):
   1. `views/team.ts` (cards and board), `views/pair.ts`, `views/appearance.ts`, `views/new-route.ts`.
   2. `palette.ts`, `views/messages.ts`, `views/files.ts`, `views/artifacts.ts`, `views/hosts.ts`.
   3. `views/diff.ts`, `views/route-form.ts` (forms: convention 9), `views/agent.ts` (owns elements: convention 8).
   4. `main.ts` last: the shell, the sidebar, the route tabs, and the `render()` roots that replace its `patch()` calls. `docs.ts` with it.
   Each file's views return descriptions; a container still on `h()` takes them through the bridge until it migrates.
3. **Stage 3, removal (done 2026-10-27)**: every component that owns its root mounts it with `mount()` and patches it with `update()`; the Team board and the session view's card grid are views in child holes (the card grid a keyed `list()` of `card()` views); then the old machinery went, as listed below.

## Stage 3: what replaced what

| Removed                                                    | Replaced by                                                                                     |
| ---------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| `h(tag, props, ...children)`                               | a template constant and its view function (conventions 1 to 3)                                  |
| `mount(el, ...children)` (dom.ts)                          | `render(container, description)`                                                                |
| `patch(el, ...children)` and the morph it ran              | `render()` / `update()`: views write only changed holes; nothing in the app imports `morph`     |
| `each(items, key, render)` and the `Keyed` type            | `list(items, key, render)` in a child hole typed `ListNode<…, K>`                               |
| `unkeyed(nodes)` and the `Unkeyed` type                    | `runs([...])` for inline content; a fixed set of parts is separate holes                        |
| the `Child` type, the handler map (`on`, `keepHandlers`)   | typed `child<C>()` holes and `on<K, E>()` holes, delegated by the render root                   |
| the bridge (`materialize`, `Scratch`, `data-key` stamps)   | nothing: no view crosses into an `h()` tree                                                     |
| `mountElement(tag, View, vals)`                            | `element(tag, View(vals))` or `ElementView` (views/element-view.ts), on herdsman-blocks `mount()` |
| scratch holder + `renderDirect` + `isHTML`/`isDiv` checks  | `element(tag, description)` once, then `update(el, description)`                                |
| `teamView()` (temporary)                                   | `teamBoard()` in the route shell's child hole                                                   |
| `addEventListener("compositionend")` on the terminal field | an `on<"compositionend", HTMLTextAreaElement>()` hole                                           |
| `HERDSMAN_DEV` (only `each()` read it)                     | nothing; the build no longer defines it                                                         |
| dom.ts                                                     | `icons.ts`: `icons`, `icon()`, `logoMark()`, `IconNode`, `LogoNode`                             |
| dom.test.ts's `patch`, `each` and bridge tests             | deleted with what they tested; the icon tests are `icons.test.ts`                               |

Herdsman-blocks still exports `renderDirect` and `morph` (Layer 3) for other users; the app uses neither. Browser checks of the whole app and its forms live in `web/e2e` (`make web-e2e`).

## Helper signatures changed in stage 1

| Helper                                  | Before                    | After                                                                 |
| --------------------------------------- | ------------------------- | --------------------------------------------------------------------- |
| `icon(paths, label?)`                   | `string[]` → `SVGSVGElement` | `readonly string[]` → `IconNode`                                    |
| `logoMark()`                            | `SVGSVGElement`           | `LogoNode`                                                            |
| `mountElement(tag, view, vals)`         | (new)                     | `HTMLElementTagNameMap[K]`                                            |
| `pill(status)`                          | `HTMLElement`             | `PillNode`                                                            |
| `themeSwitch(current, onChange)`        | `HTMLElement`             | `ThemeSwitchNode`                                                     |
| `highlight(value, hits)`                | `(string, number[])` → `Unkeyed` | `(string, readonly number[])` → `HighlightNode` (runs of text and `<mark>`) |
| `renderMarkdown(src)`                   | `HTMLElement`             | `MarkdownNode` (also exported: `InlineNode`)                          |
| `codeBlock(lines, lang)`                | `Node \| string`          | `CodeNode` = `string \| RunsNode<token span>`                          |
| `each(items, key, render)`              | render returns `Element`  | render returns `Element \| ViewNode` (bridge)                         |
| `h()`, `mount()`, `patch()` children    | Node, text, lists         | also `ViewNode`, `ListNode` and `RunsNode` (bridge)                   |
| `toast`, `confirmDialog`, `formDialog`, `dismissOnBackdrop`, `panelResizer`, `navResizer` | unchanged | unchanged |
| testdom.ts                              | its own fake              | `FakeElement` extends herdsman-blocks.testdom's; `FakeEvent` is a class extending its bubbling event (with `submitter`, `preventDefault`); `dispatch(el, type, extras)` and `dump(node)` are exported |

A caller that used a returned element as an element (`el.append(icon(...))`) passes the description to a child hole instead.
