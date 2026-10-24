// Agent actions that need a form, shared by the agent panel and the palette.
import { run } from "./actions.ts";
import { type Member, splitArgs } from "./model.ts";
import { formDialog } from "./ui.ts";

export const keySet: { label: string; keys: string[]; hint: string }[] = [
  {
    label: "Enter",
    keys: ["enter"],
    hint: "Submits what sits in the agent's input",
  },
  {
    label: "Esc",
    keys: ["esc"],
    hint: "Usually ends the agent's running turn",
  },
  {
    label: "Tab",
    keys: ["tab"],
    hint: "Queues a composer message in some agents",
  },
  {
    label: "Alt+↑",
    keys: ["alt+up"],
    hint: "Recalls a queued message in some agents",
  },
  {
    label: "Ctrl+]",
    keys: ["ctrl+]"],
    hint: "Clears a queued question in some agents",
  },
  {
    label: "Ctrl+C",
    keys: ["ctrl+c"],
    hint: "Interrupts the foreground process",
  },
];

/** Arrow keys, for agent menus, choosers and input history. */
export const arrowKeys: { label: string; name: string; keys: string[] }[] = [
  { label: "←", name: "Left", keys: ["left"] },
  { label: "↑", name: "Up", keys: ["up"] },
  { label: "↓", name: "Down", keys: ["down"] },
  { label: "→", name: "Right", keys: ["right"] },
];

const namePattern = "[a-z][a-z0-9_\\-]{0,31}";

export async function sendOtherKeys(pane: string): Promise<void> {
  const v = await formDialog(
    "Send keys",
    [
      {
        name: "keys",
        label: "Key names",
        placeholder: "esc enter ctrl+c",
        hint: "herdr key names, separated by spaces",
        required: true,
      },
    ],
    "Send",
  );
  const keys = v?.["keys"]?.split(/\s+/).filter(Boolean) ?? [];
  if (keys.length) await run({ type: "keys", pane, keys });
}

export async function startAgent(pane: string, kinds: string[]): Promise<void> {
  const v = await formDialog(
    "Start an agent",
    [
      {
        name: "name",
        label: "Agent name",
        placeholder: "im-auth",
        pattern: namePattern,
        required: true,
        hint: "Lowercase letters, digits, - and _",
      },
      {
        name: "kind",
        label: "Kind",
        placeholder: "claude",
        list: kinds,
        required: true,
        hint: "Any agent kind herdr knows",
      },
      {
        name: "args",
        label: "Arguments",
        placeholder: "--model … --effort high",
        hint: "Passed to the agent as written; quotes group words",
      },
    ],
    "Start",
  );
  if (!v) return;
  await run({
    type: "start",
    pane,
    name: v["name"] ?? "",
    kind: v["kind"] ?? "",
    args: splitArgs(v["args"] ?? ""),
  });
}

export async function renamePane(m: Member): Promise<void> {
  const v = await formDialog(
    "Rename",
    [
      {
        name: "label",
        label: "Pane label",
        value: m.pane?.label ?? "",
        placeholder: "im-auth",
      },
      ...(m.agent
        ? [
            {
              name: "name",
              label: "Agent name",
              value: m.agent.name ?? "",
              pattern: namePattern,
            },
          ]
        : []),
    ],
    "Save",
  );
  if (!v) return;
  if ((v["label"] ?? "") !== (m.pane?.label ?? ""))
    await run({
      type: "rename_pane",
      pane: m.pane_id,
      label: v["label"] ?? "",
    });
  if (v["name"] && v["name"] !== m.agent?.name) await run({ type: "rename_agent", pane: m.pane_id, name: v["name"] });
}
