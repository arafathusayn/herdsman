import { For, memo, onCleanup, Show } from "@reactre/core";
import { map } from "@reactre/core/features/lists";

import { Icon } from "../components/icons.tsx";
import { mountRoot } from "../components/mount-root.ts";
import { icons } from "../icons.ts";
import {
  accountOptions,
  checklist,
  type ChecklistItem,
  checkpointerAvailable,
  checkpointerOn,
  effortOptions,
  effortsFor,
  inboxKey,
  kindOf,
  knownFolders,
  liveCatalog,
  OTHER,
  pickSummary,
  preview,
  previousPicks,
  projectLabel,
  RECOMMENDED_SETUP,
  roleKind,
  SETUP_QUESTIONS,
  skillWarnings,
  stepNote,
} from "../new-route.ts";
import type { Catalog, RouteStep, State } from "../types.ts";
import { CopyButton, Field, type InputOpts, RequestBox, SelectBox, TextInput } from "./route-form-controls.tsx";
import {
  createRouteFormState,
  type Deps,
  type FormController,
  type FormOptions,
  type TextKey,
} from "./route-form-state.ts";
export { type Deps, INBOX_DEBOUNCE_MS, INBOX_POLL_MS } from "./route-form-state.ts";

let uid = 0;
const checkpointerFields = ["checkpointer", "minutes", "limit"] as const;
const roles = [
  { role: "orchestrator", label: "Orchestrator" },
  { role: "adjutant", label: "Adjutant (Recommended)" },
] as const;

function DraftInput(props: { controller: FormController; id: string; name: TextKey; options?: InputOpts }) {
  const snapshot = props.controller.state;
  return (
    <TextInput
      id={`${props.id}-${props.name}`}
      name={props.name}
      value={snapshot().draft[props.name]}
      busy={snapshot().busy}
      change={(value) => props.controller.change({ [props.name]: value })}
      options={props.options}
    />
  );
}

