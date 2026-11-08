import { For, Show, signal } from "@reactre/core";

import { Markdown } from "../../markdown.tsx";
import { explain, services } from "../../sdk/index.ts";
import { Async, Failure, fieldValue, load, PageFrame } from "../load.tsx";

export function SuggestionsPage() {
  const [text, setText] = signal("");
  const state = load(() => services().suggest.search({ text: text().trim() }));
  const [openId, setOpenId] = signal("");
  const [body, setBody] = signal("");
  const [bodyError, setBodyError] = signal<unknown>(undefined);
  const [adding, setAdding] = signal(false);
  const [title, setTitle] = signal("");
  const [kind, setKind] = signal("tool");
  const [draft, setDraft] = signal("");
  const [problem, setProblem] = signal("");

  const open = async (id: string) => {
    setOpenId(id);
    setBody("");
    setBodyError(undefined);
    try {
      const reply = await services().suggest.get({ id });
      if (openId() === id) setBody(reply.entry?.body ?? "");
    } catch (error) {
      if (openId() === id) setBodyError(() => error);
    }
  };

  const add = async (event: Event) => {
    event.preventDefault();
    setProblem("");
    try {
      await services().suggest.add({ kind: kind().trim(), title: title().trim(), body: draft() });
      setTitle("");
      setDraft("");
      setAdding(false);
      await state.reload();
    } catch (error) {
      setProblem(explain(error));
    }
  };

  return (
    <PageFrame title="Suggestions" lead="Tools and skills that fit this machine's projects.">
      <form
        class="page-form"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          void state.reload();
        }}
      >
        <label class="field">
          Search
          <input type="search" value={text()} onInput={(event) => setText(fieldValue(event))} />
        </label>
        <button class="btn" type="submit">
          Search
        </button>
        <button
          class="btn ghost"
          type="button"
          aria-expanded={adding() ? "true" : "false"}
          onClick={() => setAdding((on) => !on)}
        >
          Add an entry
        </button>
      </form>
      <Show when={adding()}>
        <form class="page-form" aria-label="Add a suggestion" onSubmit={add}>
          <label class="field">
            Title
            <input required value={title()} onInput={(event) => setTitle(fieldValue(event))} />
          </label>
          <label class="field">
            Kind
            <input required value={kind()} onInput={(event) => setKind(fieldValue(event))} />
          </label>
          <label class="field page-wide">
            Notes, in Markdown
            <textarea rows={5} defaultValue={draft()} onInput={(event) => setDraft(fieldValue(event))} />
          </label>
          <button class="btn primary" type="submit" disabled={!title().trim() || !kind().trim()}>
            Save entry
          </button>
          <Show when={problem()}>
            <p class="page-error" role="alert">
              {problem()}
            </p>
          </Show>
        </form>
      </Show>
      <Async state={state} what="The suggestion catalog">
        {(data) => (
          <Show when={(data.items ?? []).length > 0} fallback={<p class="page-empty">Nothing matches.</p>}>
            <ul class="page-list">
              <For each={data.items ?? []} key={(item) => item.entry?.id ?? ""}>
                {(item) => (
                  <li class="page-row page-stack">
                    <button class="page-link" type="button" onClick={() => void open(item.entry?.id ?? "")}>
                      <strong>{item.entry?.title}</strong>
                      <span class="badge">{item.entry?.kind}</span>
                    </button>
                    <span class="page-sub">{(item.reasons ?? []).map((reason) => reason.code).join(", ")}</span>
                    <Show when={openId() === item.entry?.id}>
                      <div class="page-detail">
                        <Show when={bodyError() !== undefined}>
                          <Failure error={bodyError()} what="This entry" />
                        </Show>
                        <Markdown source={body()} />
                        <p class="page-sub">
                          {"Installing runs from a terminal, after approval: "}
                          <code>{`herdsman suggest apply ${item.entry?.id ?? ""}`}</code>
                        </p>
                      </div>
                    </Show>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        )}
      </Async>
    </PageFrame>
  );
}
