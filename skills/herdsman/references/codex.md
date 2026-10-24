# Codex

Codex can take any agent type: implementer, integrator or reviewer. The notes below hold for every type unless a line names one.

## Launch form

```
herdr agent start im-<x> --kind codex --pane <id> --timeout 90000 -- \
  --model gpt-6-sol -c model_reasoning_effort=medium -a never \
  --sandbox workspace-write -c sandbox_workspace_write.network_access=true \
  -C <writable root>
```

- `-a never`: no approval prompts. `--sandbox workspace-write` with network on: git, gh, package installs and local test databases work.
- Do not add `--skip-git-repo-check`: this Codex rejects it ("unexpected argument") and the start times out. `-C` should be inside a git repository anyway.
- The user's `~/.codex/config.toml` may default to a higher effort (xhigh); pass the effort explicitly. User rule: implementers run at medium unless the interview sets another effort; reviewers keep xhigh.
- Codex shows `Goal active` and `Pursuing goal (Nm)` after a `/goal` prompt.
- The first launch in a new folder (a fresh worktree) can show a folder-trust dialog with "Yes, continue" selected, or a project tool-server (MCP) dialog, and `agent start` answers `agent_not_ready`. Read each dialog and answer it by its visible option; never send a blind `enter`. The dispatch plan names the trust answer for each new folder, so the user's go covers it. Without that go, quote the dialog to the user first, because the answer grants access to the user's folder. With "Yes, continue" selected, send `enter`, then wait for the idle prompt.
- A launch can open an "Update available" chooser with "1. Update now" selected, while `agent start` still answers `interactive_ready:true`. Read the pane after every launch. Send `esc` (skip): an update mid-route changes the binary under a running route and is the user's call.
- A restart of an agent whose earlier launch skipped an update can install it anyway: `agent start` answered `agent_not_ready` ("blocked during startup"), and seconds later the pane showed "Update ran successfully! Please restart Codex." with no key sent by the orchestrator. Before restarting a Codex agent mid-route, read the pane after the start command and act within seconds, or start it with the update check off (`-c check_for_update_on_startup=false`, check the key in the installed version's config docs first). If the update ran, tell the user and every peer session that runs Codex agents: running sessions keep the old binary, new launches and resumes get the new one.

## Update and resume

- Quit a Codex agent with `/quit` (`herdr agent prompt <name> "/quit"`), then send `enter`. Do not use ctrl+c: the pane can stay busy, and the next start fails with `agent_pane_busy`. Before a start in a reused pane, read a bare shell prompt ([`herdr.md`](herdr.md), Gotchas). When a pane that must hold an old session already runs a new empty one, end the new one with `/quit` and resume the old one by its id.
- `codex update` updates the standalone install in place; running panes keep the old binary until restarted.
- `codex debug models` prints the model catalog as JSON (slug, display name, efforts). Check it before a launch names a new model.
- An ended session (the pane is back at the shell prompt, "To continue this session, run: codex resume <id>") restarts in the same pane with the same Herdr name: `herdr agent start <name> --kind codex --pane <id> --timeout 90000 -- resume --model <model> -c model_reasoning_effort=<e> -a never --sandbox workspace-write -c sandbox_workspace_write.network_access=true -C <root> <session id>`. A goal that was paused shows "Resume paused goal?" with option 1 selected: `herdr pane send-keys <pane> enter` resumes it. The footer then shows the new model.

## The writable root

The sandbox writes only under the `-C` directory and /tmp. Everything an agent must write lives there: worktrees, report files, scratch files, package caches. A report path outside it (a docs folder of a sibling repo) blocks the agent at the very end of its task. Contracts may point at files anywhere for reading.

Practical layout: `-C <parent of the clone>` so that `<parent>/<clone>`, `<parent>/worktrees/<x>` and `<parent>/reports/` are all writable. Git-ignore `worktrees/` and `reports/` in the parent if the parent is itself a repository.

If the default package cache is not writable, agents set `TMPDIR=/private/tmp` and the cache directory under /private/tmp; `ps` may be denied inside the sandbox. The sandbox also denies Herdr commands, so a job that needs Herdr control stays with the orchestrator. With an adjutant, keep the private inbox out of every Codex agent's writable and readable roots (never under `-C` or an extra writable folder) and confirm with a negative inbox-write and socket-connect probe from that agent before relayed gos count ([`messages.md`](messages.md), Private inbox boundary). Codex support as a task agent does not prove it as a coordinator.

## Git and GitHub inside the sandbox

- No terminal for password prompts. A remote URL that embeds an account name triggers `Password for 'https://<account>@github.com': Device not configured` on fetch. Every remote git command needs the one-shot credential helper (see [`git-and-github.md`](git-and-github.md)); put it in the shared rules for fetch, pull and push. A fresh session, or a session on a new model, can forget it and run a plain `git fetch`: say "never a plain git fetch" in every dispatch prompt.
- `gh` needs `GH_TOKEN=$(gh auth token --user <account>)` when the default gh account cannot see the organisation.
- An agent that runs git or `gh` needs the network on (`-c sandbox_workspace_write.network_access=true`). Without it, `gh` prints a misleading "invalid stored credentials" error. When the repository's git folder lies outside the writable root, add `--add-dir <git folder>`, because the sandbox keeps that folder read-only.
- Implementers will use an existing clean worktree that already has the branch instead of creating a new one when the contract permits it; say which worktrees are free.

## Scope control

- A coverage rule phrased as "100% for package X" makes an implementer chase pre-existing gaps in unrelated files. Phrase it as "100% for the files you changed or added; list other gaps under gaps:".
- An implementer given "authorized base change" latitude will dissolve a GitHub stack and retarget a pull request on its own. Name each allowed GitHub mutation in the task file; everything else is forbidden by the shared rules.
- A `/goal` sent while Codex runs a long tool call (a wait for the review bot) does not open "Replace current goal", and a plain prompt sent mid-turn behaves alike. The send can be lost with no trace, can stay in the composer until you send `tab`, can arrive as a message to the running goal ("Messages to be submitted after next tool call"), or can interrupt the turn and pause the goal ("Goal paused"). Read the pane after each send. When the goal shows "Goal paused", send `/goal resume` alone. Write the whole instruction so that it works either way, word a mid-turn correction as "apply this, then continue", and arm any background wait again, because the old wait returns at the interruption.
- Interrupting the publishing agent per review-bot round works (esc, then a new `/goal` ending with "then resume task X"; committed work survives), but stretches the task; for a long bot loop ask the user for a second pane.

## Prompts that blocked or failed

- "Switch to <other model>?" rate-limit prompt: cursor sits on option 1 (switch). Choose the option that keeps the model and hides reminders (down, down, enter). Never let it switch the confirmed model.
- "Usage limit reached" kills in-flight work. Tell the user; never fall back to an API key; re-dispatch lost jobs after the reset.
- Bot review loop on the pull request: Codex handles it well when the shared rules give the exact gh commands (fetch comments, reply with `gh api .../pulls/<n>/comments/<id>/replies`, mention the bot in an issue comment to force a re-review of the current head).

## Queued follow-up inputs (Codex asks a question, then the answer arrives as a prompt)

When a Codex agent ends a turn with a question ("Can you enable write access to that directory?") and the orchestrator answers with a new `herdr agent prompt`, Codex takes the prompt as a normal turn but keeps the question in a "Queued follow-up inputs, ? 1 question, ⌥ + ↑ to answer" box. Herdr then reports `agent_status: blocked` and refuses further prompts (`agent_blocked`). Clear it: `herdr pane send-keys <pane> alt+up` opens the question ("Type your answer, enter submit, ctrl + ] skip"), then `herdr pane send-keys <pane> 'ctrl+]'` skips it; the next `agent prompt` is accepted. The watcher lists "Queued follow-up inputs" and "Type your answer" as BLOCKED text.

Read the key hint on screen: some builds show "⇧← to answer" instead of "⌥ + ↑"; then `send-keys <pane> shift+left` opens the question. To answer instead of skipping, type with `herdr pane send-text <pane> "<answer>"` and press `enter`. A plain message that tells a Codex agent to stop its goal leaves the footer at "Goal paused (/goal resume)"; a later `/goal` prompt is still taken, but if the turn ends without the report, send `/goal resume`. A sandboxed Codex agent cannot start a browser automation daemon, because the sandbox denies writes to the home folder. Start the daemon outside the sandbox first, then hand the agent over. When the daemon attaches to the user's own browser, ask for the user's attach click together with the other approvals while the user is present, because each new attach needs a click and times out. Without such a daemon, give the agent measured numbers from another agent rather than a browser task.

## Context hygiene (user rule)

- The Codex pane footer shows the context use per agent (for example "context 53%"). Some builds show no figure there (only the model, the folder and the goal state); then compact after every finished job without waiting for a reading.
- The next dispatch is a fix round on the same pull request or branch: compact right after the finished turn, with no threshold, except near the usage limit ([`flow-and-time.md`](flow-and-time.md), Quota is capacity). `herdr agent prompt <name> "/compact"`, wait until the pane shows the compaction finished and the prompt line is back, then the `/goal`.
- Next dispatch is a new task (a new contract, the integrator job, a branch the reviewer has not seen): `herdr agent prompt <name> "/new"` (fresh session in the same pane and process; the Herdr name stays), then the `/goal` that names the contracts again. `/new` first opens a chooser "Where should the new conversation run? 1. Current checkout 2. New worktree": send `herdr pane send-keys <pane> enter` for the current checkout, then read the pane for the empty prompt.
- The chooser does not always open: read the pane after `/new`, and send `enter` only when the chooser shows. With the empty prompt already there, send nothing.
- `/new` is refused while a `/compact` still runs ("'/new' is disabled while a task is in progress"). Read at least 25 pane lines for "Compacting context" before sending it.
- A `/goal` while an old goal is active opens "Replace current goal": send `enter`.
- After a goal ends with a report but no "Goal achieved" (for example it wrote a `NEEDS:` line), the footer reads "Goal stalled (/goal resume)". A `/compact` and then a new `/goal` work. The footer can keep the stalled text while the pane body shows "Working": judge by the pane body, not the footer. The same holds after "Goal achieved": after `/compact`, a new `/goal` ran ("Working", and the agent restated the new brief) while the footer still read "<old goal title> Goal achieved (Nm)".
- The goal driver checks the write set and the stop conditions against the brief FILE, not the chat: a prompt that says "you may edit X" changes nothing. Amend the file, then `/goal resume`. Send `/goal resume` alone: text after it becomes a new objective. With an adjutant, the approved watcher runs `scripts/resume-goal-when-idle.sh <registered-name>`, which sends exactly `/goal resume` to a registered task agent when its turn is idle, an anchored paused-goal marker shows and its full session identity still matches; never to the adjutant, never over a user pause or freeze, and never twice after an uncertain send ([`messages.md`](messages.md), Input provenance). Its idle reading and read-back are not verified live yet.
- The goal driver keeps the authorization scope that its goal named. A new goal outside that scope stalls. Close the "Replace goal" dialog with `esc`, send a plain prompt that names the authorization and the brief file, wait for the one-line acknowledgement, then send `/goal resume` alone.
- Never compact or clear while a goal runs; a prompt sent mid-goal is queued and would land at a random point. Codex refuses it anyway: "'/compact' is disabled while a task is in progress".
- A compaction takes 30 s to 2 min on a 70% to 90% context ("Compacting context (Ns)" then "Context compacted · <time>"); the footer figure updates only after it finishes. Do not re-send `/compact` while "Compacting context" is on screen.
- One task can fill more than half of an agent's context. Without hygiene the agent's second task starts near the limit.

## Usage quota

- All Codex agents on one account draw from one weekly pool. Codex `/status` shows the weekly limit; the figure is the same on every agent. The weekly figure in `/status` and in the footer is the share LEFT, not the share used.
- The "less than N% of your weekly limit left" lines in a pane are scrollback: they stay on screen after the weekly reset. Read `/status` before any decision that rests on the quota (the reviewer choice, folding one review into another). Reason: a stale banner can say that the pool is nearly empty while most of it is left, and a plan built on the banner skips reviews or wastes them.
- After `/status` the info panel can keep the next slash command in the input: `/new` stays there with the command list open. Read the pane; one `pane send-keys <pane> enter` submits it.
- At 0% a running goal stops with "Usage limit reached" and the pane footer reads "Goal hit usage limits (/goal resume)". The watcher reports BLOCKED. Work in the worktree stays as it was (possibly mid-change, tests red).
- Check the figure before a long dispatch; one review cycle with two active agents can take several percent. Below about 5%, expect the block during the round.
- Recovery: wait for the reset time, verify the figure moved, then `herdr agent prompt <name> "/goal resume"`. A session one-shot timer at the reset time (the CronCreate tool) wakes the orchestrator for this. A Claude reviewer is a separate pool and keeps working. Whether a task moves to a Claude implementer while waiting is the user's decision.
- "■ Selected model is at capacity. Please try a different model." stops a running goal mid-turn; so can a content-filter or safety notice ("This content can't be shown"). The footer reads "Goal stalled (/goal resume)" and the waiter reports `BLOCKED <agent> Goal stalled` when no working marker shows. It is a transient stop, not quota: send `/goal resume` alone once, read the pane, and check that the footer shows the goal runs. The goal picks up where it stopped and writes its report. For a content-filter stop, reword the brief in neutral terms. If the same agent stops a second time, do not resume again, because repeated resumes loop on the same stop: narrow the prompt, start a fresh session with a new brief, or ask the user. Read the reviewer pane on every PR-watcher TICK, not only on waiter events.

## Fix rounds

The implementer or the integrator updates its existing report file after a fix round (new head, per-finding lines under `decisions:`). The watcher emits REPORT-UPDATED on the file's mtime change and keeps watching the pane after the first report, so a fix round is visible. Prompt the fix as a new `/goal`; a Codex goal that reached "Goal achieved" is closed and does not resume.

## Report handshake

The last action of an implementer or the integrator is to write `<writable root>/reports/<task>.md` starting with `STATUS: complete` or `STATUS: blocked <reason>` and to reply with only that path. The watcher fires REPORT on the file, not on the reply. Read the report; grep `STATUS:`, `decisions:` and `gaps:` before opening anything else.