function HarnessFields(props: {
  controller: FormController;
  id: string;
  which: "main" | "orch";
  nameField: "name" | "orchName";
  who: string;
}) {
  const controller = props.controller,
    snapshot = controller.state;
  const form = memo(() => snapshot().draft),
    catalog = memo(() => snapshot().catalog);
  const harness = memo(() => form()[props.which]);
  const kind = memo(() => roleKind(form(), catalog(), props.which));
  const kinds = memo(() => {
    const live = liveCatalog(catalog()).kinds;
    const kinds: [string, string][] = live.map((kind) => [kind.kind, kind.label]);
    if (kind() && !live.some((item) => item.kind === kind()!.kind)) kinds.unshift([kind()!.kind, kind()!.label]);
    return kinds;
  });
  const models = memo(() => {
    const selected = kind();
    if (!selected) return [];
    const listed = [
      ...selected.models,
      ...(selected.saved ? (kindOf(liveCatalog(catalog()), selected.kind)?.models ?? []) : []),
    ];
    const values: [string, string][] = [...new Map(listed.map((item) => [item.id, item])).values()].map((item) => [
      item.id,
      item.label === item.id ? item.id : `${item.label} (${item.id})`,
    ]);
    if (selected.other_model) values.push([OTHER, "Other model id…"]);
    return values;
  });
  const efforts = memo(() => effortsFor(kind(), harness().model));
  return (
    <>
      <Field
        id={`${props.id}-${props.which}`}
        name={props.which}
        label={`${props.who} harness`}
        error={snapshot().errors[props.which]}
        hint="The coding agent program this role runs in."
      >
        <SelectBox
          name={`${props.which}-kind`}
          id={`${props.id}-${props.which}`}
          value={harness().kind}
          options={kinds()}
          busy={snapshot().busy}
          change={(kind) => controller.harness(props.which, { kind })}
        />
      </Field>
      <Field
        id={`${props.id}-${props.which}-model`}
        name={`${props.which}-model`}
        label="Model"
        error={snapshot().errors[`${props.which}-model`]}
        hint="The models this harness lists now, newest first; its docs say which suits coding and review."
      >
        <div class="nr-row">
          <Show
            when={kind() && !kind()?.free_model}
            fallback={
              <TextInput
                name={`${props.which}-model`}
                id={`${props.id}-${props.which}-model`}
                value={harness().model}
                busy={snapshot().busy}
                change={(model) => controller.harness(props.which, { model })}
                options={{ placeholder: "model id (optional)" }}
              />
            }
          >
            <SelectBox
              name={`${props.which}-model`}
              id={`${props.id}-${props.which}-model`}
              value={harness().model}
              options={models()}
              busy={snapshot().busy}
              change={(model) => controller.harness(props.which, { model })}
            />
          </Show>
          <Show when={kind()?.offline}>
            <span
              class="nr-chip"
              title={`${kind()?.unavailable ?? "The harness could not be asked"}: it offers no models and starts with its own default.`}
            >
              models unavailable
            </span>
          </Show>
          <Show
            when={kind()?.docs_url && /^https?:\/\//.test(kind()?.docs_url ?? "")}
            fallback={
              <Show when={kind()?.docs_command}>
                <small class="muted" data-docs={props.which}>{`Model details: ${kind()?.docs_command}`}</small>
              </Show>
            }
          >
            <a
              href={kind()?.docs_url ?? ""}
              target="_blank"
              rel="noopener"
              title={`${kind()?.label}'s own page of its models`}
            >
              Model docs
            </a>
          </Show>
        </div>
      </Field>
      <Show when={harness().model === OTHER}>
        <Field
          id={`${props.id}-${props.which}-other`}
          name={`${props.which}-other`}
          label="Model id"
          error={snapshot().errors[`${props.which}-other`]}
        >
          <TextInput
            name={`${props.which}-other`}
            id={`${props.id}-${props.which}-other`}
            value={harness().other}
            busy={snapshot().busy}
            change={(other) => controller.harness(props.which, { other })}
            options={{ placeholder: "a model from the harness's list" }}
          />
        </Field>
      </Show>
      <Show when={efforts().length > 0}>
        <Field
          id={`${props.id}-${props.which}-effort`}
          name={`${props.which}-effort`}
          label="Effort"
          error={snapshot().errors[`${props.which}-effort`]}
          hint="How long the model thinks before it answers: more is slower and costs more."
        >
          <SelectBox
            name={`${props.which}-effort`}
            id={`${props.id}-${props.which}-effort`}
            value={harness().effort}
            options={effortOptions(kind(), harness().model)}
            busy={snapshot().busy}
            change={(effort) => controller.harness(props.which, { effort })}
          />
        </Field>
      </Show>
      <Field
        id={`${props.id}-${props.nameField}`}
        name={props.nameField}
        label={`${props.who} name`}
        error={snapshot().errors[props.nameField]}
        hint="The agent's name in Herdr, used in its tab and its messages."
      >
        <DraftInput controller={controller} id={props.id} name={props.nameField} />
      </Field>
    </>
  );
}

function GitHubField(props: { controller: FormController; id: string }) {
  const controller = props.controller,
    snapshot = controller.state;
  const accounts = memo(() => (snapshot().github ? accountOptions(snapshot().github!) : []));
  return (
    <Show when={snapshot().githubPending || snapshot().github}>
      <Field
        id={`${props.id}-ghAccount`}
        name="ghAccount"
        label="GitHub account"
        wide
        error={snapshot().errors.ghAccount}
        hint={
          snapshot().githubPending
            ? null
            : accounts().length
              ? `The account the route pushes and opens pull requests with; it can read ${snapshot().github?.repo}.`
              : "Log in with gh auth login, then reopen this form."
        }
      >
        <Show when={!snapshot().githubPending} fallback={<span>Checking…</span>}>
          <Show
            when={accounts().length > 0}
            fallback={<span>{snapshot().github?.error ?? `No gh account can read ${snapshot().github?.repo}.`}</span>}
          >
            <SelectBox
              name="ghAccount"
              id={`${props.id}-ghAccount`}
              value={snapshot().draft.ghAccount}
              options={[["", "Confirm an account…"], ...accounts()]}
              busy={snapshot().busy}
              change={(ghAccount) => controller.change({ ghAccount })}
            />
          </Show>
        </Show>
      </Field>
    </Show>
  );
}

function PreviousChoices(props: { controller: FormController }) {
  const controller = props.controller,
    snapshot = controller.state;
  const picks = memo(() => previousPicks(snapshot().draft, snapshot().catalog));
  return (
    <Show when={picks().length > 0}>
      <div class="nr-previous">
        <span>{`Previous choice: ${picks().map(pickSummary).join("; ")}`}</span>
        <Show when={snapshot().catalog.from_picks && controller.options.needLists}>
          <button type="button" class="chip-btn" name="choose-lists" onClick={() => controller.options.needLists?.()}>
            Choose again
          </button>
        </Show>
        <button type="button" class="chip-btn" onClick={() => controller.previous()}>
          Use previous choice
        </button>
      </div>
    </Show>
  );
}

function RecommendedSummary(props: { controller: FormController }) {
  const snapshot = props.controller.state;
  const on = memo(() => checkpointerOn(snapshot().draft, snapshot().catalog));
  return (
    <dl class="nr-summary" data-field="recommended-summary">
      <For each={RECOMMENDED_SETUP} key={([key]) => key}>
        {([key, value]) => (
          <>
            <dt>{key}</dt>
            <dd data-item={key}>
              {key === "Checkpointer" && !on()
                ? `Off: ${snapshot().catalog.checkpointer_why || "the checkpointer is not available here"}`
                : value}
            </dd>
          </>
        )}
      </For>
    </dl>
  );
}

function CustomSetup(props: { controller: FormController; id: string }) {
  const controller = props.controller,
    snapshot = controller.state;
  return (
    <div>
      <div class="nr-role" role="radiogroup" aria-label="Role">
        <For each={roles} key={(role) => role.role}>
          {(role) => (
            <button
              type="button"
              role="radio"
              name={`role-${role.role}`}
              aria-checked={snapshot().draft.role === role.role ? "true" : "false"}
              class={snapshot().draft.role === role.role ? "active" : ""}
              onClick={() => controller.role(role.role)}
            >
              {role.label}
            </button>
          )}
        </For>
      </div>
      <div class="nr-grid nr-custom" data-field="custom-setup">
        <For each={SETUP_QUESTIONS} key={([item]) => item}>
          {([item, recommended]) => (
            <Field
              id={`${props.id}-custom-${item}`}
              name={`custom-${item}`}
              label={item}
              hint="Empty: the route asks you."
              wide
              error={snapshot().errors[`custom-${item}`]}
            >
              <TextInput
                id={`${props.id}-custom-${item}`}
                name={`custom-${item}`}
                value={snapshot().draft.custom[item] ?? ""}
                busy={snapshot().busy}
                options={{ placeholder: recommended, maxlength: 400 }}
                change={(value) => controller.change({ custom: { ...snapshot().draft.custom, [item]: value } })}
              />
            </Field>
          )}
        </For>
      </div>
    </div>
  );
}

function Checkpointer(props: { controller: FormController; id: string }) {
  const controller = props.controller,
    snapshot = controller.state;
  const available = memo(() => checkpointerAvailable(snapshot().draft, snapshot().catalog));
  const on = memo(() => checkpointerOn(snapshot().draft, snapshot().catalog));
  return (
    <div
      class={`field wide nr-ck${checkpointerFields.some((field) => snapshot().errors[field]) ? " invalid" : ""}`}
      data-field="checkpointer"
    >
      <label class="nr-toggle">
        <input
          type="checkbox"
          name="checkpointer"
          disabled={!available().ok}
          checked={on()}
          onChange={(event) => {
            const input = event.currentTarget as HTMLInputElement;
            if (snapshot().busy) input.checked = on();
            else controller.change({ checkpointer: input.checked });
          }}
        />
        <span>Checkpointer (Recommended)</span>
        <small>{available().ok ? "keeps the orchestrator's memory and compacts its context" : available().why}</small>
      </label>
      <Show when={on()}>
        <div class="nr-row">
          <label class="nr-inline">
            every
            <DraftInput
              controller={controller}
              id={props.id}
              name="minutes"
              options={{ cls: `nr-num${snapshot().errors.minutes ? " invalid" : ""}`, inputmode: "numeric" }}
            />
            min
          </label>
          <label class="nr-inline">
            compact at
            <DraftInput
              controller={controller}
              id={props.id}
              name="limit"
              options={{ cls: `nr-num${snapshot().errors.limit ? " invalid" : ""}` }}
            />
            tokens
          </label>
        </div>
      </Show>
      <For each={checkpointerFields} key={(name) => name}>
        {(name) => (
          <Show when={snapshot().errors[name]}>
            <small class="error" role="alert" data-error={name}>
              {snapshot().errors[name]}
            </small>
          </Show>
        )}
      </For>
    </div>
  );
}

function SetupItem(props: { item: ChecklistItem; index: number; controller: FormController }) {
  return (
    <li class={props.item.done ? "done" : ""} data-item={String(props.index)}>
      <span class="nr-check" aria-hidden="true">
        <Show when={props.item.done}>
          <Icon paths={icons.check} />
        </Show>
      </span>
      <span class="nr-item">
        <span>{props.item.title}</span>
        <Show when={props.item.code}>
          <Show when={props.index === 2} fallback={<code>{props.item.code}</code>}>
            <span class="nr-attach">
              <code>{props.item.code}</code>
              <CopyButton
                label="Copy the command"
                copy={() => props.controller.deps.copy(props.item.code ?? "", "attach command")}
              />
            </span>
          </Show>
        </Show>
        <Show when={props.item.note}>
          <small>{props.item.note}</small>
        </Show>
      </span>
      <span class={`nr-who${props.item.who === "you do this" ? " you" : ""}`}>
        {props.item.done ? "done" : props.item.who}
      </span>
    </li>
  );
}

function ChannelChecklist(props: { controller: FormController }) {
  const controller = props.controller,
    snapshot = controller.state;
  const applicable = memo(
    () => controller.active().role === "adjutant" && (!snapshot().result || !!snapshot().result?.adjutant_pane),
  );
  const inbox = memo(() =>
    snapshot().inbox?.key === inboxKey(controller.active()) ? (snapshot().inbox?.state ?? null) : null,
  );
  const items = map(
    () =>
      checklist(controller.active(), snapshot().catalog, inbox(), snapshot().result?.attach_command).map(
        (item, index) => ({ item, index }),
      ),
    (row) => row,
    { keyed: (row) => row.item.title },
  );
  return (
    <section class="nr-checklist" aria-label="Channel setup">
      <Show when={applicable()}>
        <h3>Channel setup</h3>
        <ol>
          <For each={items()} key={(row) => row().item.title}>
            {(row) => <SetupItem item={row().item} index={row().index} controller={controller} />}
          </For>
        </ol>
        <Show when={inbox() === null}>
          <small class="nr-note">Reading the inbox…</small>
        </Show>
      </Show>
    </section>
  );
}

function DraftScreen(props: { controller: FormController; id: string }) {
  const controller = props.controller,
    snapshot = controller.state;
  const workspaces = controller.session?.snapshot.workspaces ?? [];
  const folders = [...new Set(knownFolders(controller.session))];
  const project = memo(() => projectLabel(snapshot().draft.folder) || "<project>");
  const warnings = memo(() => [...new Set(skillWarnings(snapshot().draft, snapshot().catalog))]);
  return (
    <>
      <div class="nr-body">
        <fieldset class="nr-fields" disabled={snapshot().busy}>
          <label class="nr-toggle nr-recommended">
            <input
              type="checkbox"
              name="recommended"
              checked={snapshot().draft.recommended}
              onChange={(event) => {
                const input = event.currentTarget as HTMLInputElement;
                if (snapshot().busy) input.checked = snapshot().draft.recommended;
                else controller.recommended(input.checked);
              }}
            />
            <span>Recommended setup</span>
            <small>Fills the usual choices below so only the tasks and each role's model are left to pick.</small>
          </label>
          <Show when={snapshot().draft.recommended} fallback={<CustomSetup controller={controller} id={props.id} />}>
            <RecommendedSummary controller={controller} />
          </Show>
          <div class="nr-grid">
            <Field
              id={`${props.id}-workspace`}
              name="workspace"
              label="Workspace"
              error={snapshot().errors.workspace}
              hint="The Herdr workspace the route's tabs open in."
            >
              <SelectBox
                name="workspace"
                id={`${props.id}-workspace`}
                value={snapshot().draft.workspace}
                options={workspaces.map((workspace) => [
                  workspace.workspace_id,
                  `${workspace.label || `Workspace ${workspace.number}`}${workspace.focused ? " (focused)" : ""}`,
                ])}
                busy={snapshot().busy}
                change={(workspace) => controller.change({ workspace })}
              />
            </Field>
            <Field
              id={`${props.id}-folder`}
              name="folder"
              label="Folder"
              error={snapshot().errors.folder}
              wide
              tabsHint
              hint={
                snapshot().draft.role === "adjutant"
                  ? `Tabs ${project()}-adjutant and ${project()}-orchestrator`
                  : `Tab ${project()}-orchestrator`
              }
            >
              <span class="nr-folder">
                <DraftInput
                  controller={controller}
                  id={props.id}
                  name="folder"
                  options={{ list: `${props.id}-folders`, placeholder: "/path/to/repo" }}
                />
                <datalist id={`${props.id}-folders`}>
                  <For each={folders} key={(folder) => folder}>
                    {(folder) => <option value={folder} />}
                  </For>
                </datalist>
              </span>
            </Field>
            <GitHubField controller={controller} id={props.id} />
            <PreviousChoices controller={controller} />
            <Show when={snapshot().draft.role === "adjutant"}>
              <h3 class="nr-sub">Adjutant</h3>
            </Show>
            <HarnessFields
              controller={controller}
              id={props.id}
              which="main"
              nameField="name"
              who={snapshot().draft.role === "adjutant" ? "Adjutant" : "Orchestrator"}
            />
            <Show when={snapshot().draft.role === "adjutant"}>
              <h3 class="nr-sub">Orchestrator</h3>
              <HarnessFields
                controller={controller}
                id={props.id}
                which="orch"
                nameField="orchName"
                who="Orchestrator"
              />
              <Field
                id={`${props.id}-routeName`}
                name="routeName"
                label="Route name"
                error={snapshot().errors.routeName}
                hint="Names the route's folder and its inbox."
              >
                <DraftInput controller={controller} id={props.id} name="routeName" />
              </Field>
              <Field
                id={`${props.id}-inbox`}
                name="inbox"
                label="Private inbox"
                error={snapshot().errors.inbox}
                wide
                hint="One inbox per route. The app never creates or writes it."
              >
                <span class="nr-inbox">
                  <DraftInput controller={controller} id={props.id} name="inbox" />
                  <CopyButton
                    label="Copy the inbox path"
                    copy={() => controller.deps.copy(snapshot().draft.inbox, "inbox path")}
                  />
                </span>
              </Field>
            </Show>
            <Field
              id={`${props.id}-request`}
              name="request"
              label="Route request"
              error={snapshot().errors.request}
              wide
              hint="The tasks, in your words; the orchestrator plans them and asks before it starts anything."
            >
              <RequestBox
                id={`${props.id}-request`}
                value={snapshot().draft.request}
                busy={snapshot().busy}
                change={(request) => controller.change({ request })}
              />
            </Field>
            <Show when={!snapshot().draft.recommended}>
              <Checkpointer controller={controller} id={props.id} />
            </Show>
          </div>
        </fieldset>
        <For each={warnings()} key={(warning) => warning}>
          {(warning) => (
            <p class="nr-warn" role="note">
              {warning}
            </p>
          )}
        </For>
        <div class="nr-sends">
          <span>Sends</span>
          <pre class="nr-preview">{preview(snapshot().draft, snapshot().catalog).command}</pre>
        </div>
        <ChannelChecklist controller={controller} />
      </div>
      <div class="dialog-actions nr-actions">
        <button value="cancel" class="btn ghost" formnovalidate>
          Cancel
        </button>
        <button
          type="submit"
          class="btn primary"
          name="start"
          disabled={snapshot().busy || !!controller.blocked()}
          title={controller.blocked() || null}
        >
          {snapshot().busy ? "Starting…" : "Start route"}
        </button>
      </div>
    </>
  );
}

function ResultStep(props: { step: RouteStep }) {
  return (
    <li class={`s-${props.step.status}`}>
      <span class="nr-check" aria-hidden="true">
        <Show when={props.step.status !== "skipped"} fallback="–">
          <Icon paths={props.step.status === "ok" ? icons.check : icons.close} />
        </Show>
      </span>
      <span class="nr-item">
        <span>{props.step.step}</span>
        <Show when={props.step.status !== "ok"}>
          <small>{stepNote(props.step)}</small>
        </Show>
      </span>
      <Show when={props.step.id}>
        <code>{props.step.id}</code>
      </Show>
    </li>
  );
}
function ResultScreen(props: { controller: FormController }) {
  const controller = props.controller;
  const result = memo(() => controller.state().result);
  const steps = map(
    () => result()?.steps ?? [],
    (step) => step,
    { keyed: (step) => `${step.method}|${step.step}` },
  );
  return (
    <>
      <div class="nr-body">
        <Show when={!result()?.ok}>
          <p class="error">{result()?.error ?? "A step failed."}</p>
        </Show>
        <ol class="nr-steps">
          <For each={steps()} key={(step) => `${step().method}|${step().step}`}>
            {(step) => <ResultStep step={step()} />}
          </For>
        </ol>
        <Show when={!result()?.ok}>
          <p class="nr-note">Nothing was closed: the tabs and panes created so far stay as they are.</p>
        </Show>
        <ChannelChecklist controller={controller} />
      </div>
      <div class="dialog-actions nr-actions">
        <Show when={!result()?.ok}>
          <button type="button" class="btn ghost" onClick={() => controller.back()}>
            Back to the form
          </button>
        </Show>
        <Show when={result()?.orchestrator_pane}>
          <button
            type="button"
            class="btn ghost"
            onClick={() => {
              const pane = result()?.orchestrator_pane;
              if (pane) {
                controller.deps.close();
                controller.deps.openPane(pane);
              }
            }}
          >
            Open the orchestrator
          </button>
        </Show>
        <button value="ok" class="btn primary">
          {result()?.ok ? "Done" : "Close"}
        </button>
      </div>
    </>
  );
}

export function RouteFormView(props: { controller: FormController; id: string }) {
  const snapshot = props.controller.state;
  return (
    <form method="dialog" novalidate onSubmit={(event) => props.controller.submit(event)}>
      <div class="nr-head">
        <h2>
          {snapshot().result ? (snapshot().result?.ok ? "Route started" : "The route stopped part way") : "New route"}
        </h2>
        <button value="cancel" class="icon-btn" title="Close" aria-label="Close" formnovalidate={!snapshot().result}>
          <Icon paths={icons.close} />
        </button>
      </div>
      <Show when={snapshot().result} fallback={<DraftScreen controller={props.controller} id={props.id} />}>
        <ResultScreen controller={props.controller} />
      </Show>
    </form>
  );
}

/** Mount one owned form; its immutable draft and request results update the native controls. */
export function routeForm(
  body: HTMLElement,
  catalog: Catalog,
  session: State | null,
  deps: Deps,
  options: FormOptions = {},
) {
  const controller = createRouteFormState(catalog, session, deps, options);
  const id = `nr${++uid}`;
  const view = mountRoot("form", () => {
    onCleanup(() => controller.stop());
    return <RouteFormView controller={controller} id={id} />;
  });
  body.parentNode?.replaceChild(view.el, body);
  controller.start();
  view.el.querySelector<HTMLElement>(controller.state().draft.folder ? "textarea" : "input[name=folder]")?.focus();
  return {
    el: view.el,
    stop: view.dispose,
    form: () => controller.state().draft,
    sent: () => controller.state().busy || controller.state().submitted !== null,
    setCatalog: controller.setCatalog,
  };
}
