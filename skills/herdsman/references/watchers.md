# Watchers: a background command that exits is the only reliable wake-up

## The wake-up fact

- A Monitor task keeps running and its stdout lines become chat events, but those events are delivered only while a turn is running or when the user sends a message. Between turns the session sleeps through them: the monitor sees a new report within a minute, and the orchestrator only when the user writes.
- A Bash command started with `run_in_background: true` re-invokes the session when it exits. That is the wake-up to build on: [`scripts/wait-event.sh`](../scripts/wait-event.sh) polls, exits on the first event pass, or exits after 1500 s with `TICK`. Claude Code stops a background command after 30 minutes unless its `timeout` asks for more (at most 2 hours), so run the waiter with `timeout: 1800000` and keep `DEADLINE` below it. Handle, re-arm, repeat. Never end a turn while the route runs without an armed waiter.
- Keep IDLE out of the waiter's events (a changed idle screen is noise that would wake the session for nothing); GOAL-DONE-NO-REPORT and REPORT cover the useful cases.
- Arm the waiter right after each dispatch, reviewer prompts included. A review written while no waiter runs is seen only at the next wake.
- One waiter per background task. Never put a pane read and the waiter in one background command that throws the waiter's output away (the event text is lost), and never detach a waiter from the session (`& disown`): it cannot wake the session and it shares the state folder. After any stop, check that only one waiter runs.
- Never stop a waiter with `pkill -f wait-event.sh` or any pattern on the skill's script path: every session on the machine runs the same script, so the pattern stops other sessions' waiters too (they exit with code 143 and their routes stop waking). Stop only your own: TaskStop with its task id, or `kill` a pid whose command line names your own route folder.
- Stop your own waiter by its task id, never by a pattern kill: other sessions on the machine run the same script for their routes.
- To wait for one agent only, run `herdr agent wait <name> --until idle --until done --until blocked --timeout 540000` as a background command, and arm it again on a timeout.

## Monitor tasks (secondary, inside a long turn only)

- The Bash tool blocks `sleep N; <cmd>` chains ("use Monitor with an until-loop"). Background Bash waits get killed periodically. One persistent Monitor per concern replaces a dozen waits.
- Every stdout line of the monitored script is a chat event; keep the script selective: only lines you act on.

## Arming

```
Monitor(command: "bash <scratchpad>/watch-implementers.sh", description: "<route> implementers and integrator: BLOCKED / STALL / IDLE / REPORT, every 2 min", timeout_ms: 1800000)
Monitor(command: "bash <scratchpad>/watch-reviewer.sh", description: "reviewer: REVIEW / BLOCKED / IDLE, every 90 s", timeout_ms: 1800000)
```

- Maximum is 30 minutes; re-arm on the expiry notice. Stop a dead one with TaskStop before restarting (the harness refuses a second start of the same command otherwise).
- Read the monitor's output file right after arming (`.../tasks/<id>.output`). A script that dies at its first line is silent in chat until expiry.

## Configuring the templates

Both scripts read their paths and pane ids from the environment, so a copy can stay unchanged: `HERDSMAN_REPORTS`, `HERDSMAN_STATE`, `HERDSMAN_IMPLEMENTERS` ("im-a im-b ..."), `HERDSMAN_INTEGRATOR` (optional, one name, watched the same way as an implementer), `HERDSMAN_PANE_im_a` (one per implementer and one for the integrator, dashes as underscores in every variable name: a dash makes the shell run the word as a command), `HERDSMAN_REVIEWER_PANE`, `HERDSMAN_REVIEWER_NAME`. The waiter also reads `HERDSMAN_DUE_im_a` (optional, epoch seconds, one per implementer or integrator): when that time has passed and the agent's report was not written or changed since the waiter first saw that due time, it prints `OVERDUE im-a due <HH:MM> ...` once. A new due value re-arms it. The waiter also reads `HERDSMAN_TASK_im_a` (optional: the report name `reports/<task>.md` when the agent name is not `im-<task>`, for example a route-specific name such as `rt-a`, or `i` for the integrator: `HERDSMAN_TASK_int_1=i`) and `HERDSMAN_REVIEWERS="name:pane name:pane"` (optional: several reviewer panes to watch for BLOCKED; without it, the one `HERDSMAN_REVIEWER_NAME`:`HERDSMAN_REVIEWER_PANE`). When an implementer becomes the integrator, move its name from `HERDSMAN_IMPLEMENTERS` to `HERDSMAN_INTEGRATOR` and set its task name to `i`. Keep a route's settings in a small wrapper script in the route folder (exports, then `exec` the waiter) and re-arm only through it, so that each re-arm runs the same command. Make every task name and every due time a variable of the wrapper and edit the wrapper when an agent changes task: a value fixed in a script hides the new report file, and a default due time fires false OVERDUE events at once. For a finished agent that stays in the watch list, pass a due time far in the future, never an empty value. An implementer stays in the watch list until its branch passes review, so that a fix round's REPORT-UPDATED is seen; its due time ends with each report and comes back with the next fix prompt. DEADLINE is only the waiter's own run time before `TICK`, not an agent's time box. The default (1500 s) leaves room under the 30-minute background limit; a shorter deadline only adds wakes, because events and the ten-minute health check (`HERDSMAN_STATUS_SPECS`, HEALTH events) already wake the orchestrator when something changes. The Monitor command then looks like `HERDSMAN_REPORTS=... HERDSMAN_PANE_im_a=w2:pG ... bash <skill>/scripts/watch-implementers.sh`. Or copy the script to the scratchpad and edit the defaults with the Edit tool.

