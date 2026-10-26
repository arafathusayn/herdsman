# Migrating herdsman web to views

How every part of the web app moves from `h()` + `patch()` (dom.ts) to herdsman-blocks' declarative view layer (`web/src/herdsman-blocks-view.ts`, spec in `plans/herdsman-blocks.md`, Layer 4 and Types). These are the conventions every migration agent follows. Status as of 2026-10-27: stage 1 (the foundation and the temporary bridge) is done except `fuzzy.ts` and `markdown.ts`, which wait for an API decision (see "What views cannot express yet").

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
| `child<C>()`         | element content             | C, a subset of: text, nothing, a Node, a view description, a list        |

Calling a view with its value tuple returns a description (`ViewNodeOf<typeof Row>`); `render(container, description)` mounts it the first time and patches it after. `svg` is `html` for SVG fragments.

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
4. No casts. If a type does not fit, narrow the marker or the data; a cast that is truly unavoidable gets a comment saying why. `mountElement` (below) narrows an element with a checked type predicate, never `as`.

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
3. Never read or assign `el.onclick` and never `addEventListener` on an element a view renders: the runtime may soon delegate events at each `render()` root, and the handler contract is the only promise.
4. Listeners that come and go with an interaction (a drag's `pointermove` and `pointerup`) are the exception: they are added in the handler that starts it and removed when it ends (`resize.ts`).

### 6. Text safety

Values are never parsed as HTML: a text hole sets a text node's data, an attribute hole calls setAttribute, a class hole sets className. The template markup is code, written in the source as a literal; never pass a computed string to `html` or `svg` as markup, and never build markup from data. Data that looks like markup (`<b>`, `&amp;`) shows as typed. Attribute values that are URLs are still URLs: check schemes as `markdown.ts` does (`^https?://`) before putting data in an `href`.

### 7. SVG

1. A whole icon or graphic whose root is `<svg>` is an `html` template: the HTML parser puts `<svg>` and everything in it in the SVG namespace.
2. A fragment that goes inside SVG content (a `<path>`, a `<g>`, a `<circle>`) is an `svg` template, so its root is an SVG element even though it is parsed on its own.
3. `class` on an SVG element is a static attribute or an `attr()` hole, never `cls()` (className is read-only on SVG elements; a class marker there fails when the template compiles).
4. A child hole inside SVG content is typed with `SvgChild` (or a narrower type) so HTML views cannot go in it.

After (`dom.ts`):

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

### 8. Components that own an element

Some elements are made once and then changed by their owner rather than re-rendered: dialogs, toasts, resize handles, the terminal, the agent panel. Two tools:

1. **Making the element**: `mountElement(tag, View, vals)` (dom.ts) creates a view's root once and returns it typed by tag (`HTMLDialogElement` for "dialog"). Text, class, attribute, property and event holes are written at creation; handlers are set on the element directly. A view with a child hole is refused (it would stay empty). Content that changes is rendered into the element with `render(el, ContentView(...))`: the element is the render container.

   Before (`ui.ts` confirmDialog): one `h("dialog", ..., h("form", ...))` tree plus `dlg.addEventListener("close", ...)`.

   After:

   ```ts
   const Dialog = html`<dialog class="dialog" onclose=${on<"close", HTMLDialogElement>()}></dialog>`;
   const dlg = mountElement("dialog", Dialog, [
     function () {
       resolve(this.returnValue === "ok");
       this.remove();
     },
   ]);
   render(dlg, ConfirmForm([title, body, danger ? "btn danger" : "btn primary", confirmLabel]));
   ```

2. **Placing the element in a view**: a component object that owns an element is put into a parent view through a `child<HTMLElement>()` hole (or a narrower element type) by passing the element itself. The slot inserts that node and keeps it; it never morphs it into anything. Pass the same node every render; passing a new node replaces the old one.

### 9. Forms and focus

1. Form state goes through property holes: `.value=${prop<HTMLInputElement, "value">()}`, `.checked=${prop<HTMLInputElement, "checked">()}`.
2. A property hole is written only when its value differs from the value the last render passed, not from what the element holds. While the user types, the state the view reads can lag or stay the same, and the field is not overwritten. To reset a field the user edited back to a value the view already passed, change the state through another value (or write the property in the handler that resets it).
3. Focus, caret and selection survive because nodes survive: a patched view keeps its elements, and a keyed list moves rows with their nodes. They are lost where an element is replaced: a different template in a hole, a list item whose render switches views, a node passed fresh through a child hole, and every view that crosses the bridge (below). Keep a focused field in a stable template.

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

1. Tests run on herdsman-blocks' linked-list fake DOM (`herdsman-blocks.testdom.ts`, installed by `installFakeDocument()`), which parses templates, clones, records namespaces and counts DOM writes (`ops`, `resetOps()`). `testdom.ts` extends it with what the app's forms need (form properties, `classList`, readable `dataset`, bubbling `dispatch()` with `submitter`) and keeps `all`, `find` and `byName`.
2. Render into a scratch container: `const box = document.createElement("div"); render(box, view(...));` and inspect `box.firstChild`.
3. When migrating, keep the old builder in the test (or build the old markup with `h()` while it exists) and compare a dump of both: tag, namespace, sorted attributes, text. `dom.test.ts` (icons) and `ui.test.ts` (pill, theme switch, dialogs) show the pattern. Where the fake DOM and the browser differ (the browser reflects `tabIndex` as `tabindex` and `spellcheck = false` as `spellcheck="false"`; the fake records them as given), write the reference with the browser's result and say so in a comment.
4. Test behavior through events and data, and assert node identity (`toBe`) to prove a patch kept nodes. To fire a handler, use `testdom.ts`'s `dispatch()`, which runs listeners and on* handlers; if event delegation lands, that helper is the one place to update.
5. Use `ops` to prove that an unchanged render writes nothing.

### What views cannot express yet

1. **Mixed inline content of data-driven length**: text runs between elements, such as `highlight()`'s text and `<mark>` runs, a markdown paragraph's text, code, bold and links, and highlighted code's text and `<span class="hl-*">` runs. Every view and every list item has exactly one element root, so text items cannot be listed. Keeping the DOM identical needs an API addition (proposed: list items that render to text as well as views, or a positional `runs([...])` child kind for content without identity, the view-layer counterpart of `unkeyed`). Until it is decided, `fuzzy.ts` and `markdown.ts` stay on `h()` and reach views through the bridge's other direction: a view takes their output as a Node in a child hole.
2. **DocumentFragments in child holes**: a fragment's nodes move out when it is inserted, so the slot cannot remove them later. Never pass a fragment to a child hole.

## The temporary bridge (stage 1 to stage 3)

Files migrate in parallel, so for a while views and `h()` trees nest in both directions. dom.ts carries a bridge, marked TEMPORARY, that stage 3 deletes:

1. `h()`, `mount()` and `patch()` accept a view description or a view list wherever they accept a child. The bridge renders it once into a scratch element and moves its element roots out (one-shot; the `h()` parent rebuilds anyway).
2. `each()`'s render may return a view description instead of an element.
3. Every root the bridge moves out is stamped with a `data-key` no other render uses: `view:<n>`, or `<item key>|view:<n>` for an `each()` row. `patch()`'s morph therefore never matches an old materialization to a new one: it replaces it. Morphing would keep the old element and, with it, the old render's event handlers.

Costs, all temporary: a view crossing the bridge is rebuilt on every `patch()` of its parent (no node identity, focus or caret inside it survives), its root carries the stamp in the DOM, and its handlers are bound to the scratch render. If the runtime starts delegating events at each `render()` root, materialized subtrees (moved out of their scratch container) lose their handlers, and the bridge must render in place instead; check this when that change lands.

The other direction needs no bridge: an `h()` element is a Node, and a view takes it through a `child<HTMLElement>()` hole. A fresh element per render replaces the old one each time, so do this only for parts that are about to migrate.

## Migration order

1. **Stage 1, foundation (done except as noted)**: dom.ts `icon()`, `logoMark()` and `mountElement()`; ui.ts `pill()`, toasts, `confirmDialog()`, `formDialog()`, `themeSwitch()`; resize.ts handles; the bridge; the test DOM. Waiting on the API decision: fuzzy.ts `highlight()`, markdown.ts `renderMarkdown()` and `codeBlock()`.
2. **Stage 2, views, leaves before containers** (files can go in parallel; each agent owns its files):
   1. `views/team.ts` (cards and board), `views/pair.ts`, `views/appearance.ts`, `views/new-route.ts`.
   2. `palette.ts`, `views/messages.ts`, `views/files.ts`, `views/artifacts.ts`, `views/hosts.ts`.
   3. `views/diff.ts`, `views/route-form.ts` (forms: convention 9), `views/agent.ts` (owns elements: convention 8).
   4. `main.ts` last: the shell, the sidebar, the route tabs, and the `render()` roots that replace its `patch()` calls. `docs.ts` with it.
   Each file's views return descriptions; a container still on `h()` takes them through the bridge until it migrates.
3. **Stage 3, removal**: when nothing calls `h()`, delete:
   - dom.ts: `h()`, `mount()`, `patch()`, `each()`, `unkeyed()`, the `Keyed`, `Unkeyed` and `Child` types, the handler map (`handlers`, `on()`, `keepHandlers()`), the bridge section (`Scratch`, `materialize()`, `materializeView()`, `isElement()`, `isList()`), and the `KEY_ATTR` and `morph` imports. dom.ts keeps `icons`, `icon()`, `logoMark()` and `mountElement()`.
   - dom.test.ts: the `patch`, `each` and bridge tests.
   - ui.test.ts and the other comparison tests: the `h()` reference builders (keep the expected markup as literals).
   - plans/each-migration.md: mark superseded by this plan; plans/herdsman-blocks.md "Integration with dom.ts": rewrite for views.
   - Check that Layer 3 (`morph`) still has a user in the app; if not, say so in plans/herdsman-blocks.md (the library keeps it).

## Helper signatures changed in stage 1

| Helper                                  | Before                    | After                                                                 |
| --------------------------------------- | ------------------------- | --------------------------------------------------------------------- |
| `icon(paths, label?)`                   | `string[]` → `SVGSVGElement` | `readonly string[]` → `IconNode`                                    |
| `logoMark()`                            | `SVGSVGElement`           | `LogoNode`                                                            |
| `mountElement(tag, view, vals)`         | (new)                     | `HTMLElementTagNameMap[K]`                                            |
| `pill(status)`                          | `HTMLElement`             | `PillNode`                                                            |
| `themeSwitch(current, onChange)`        | `HTMLElement`             | `ThemeSwitchNode`                                                     |
| `each(items, key, render)`              | render returns `Element`  | render returns `Element \| ViewNode` (bridge)                         |
| `h()`, `mount()`, `patch()` children    | Node, text, lists         | also `ViewNode` and `ListNode` (bridge)                               |
| `toast`, `confirmDialog`, `formDialog`, `dismissOnBackdrop`, `panelResizer`, `navResizer` | unchanged | unchanged |
| testdom.ts `FakeElement`                | its own fake              | extends herdsman-blocks.testdom's `FakeElement`; `FakeEvent.target` is never null |

A caller that used a returned element as an element (`el.append(icon(...))`) passes the description to a child hole instead, or, until it migrates, to `mount()`/`h()` (docs.ts's copy button does `mount(btn, icon(icons.copy), label)`).
