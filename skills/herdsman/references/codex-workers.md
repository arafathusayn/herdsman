# Codex implementation workers

## Launch form (verified with Codex CLI 0.155.1, 2026-09-22)

```
herdr agent start wk-<x> --kind codex --pane <id> --timeout 90000 -- \
  --model gpt-6-sol -c model_reasoning_effort=medium -a never \
  --sandbox workspace-write -c sandbox_workspace_write.network_access=true \
  -C <writable root>
```

- `-a never`: no approval prompts. `--sandbox workspace-write` with network on: git, gh, package installs and local test databases work.
- Do not add `--skip-git-repo-check`: this Codex rejects it ("unexpected argument") and the start times out. `-C` should be inside a git repository anyway.
- The user's `~/.codex/config.toml` may default to a higher effort (xhigh); pass the effort explicitly. Medium is the default since 2026-09-23 (the user's instruction after a day of routes on high); the reviewer keeps xhigh.
- Codex shows `Goal active` and `Pursuing goal (Nm)` after a `/goal` prompt.

## Update and resume (verified with Codex CLI 0.159.2, 2026-09-30)

- `codex update` updates the standalone install in place (0.155.1 to 0.159.2); running panes keep the old binary until restarted.
- `codex debug models` prints the model catalog as JSON (slug, display name, efforts). `gpt-6.1-sol` exists from 0.159.
- An ended session (the pane is back at the shell prompt, "To continue this session, run: codex resume <id>") restarts in the same pane with the same Herdr name: `herdr agent start <name> --kind codex --pane <id> --timeout 90000 -- resume --model <model> -c model_reasoning_effort=<e> -a never --sandbox workspace-write -c sandbox_workspace_write.network_access=true -C <root> <session id>`. A goal that was paused shows "Resume paused goal?" with option 1 selected: `herdr pane send-keys <pane> enter` resumes it. The footer then shows the new model.

## The writable root

The sandbox writes only under the `-C` directory and /tmp. Everything a worker must write lives there: worktrees, report files, scratch files, package caches. A report path outside it (a docs folder of a sibling repo) blocks the worker at the very end of its task. Contracts may point at files anywhere for reading.

Practical layout: `-C <parent of the clone>` so that `<parent>/<clone>`, `<parent>/worktrees/<x>` and `<parent>/reports/` are all writable. Git-ignore `worktrees/` and `reports/` in the parent if the parent is itself a repository.

If the default package cache is not writable, workers set `TMPDIR=/private/tmp` and the cache directory under /private/tmp; `ps` may be denied inside the sandbox.

## Git and GitHub inside the sandbox

- No terminal for password prompts. A remote URL that embeds an account name triggers `Password for 'https://<account>@github.com': Device not configured` on fetch. Every remote git command needs the one-shot credential helper (see `git-and-github.md`); put it in the shared rules for fetch, pull and push.
- `gh` needs `GH_TOKEN=$(gh auth token --user <account>)` when the default gh account cannot see the organisation.
- Workers will use an existing clean worktree that already has the branch instead of creating a new one when the contract permits it; say which worktrees are free.

## Scope control

- A coverage rule phrased as "100% for package X" makes a worker chase pre-existing gaps in unrelated files. Phrase it as "100% for the files you changed or added; list other gaps under gaps:".
- A worker given "authorized base change" latitude will dissolve a GitHub stack and retarget a pull request on its own. Name each allowed GitHub mutation in the task file; everything else is forbidden by the shared rules.
- Interrupting a worker per review-bot round works (esc, then a new `/goal` ending with "then resume task X"; committed work survives), but stretches the task; for a long bot loop ask the user for a second worker pane.

## Prompts that blocked or failed

- "Switch to <other model>?" rate-limit prompt: cursor sits on option 1 (switch). Choose the option that keeps the model and hides reminders (down, down, enter). Never let it switch the confirmed model.
- "Usage limit reached" kills in-flight work. Tell the user; never fall back to an API key; re-dispatch lost jobs after the reset.
- Bot review loop on the pull request: Codex handles it well when the shared rules give the exact gh commands (fetch comments, reply with `gh api .../pulls/<n>/comments/<id>/replies`, mention the bot in an issue comment to force a re-review of the current head).

## Queued follow-up inputs (Codex asks a question, then the answer arrives as a prompt)

When a Codex worker ends a turn with a question ("Can you enable write access to that directory?") and the orchestrator answers with a new `herdr agent prompt`, Codex takes the prompt as a normal turn but keeps the question in a "Queued follow-up inputs, ? 1 question, ⌥ + ↑ to answer" box. Herdr then reports `agent_status: blocked` and refuses further prompts (`agent_blocked`). Clear it: `herdr pane send-keys <pane> alt+up` opens the question ("Type your answer, enter submit, ctrl + ] skip"), then `herdr pane send-keys <pane> 'ctrl+]'` skips it; the next `agent prompt` is accepted. The watcher lists "Queued follow-up inputs" and "Type your answer" as BLOCKED text.

## Context hygiene (user rule, 2026-09-22)

- The Codex pane footer shows the context use per worker (for example "context 53%").
- Above 50% and the next dispatch is a fix round on the same pull request: `herdr agent prompt wk-x "/compact"`, wait until the pane shows the compaction finished and the prompt line is back, then the `/goal`.
- Next dispatch is a new task: `herdr agent prompt wk-x "/new"` (fresh session in the same pane and process; the Herdr name stays), then the `/goal` that names the shared rules and the task file again.
- Never compact or clear while a goal runs; a prompt sent mid-goal is queued and would land at a random point. Codex refuses it anyway: "'/compact' is disabled while a task is in progress".
- A compaction takes 30 s to 2 min on a 70% to 90% context ("Compacting context (Ns)" then "Context compacted · 1m 41s"); the footer figure updates only after it finishes (88% → 4%, 69% → 0%). Do not re-send `/compact` while "Compacting context" is on screen.
- Measured 2026-09-22 after one task each: 53% to 88% of context per worker. Without hygiene the second task of a worker starts near the limit.

## Usage quota

- All Codex workers on one account draw from one weekly pool. Codex `/status` shows the weekly limit; the figure is the same on every worker.
- At 0% a running goal stops with "Usage limit reached" and the pane footer reads "Goal hit usage limits (/goal resume)". The watcher reports BLOCKED. Work in the worktree stays as it was (possibly mid-change, tests red).
- Check the figure before a long dispatch; 7% was gone within one review cycle with two workers active. Below about 5%, expect the block during the round.
- Recovery: wait for the reset time, verify the figure moved, then `herdr agent prompt wk-x "/goal resume"`. A session one-shot timer at the reset time (the CronCreate tool) wakes the orchestrator for this. The Claude reviewer is a separate pool and keeps working. Whether an implementation moves to a Claude worker while waiting is the user's decision.

## Fix rounds

The worker updates its existing report file after a fix round (new head, per-finding lines under `decisions:`). The watcher emits REPORT-UPDATED on the file's mtime change and keeps watching the pane after the first report, so a fix round is visible. Prompt the fix as a new `/goal`; a Codex goal that reached "Goal achieved" is closed and does not resume.

## Report handshake

The worker's last action is to write `<writable root>/reports/<task>.md` starting with `STATUS: complete` or `STATUS: blocked <reason>` and to reply with only that path. The watcher fires REPORT on the file, not on the reply. Read the report; grep `STATUS:`, `decisions:` and `gaps:` before opening anything else.