Sanity test (fake `herdr`, one poll per run): `zsh <skill>/scripts/test/run-tests.sh` (or `/bin/bash`; from zsh or sh it runs itself again under `/bin/bash`). It checks syntax, the silent first pass, STALL, BLOCKED, IDLE, REPORT once, no exit when every report exists, REVIEW once, a missing report folder, OVERDUE (not before the due time, once after it, a stale existing report in a fix round, none once the report is written), the integrator's report, HEALTH (once per flag, again after it clears and returns), the machine line of `agent-status.sh`, `run-check.sh`, and the documented command forms started from zsh. CI runs it on macOS (bash 3.2, BSD tools) and Linux (GNU tools). Run it after any edit to the scripts.

## Script constraints (macOS)

- Monitors run under `/bin/bash` 3.2: no `declare -A`, no `mapfile`, no `${var,,}`. Use `case` functions for lookups (see `scripts/`).
- BSD and GNU `stat` and `date` take different flags (`stat -f %m` or `stat -c %Y`, `date -r <epoch>` or `date -d @<epoch>`). The scripts pick the form once at the top (`file_mtime`, `clock_of`); a new call goes through those helpers.
- The user's shell and the harness's shell on macOS are zsh. Start scripts as `/bin/bash <script>` with `NAME=value` prefixes: zsh passes the prefixes through unchanged. The Z tests run these forms from zsh.
- No GNU `timeout`; `ls --time-style` fails (`stat -f '%N %Sm' -t '%H:%M:%S'` instead). To bound a command that can stream, use `perl -e 'alarm 40; exec @ARGV' <command>`. A missing `timeout` followed by a `grep` prints nothing, which looks like "no match".
- Absolute paths only; the session cwd is not the script's cwd and a `cd` inside a backgrounded command does not carry over.
- State files (hashes, seen markers) go in a scratchpad subfolder; the first run of a GitHub-comment watcher dumps history, so seed the seen-file first.
- Every pipe stage must flush per line (`grep --line-buffered`); never `| head -N` in a stream.

## Events and reactions

| Event | Meaning | Reaction |
| --- | --- | --- |
| REPORT `<task> <path>` | an implementer's or the integrator's report file appeared | read it, check STATUS, verify the pull request or branch head, dispatch the review |
| REPORT-UPDATED `<task> <path>` | the report's mtime changed (a fix round finished) | read the new head and the per-finding decisions, dispatch the re-review |
| REVIEW `<path>` | reviewer report appeared | read VERDICT, forward findings or record pass |
| BLOCKED `<agent> <text>` | password, approval, model-switch, usage-limit or trust text on screen, or stalled-goal text with no working marker | see [`codex.md`](codex.md), [`claude-code.md`](claude-code.md) and [`muse.md`](muse.md); never type a password |
| STALL `<agent>` | working marker but unchanged screen across a poll | on the second STALL read the pane in full, then prompt the next step |
| HEALTH `<agent or machine> <flag>` | the waiter's ten-minute `agent-status.sh` run shows a new flag (waiter only) | an agent flag: as on a wake; a machine flag: no new heavy work; check the `run-check.sh` limits and the publisher |
| IDLE `<agent>` | no working marker and the screen changed | the agent finished or is waiting; read the last lines |

On every wake, list the reports folder (`ls -la <route>/reports`) as well as reading the waiter's line. The waiter exits on its first event, so a report written a few minutes after a HEALTH or STALL exit is not announced until the next re-arm; and while a question waits on the user, no waiter runs at all, so a report can wait unseen until the next wake. A HEALTH "NO WRITES" right after an agent's compaction or a fresh prompt is expected; re-arm without acting.

