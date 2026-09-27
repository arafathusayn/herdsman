# Watchers: a background command that exits is the only reliable wake-up

## The wake-up fact (learned 2026-09-22, "Your monitor is not working. Fix for real")

- A Monitor task keeps running and its stdout lines become chat events, but those events are delivered only while a turn is running or when the user sends a message. Between turns the session sleeps through them. A report written at 14:29 was seen at 14:30 by the monitor and by the orchestrator only at 14:31 when the user complained.
- A Bash command started with `run_in_background: true` re-invokes the session when it exits. That is the wake-up to build on: `scripts/wait-event.sh` polls, exits on the first event pass, or exits after 540 s with `TICK` (the Bash tool caps `timeout` at 600000 ms). Handle, re-arm, repeat. Never end a turn while the route runs without an armed waiter.
- Keep IDLE out of the waiter's events (a changed idle screen is noise that would wake the session for nothing); GOAL-DONE-NO-REPORT and REPORT cover the useful cases.

## Monitor tasks (secondary, inside a long turn only)

- The Bash tool blocks `sleep N; <cmd>` chains ("use Monitor with an until-loop"). Background Bash waits get killed periodically. One persistent Monitor per concern replaces a dozen waits.
- Every stdout line of the monitored script is a chat event; keep the script selective: only lines you act on.

## Arming

```
Monitor(command: "bash <scratchpad>/watch-workers.sh", description: "<route> workers: BLOCKED / STALL / IDLE / REPORT, every 2 min", timeout_ms: 1800000)
Monitor(command: "bash <scratchpad>/watch-reviewer.sh", description: "reviewer: REVIEW / BLOCKED / IDLE, every 90 s", timeout_ms: 1800000)
```

- Maximum is 30 minutes; re-arm on the expiry notice. Stop a dead one with TaskStop before restarting (the harness refuses a second start of the same command otherwise).
- Read the monitor's output file right after arming (`.../tasks/<id>.output`). A script that dies at its first line is silent in chat until expiry.

## Configuring the templates

Both scripts read their paths and pane ids from the environment, so a copy can stay unchanged: `HERDSMAN_REPORTS`, `HERDSMAN_STATE`, `HERDSMAN_WORKERS` ("wk-a wk-b ..."), `HERDSMAN_PANE_wk_a` (one per worker, dashes as underscores), `HERDSMAN_REVIEWER_PANE`, `HERDSMAN_REVIEWER_NAME`. The Monitor command then looks like `HERDSMAN_REPORTS=... HERDSMAN_PANE_wk_a=w2:pG ... bash <skill>/scripts/watch-workers.sh`. Or copy the script to the scratchpad and edit the defaults with the Edit tool.

Sanity test (fake `herdr`, one poll per run, bash 3.2): `/bin/bash ~/.claude/skills/herdsman/scripts/test/run-tests.sh`. It checks syntax, the silent first pass, STALL, BLOCKED, IDLE, REPORT once, ALL-REPORTS with exit 0, REVIEW once, and a missing report folder. Run it after any edit to the templates.

## Script constraints (macOS)

- Monitors run under `/bin/bash` 3.2: no `declare -A`, no `mapfile`, no `${var,,}`. Use `case` functions for lookups (see `scripts/`).
- No GNU `timeout`; `ls --time-style` fails (`stat -f '%N %Sm' -t '%H:%M:%S'` instead).
- Absolute paths only; the session cwd is not the script's cwd and a `cd` inside a backgrounded command does not carry over.
- State files (hashes, seen markers) go in a scratchpad subfolder; the first run of a GitHub-comment watcher dumps history, so seed the seen-file first.
- Every pipe stage must flush per line (`grep --line-buffered`); never `| head -N` in a stream.

## Events and reactions

| Event | Meaning | Reaction |
| --- | --- | --- |
| REPORT `<task> <path>` | worker report file appeared | read it, check STATUS, verify the pull request, dispatch the review |
| REPORT-UPDATED `<task> <path>` | the report's mtime changed (a fix round finished) | read the new head and the per-finding decisions, dispatch the re-review |
| REVIEW `<path>` | reviewer report appeared | read VERDICT, forward findings or record pass |
| BLOCKED `<agent> <text>` | password, approval, model-switch, usage-limit or trust text on screen | see `codex-workers.md` and `claude-reviewer.md`; never type a password |
| STALL `<agent>` | working marker but unchanged screen across a poll | on the second STALL read the pane in full, then prompt the next step |
| IDLE `<agent>` | no working marker and the screen changed | the agent finished or is waiting; read the last lines |

The worker watcher never exits on its own (fix rounds continue after the first reports); stop it with TaskStop at route end. BLOCKED fires once per screen change, not on every poll.

## Watching pull requests

- Bot reviews arrive minutes after a push. A GitHub watcher (120 s) that diffs reviews, inline comments and issue comments on the route's branches keeps the orchestrator out of the loop until something new lands. Compare comment ids, not lines: a push moves lines and the watcher re-emits.
- Decide from structural data (`gh pr view --json statusCheckRollup,mergeable,reviewDecision`, review `commit.oid` versus `headRefOid`, thread `isResolved`), not from bot wording.
- `gh pr checks <n> --watch` confirms a fix; `gh run view <id> --log-failed` is the evidence for the worker.
