# Herdr: command surface, verified forms, gotchas

The installed binary is the authority: `herdr --help`, then a group without a subcommand (`herdr agent`, `herdr pane`, `herdr tab`). Do not run bare `herdr` (it attaches the TUI). Do not probe mutating commands by omitting arguments (`herdr workspace create` runs with defaults).

## Verified forms

- Own position: `herdr agent list` shows the caller as `focused:true` with `pane_id`, `tab_id`, `workspace_id`.
- Tab: `herdr tab create --workspace <w> --cwd <dir> --label "<text>" --no-focus` returns `tab.tab_id` and `root_pane.pane_id`.
- Split: `herdr pane split --pane <id> --direction right|down --cwd <dir> --no-focus` returns `.result.pane.pane_id`. Lettered ids skip letters (pG, pH, pJ, pK, pM). The positional form with `--ratio` printed nothing and still split; use the `--pane` flag form.
- Move inside the same tab is a no-op (`changed:false, reason:"same_tab"`). To re-slot: `herdr pane move <pane> --new-tab --workspace <w> --label tmp --no-focus`, then `herdr pane move <pane> --tab <t> --split right --target-pane <sibling> --no-focus`. The temporary tab closes itself; the pane id stays.
- Start Codex: `herdr agent start <name> --kind codex --pane <id> --timeout 90000 -- <codex args>`. Start Claude: `--kind claude ... -- <claude args>`. Success = `interactive_ready:true` in the JSON.
- Prompt: `herdr agent prompt <name or pane> "<text>"`. `--timeout` requires `--wait`. Prompts sent to a working Codex are accepted (status flips to working). Prompts sent to a working Muse were dropped; re-send when idle.
- Read: `herdr pane read <id> --lines N` (last N lines), `--source recent-unwrapped` for watcher hashing. `herdr agent read` fails on alternate-screen agents (OpenCode); `pane read` works everywhere.
- Close own pane: `herdr pane close <pane>`.

## Etiquette (user rule)

- Panes first, then agents, then prompts. One Herdr mutation per Bash call. Never parallel tool calls with two mutations. Reads may run in parallel.
- Four implementers get a 2 by 2 grid in their own tab labelled "Implementer Agents"; the reviewer gets its own tab labelled "Reviewer". A 134x51 orchestrator pane split 2 by 2 gives 67x25 implementer panes; sibling splits of the orchestrator pane are too narrow (agent names vanished at 34 columns).
- The route launch (tab, panes, Docker, contract files) waits for the user's go on the dispatch plan.

## Gotchas

- A `herdr agent start` that the harness rejected can still have run. The pane then holds a stuck name (`agent_launch_pending`); `agent rename --clear`, the same name (`agent_name_taken`) and a fresh name (`agent_pane_busy`, "not an available shell") all fail. Remedy: close that pane and split a fresh one.
- Agent names must match `^[a-z][a-z0-9_-]{0,31}$` (`invalid_agent_name` otherwise) and are global across workspaces: `agent_name_taken` means another workspace's route may hold the name. Use route-specific names and keep display names in contracts.
- Read a pane before you reuse it: the user may have started another session there. `herdr agent prompt <pane id> "/quit"` reaches an agent that has no name.
- `herdr pane list | grep <label>` also matches panes whose text holds the label; filter the JSON by its `label` field.
- Every herdr call must be a plain top-level command. Calls inside `for` loops with `set --`, or with long `${VAR//x/y}` substitutions, exit 1 with no output.
- Herdr's agent state is unreliable for Codex: it may say `working` at the prompt and `idle` mid-task. Truth is the pane text: an active Codex shows `• Working (Ns • esc to interrupt)` or `Pursuing goal (Nm)`; the `› Ask Codex to do anything` line is always present.
- Names vanish from `herdr agent list` for Codex agents in narrow panes; `herdr agent rename <pane> <name>` re-registers.
- Herdr reports an agent `idle` while a long foreground command runs; read the screen before treating it as stalled.
- `agent_status: blocked` means Herdr recognised an approval UI; `unknown` does not prove completion.
- Quit a Claude agent with ctrl+c twice (sometimes four), wait for the shell prompt, then start again; flags apply per process.
- `herdr channel set` and `herdr update --handoff` refuse to run inside a Herdr pane; the user runs them outside. The handoff kept panes and agents alive.
- `/goal` is a Codex feature. Claude Code, Muse and OpenCode take plain prompts.
- `herdr agent prompt` answers `agent_blocked` while Codex shows a queued follow-up question; clear it with `pane send-keys <pane> alt+up` then `'ctrl+]'` (see `codex.md`). Key names: `alt+up`, `ctrl+]`, `esc`, `enter`. `pane send-text` and `pane run` (text plus Enter) bypass the agent state check; use them only when the agent commands refuse.
- A `grep -o '"type":...'` on a prompt result hides errors: the error JSON has no `type`. Print the result with `head -c 300` instead and look for `agent_prompted`.
- `herdr agent wait <name> --timeout N` ends with `{"error":{"code":"timeout",...}}` on exit 1 when the agent stays busy; a `grep` for `agent_status` then prints nothing ("No matches found"). A long Muse turn outlasts a one-hour wait; rely on the waiter (REPORT-UPDATED) instead of long agent waits.