The implementer watcher never exits on its own (fix rounds continue after the first reports); stop it with TaskStop at route end. BLOCKED fires once per screen change, not on every poll.

- Read the pane before you act on BLOCKED. A loose pattern ("password", "permission denied") also matches normal command output, such as a push log. Match only real prompt texts in the scripts.

- An agent can write its report twice (first with placeholder fields while it waits for CI or a long run, then the final one), which fires REPORT-UPDATED twice. Read `STATUS:` and the pane; re-arm on a placeholder. The placeholder can also be the first write (`STATUS: draft (in progress)`, `STATUS: PENDING`), which fires REPORT. Keep that agent's due variable and do not dispatch a review until `STATUS:` is final; an interim report can still carry a finding worth an amendment. `STATUS: complete` is not final by itself either: a report can say complete while its CI and full-suite lines still hold placeholders (`CI_STATE`, `FULL_SUITE`) for runs in progress. Grep the report for capitalized placeholders before you act on it. An interim write also switches off that agent's OVERDUE alarm (the report changed after the waiter saw the due time), so after each interim write change the due value in the wrapper, even by one minute, before you re-arm. Each interim write is also a whole orchestrator wake, and an implementer that waits for its full suite and CI can write its report several times. In fix-round briefs, ask for one report write after CI and the full suite finish, or earlier only for a blocker. Then the due time must include that wait: the last push plus the CI test job (its usual run time in that repository) plus the report. Each queued note that adds a commit restarts CI, so move the due with it; otherwise OVERDUE fires while the agent sits idle at its prompt waiting on background shells, which is not a stall.
- An agent can update its report within minutes of a prompt. Before you act, check that its turn ended (the pane shows the finish line) and that the head and test lines are new.
- Verify a watcher by comparing the file's change time with the event time and with the time the orchestrator acted.
- Moving the route folder is safe for the waiter: its state is keyed by task name, and `mv` keeps change times. Rewrite the paths in the contracts and tell agents in flight their new report path.

## Watching pull requests

- Bot reviews arrive minutes after a push. A GitHub watcher (120 s) that diffs reviews, inline comments and issue comments on the route's branches keeps the orchestrator out of the loop until something new lands. Compare comment ids, not lines: a push moves lines and the watcher re-emits.
- The watcher is [`scripts/watch-prs.sh`](../scripts/watch-prs.sh) (`HERDSMAN_PRS`, `HERDSMAN_REPO`, `HERDSMAN_PR_STATE`, `HERDSMAN_GH_ACCOUNT`). Run it as a second background task next to `wait-event.sh` and re-arm both after each wake. Keep its settings in a route wrapper (`<route>/watch-prs.sh`) like the waiter's. It also reports new heads and check results, so a push and its CI outcome wake the orchestrator. Only the newest run of each check name counts, so the old run that a push or a rerun cancelled is no failure. A pull request that the watcher cannot read (no access, repeated GitHub errors) is not "no change": after 3 failed polls in a row for one pull request, the watcher prints `PR-WATCH-ERROR PR <n> could not be read` and exits 1, so the orchestrator wakes. When the deadline ends after a failed last poll, the TICK names the pull request (`TICK no PR change (not read: <n>) in ...`). Read that pull request yourself before you trust the TICK. Start it when the user asks to watch the pull requests and the bot review comments.
- A watcher that wakes the orchestrator on every check transition (pending, e2e SUCCESS, a preview deploy SUCCESS) costs a whole turn for each wake, with no action. So the watcher records those silently and wakes only on comments, reviews, heads, state, failed checks, and the checks named in `HERDSMAN_PR_WAKE_CHECKS` (default `claude-review|test`) reaching SUCCESS.
- Review bots (claude-review) post an "in progress" comment first and edit it in place. The new comment id fires at once. Wait for the bot's check to turn SUCCESS before reading the review: the same comment id then holds the final text.
- In a route with one pull request per task, the orchestrator reads every bot review too, instead of trusting the implementer's bot loop, because implementers can decline cheap points (style, a defensive clone, a disabled button) that the user's rule says to fix, and can leave work out with claims ("the helper does not return the user") that the code does not support. Forward each cheap point as a queued steer to the agent that owns the branch.
- Decide from structural data (`gh pr view --json statusCheckRollup,mergeable,reviewDecision`, review `commit.oid` versus `headRefOid`, thread `isResolved`), not from bot wording.
- `gh pr checks <n> --watch` confirms a fix; `gh run view <id> --log-failed` is the evidence for the agent that publishes.
- A HEALTH "no writes" flag while other routes share the machine usually means the agent's check waits on the machine lock, not a stall. Look for queued `run-check.sh` jobs (`ps -eo etime,command | grep lockf`) before nudging the agent.
