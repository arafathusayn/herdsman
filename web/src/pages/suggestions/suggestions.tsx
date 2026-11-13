import { For, Show } from "@reactre/core";

import { Markdown } from "../../markdown.tsx";
import { explain, services } from "../../sdk/index.ts";
import { createRequest } from "../../state/request.ts";
import { valueState } from "../../state/value.ts";
import { Async, Failure, fieldValue, load, PageFrame } from "../load.tsx";

// The catalog's kinds; the daemon refuses any other.
const kinds = ["tip", "mcp", "skill", "plugin", "mod", "repo"];

function tagsOf(text: string): string[] {
  return text
    .split(",")
    .map((tag) => tag.trim())
    .filter(Boolean);
}

export function SuggestionsPage() {
  const [text, setText] = valueState("");
  const state = load((signal) => services().suggest.search({ text: text().trim() }, { signal }));
  const [openId, setOpenId] = valueState("");
  const bodyRequest = createRequest(async (id: string, signal) => services().suggest.get({ id }, { signal }));
  const body = () => bodyRequest.data()?.entry?.body ?? "";
  const bodyError = bodyRequest.error;
  const [adding, setAdding] = valueState(false);
  const [title, setTitle] = valueState("");
  const [kind, setKind] = valueState(kinds[0] as string);
  const [tags, setTags] = valueState("");
  const [draft, setDraft] = valueState("");
  const mutation = createRequest(async (input: { kind: string; title: string; tags: string[]; body: string }, signal) =>
    services().suggest.add(input, { signal }),
  );
  const problem = () => (mutation.error() === undefined ? "" : explain(mutation.error()));
  const open = async (id: string) => {
    setOpenId(id);
    bodyRequest.clear();
    await bodyRequest.run(id);
  };
  const add = async (event: Event) => {
    event.preventDefault();
    if (mutation.loading() || !title().trim() || !draft().trim() || tagsOf(tags()).length === 0) return;
    const input = { kind: kind(), title: title().trim(), tags: tagsOf(tags()), body: draft() };
    const outcome = await mutation.run(input);
    if (outcome.status !== "done") return;
    if (title().trim() === input.title && draft() === input.body) {
      setTitle("");
      setTags("");
      setDraft("");
      setAdding(false);
    }
    await state.reload();
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
            <select onChange={(event) => setKind(fieldValue(event))}>
              <For each={kinds} key={(name) => name}>
                {(name) => (
                  <option value={name} selected={name === kind()}>
                    {name}
                  </option>
                )}
              </For>
            </select>
          </label>
          <label class="field">
            Tags, separated by commas
            <input required value={tags()} onInput={(event) => setTags(fieldValue(event))} />
          </label>
          <label class="field page-wide">
            Notes, in Markdown
            <textarea required rows={5} defaultValue={draft()} onInput={(event) => setDraft(fieldValue(event))} />
          </label>
          <button
            class="btn primary"
            type="submit"
            disabled={!title().trim() || tagsOf(tags()).length === 0 || !draft().trim() || mutation.loading()}
          >
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
