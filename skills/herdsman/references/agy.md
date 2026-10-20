# Antigravity CLI (agy)

These notes come from a probe of a pane that the user started. They do not yet hold a verified launch command, working marker or interrupt key. Before a route uses this harness, probe those three first (as [`SKILL.md`](../SKILL.md), References, says for a harness with no notes) and add them here.

Antigravity CLI can take any agent type. Herdr kind: `agy`. The probed pane used a Gemini model and "auto" mode.

## Launch and modes

- The footer shows the mode, the model and the effort: `auto · <model> · <effort>`. "Auto mode: edits & safe commands auto-approved (shift+tab to cycle)" is shown above the input. For a reviewer, auto mode is enough; it must still not edit files, so the contract says so.
- The banner shows the account and its plan, the model, and the workspace folder. `/add-dir` adds a folder to the workspace (a contract or a route folder outside the workspace needs it).
- `/effort` sets the reasoning effort; `/config` holds the settings.

## Commands

- `/compact`: summarize the history to free context. `/clear` (alias `new`): a new conversation in the same pane.
- `/context`: shows the context use. `/exit` (`quit`) ends the CLI.
- `/help` opens a panel with the views "general", "commands", "shortcuts": `right` or `tab` switches the view, `esc` closes it.

## Quirks

- A notice box ("┃ ... esc to dismiss", for example the verbosity notice) takes the keys that follow, so text sent with `pane send-text` can be lost or arrive later. Send `esc` first and read the input line before you type.
- Typing `/` opens a completion list ("↑/↓ Navigate · enter Select · tab Complete"). `esc` cancels it; the typed text stays in the input. Clear it with `backspace` and read the pane again.
- `herdr agent prompt <pane> "<text>"` works (Herdr reports the agent as `agy`, status `idle`).
