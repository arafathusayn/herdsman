---
name: herdsman
description: "Run an orchestrator, implementer, integrator, reviewer loop inside Herdr: the current agent session orchestrates, implementer agents (any harness Herdr can start) implement one task each (optionally in their own git worktree and branch), an integrator agent combines the accepted branches into one branch and publishes it when the tasks land together, reviewer agents on a stronger model review every pull request, branch, combined head or tree snapshot with named review skills and report only P0 and P1 findings, and the loop of fixes and re-reviews runs until each task passes. A checkpointer pane keeps the orchestrator's memory current and its context compacted. Use when the user says herdsman, asks to orchestrate coding tasks with implementer agents and a reviewer agent through Herdr, or asks for parallel implementation agents with a review loop. Requires HERDR_ENV=1."
argument-hint: "[run <route request> | checkpoint [start|now|probe|status|stop] [--every <min>] [--limit <tokens>] | status | help]"
license: GPL-3.0-only
---

# herdsman: orchestrator, implementers, integrator, reviewers

One agent session (this one) orchestrates. It delegates all other work to three agent types: implementers, an integrator and reviewers (see Agent types). Each agent may use a different harness and model.

## Agent types

The user talks only to the orchestrator. Any harness and model can take any agent type; the interview sets them.

- **Orchestrator** (one: this session). Runs the interview, writes the contracts, launches the other agents, waits for events, applies the goal gate, forwards findings, verifies results and records the route. It never edits application code, never runs the application's tests and never pushes application code, unless the user grants it the push of reviewed commits for a route.
- **Implementer** (one per task; names `im-<x>`). Does one task on its own local branch in its own worktree, tests first, and writes a task report. In a route with one pull request per task, it also pushes, opens the pull request and runs the review-bot loop. In an integration route, it never touches GitHub and writes each change that another task's files need as a `handoff:` line in its report.
- **Integrator** (one in an integration route, none in a route with one pull request per task; name `int-1`). Combines the accepted implementer branches in a fixed order in its own worktree, applies every handoff, fixes only integration breakage and the final review's findings on the combined branch (test first), and runs every gate. It is the only agent that pushes, opens or updates the pull request, answers review threads and runs the review-bot loop. It works in phases, and each phase starts with an orchestrator prompt: combine, final-review fixes, add a late task, push. It can be a new agent or an implementer whose task was accepted; clear that implementer first, because integration is a new job.
- **Reviewer** (one or more; names `rv-<n>`). Reviews each implementer branch against its contract and, in an integration route, the combined head against the base branch before anything is pushed (the final review). Writes one numbered report per review. It never edits code, commits, pushes or posts. It usually runs on a stronger model than the implementers. A second reviewer runs in parallel only with the user's go, when reviews queue up.

## References

Read [`references/harness-guards.md`](references/harness-guards.md) before the first shell command. Read the other references when the step names them.

Harness notes: `references/` has one file per harness used so far (launch form, prompt form, slash commands for compact and clear, working and blocked markers, quirks): [`codex.md`](references/codex.md), [`muse.md`](references/muse.md) and [`claude-code.md`](references/claude-code.md). The notes are per harness, not per agent type, because any harness can take any type. Before a route uses a harness with no notes file, probe it (launch in a scratch pane, send a prompt, find its compact, clear and interrupt commands and its working marker) and write the notes file first.

Concepts: the rules that earlier routes taught, with their reasons, one file per concept. Read them before the dispatch plan (step 1), and again when a step touches the concept.

- [`references/flow-and-time.md`](references/flow-and-time.md): due times that the orchestrator enforces, the moving bottleneck (reviewers, CI, the integrator), work ahead, idle state, quota as capacity.
- [`references/context-hygiene.md`](references/context-hygiene.md): compact for a follow-up, clear for a new job, when harnesses refuse the commands, the orchestrator's own context.
- [`references/ownership-and-integration.md`](references/ownership-and-integration.md): file and line ownership, handoffs, the integrator's phases, fixes on another author's pull request, migration numbers and stacks.
- [`references/review-discipline.md`](references/review-discipline.md): what a review reports and proves, the goal gate, orchestrator amendments, disagreements, human reviews.
- [`references/shared-environment.md`](references/shared-environment.md): test databases, CPU and shared memory on one machine, tests that prove the product.
- [`references/shipping.md`](references/shipping.md): routes that push reviewed commits straight to the default branch: the ship gate, who pushes, acceptance on the deployed app, the user's browser.
- [`references/checkpointer.md`](references/checkpointer.md): the checkpointer requests and numbers (see `checkpoint` below).
- [`references/optional-techniques.md`](references/optional-techniques.md): techniques that are off unless the user picks them: plan gate, contract pre-review, progress file, locked proof tests, visual check, guide feedback.

Tool notes: [`references/herdr.md`](references/herdr.md), [`references/watchers.md`](references/watchers.md), [`references/git-and-github.md`](references/git-and-github.md), [`references/contracts.md`](references/contracts.md) and the harness files above.

## Arguments

Invocation: `/herdsman [subcommand] [options]`. Received: `$ARGUMENTS`

`<skill>` below is this skill's folder: `${CLAUDE_SKILL_DIR}` (a harness that does not substitute it shows the literal text; then use the folder this file was loaded from). Read the first word of the arguments. Values in quotes keep their spaces. On an unknown subcommand or option, say which word was not understood and print the `help` output.

| Subcommand | What it does |
| --- | --- |
| none, or `run <route request>` | The full route: steps 0 to 6. The text after `run`, or any text that does not start with a subcommand, is the route request (tasks, harnesses, models, rules). The interview does not ask again what the request already answers. |
| `checkpoint [action] [options]` | Manage the orchestrator checkpointer (below). |
| `status` | Run [`scripts/agent-status.sh`](scripts/agent-status.sh) for the agents in the current route's task table (`00-route.md`) and report. Changes nothing. |
| `help` | Print this table, the checkpoint actions and options, and the examples. Do nothing else. |

### checkpoint

How the requests and the context numbers read: [`references/checkpointer.md`](references/checkpointer.md).

Actions (default `start`):

- `start`: create the checkpointer pane (step 3 item 10) with the options below. If a pane labelled `checkpointer` already exists (`herdr pane list`), report it and change nothing; to change its options, `stop` it first.
- `now`: one checkpoint now. Run `TARGET=<pane> /bin/bash <skill>/scripts/checkpointer.sh --once` as a background command and end the turn: the script waits until the orchestrator is idle, sends the memory prompt, waits for that turn, and compacts when the context is at or over the limit.
- `probe`: run `checkpointer.sh --probe` and report the transcript path and the context tokens. Sends nothing.
- `status`: read the last 10 lines of the `checkpointer` pane (`herdr pane read <id> --lines 10`) and report the schedule, the last checkpoint and the last context reading. Report "not running" when no such pane exists.
- `stop`: close the `checkpointer` pane (`herdr pane close <id>`). The invocation is the user's approval.

Options (environment variable for the script in brackets):

- `--pane <id>`: the orchestrator pane to watch (`TARGET`). Default: this session's pane, `$HERDR_PANE_ID`.
- `--every <minutes>`: the interval (`INTERVAL`, in seconds, so multiply by 60). Default 15. `start` only.
- `--limit <tokens>`: compact at or over this context size (`LIMIT`). Accepts `250k`, `1m` or a plain number. Default 250k.
- `--prompt "<text>"`: the memory checkpoint prompt (`PROMPT`). Default: `DEFAULT_PROMPT` in the script.
- `--compact "<command>"`: the harness's compact command (`COMPACT`). Default `/compact`.
- `--side right|down`: where the new pane goes. Default right. `start` only.

Quote values with spaces inside the `herdr pane run` command string, for example `PROMPT='/save-memory now'`.

Examples:

```
/herdsman checkpoint
/herdsman checkpoint start --every 10 --limit 300k
/herdsman checkpoint now --limit 150k
/herdsman checkpoint status
/herdsman status
/herdsman run "tasks: issues 12 and 14; implementers: <harness> <model>; reviewer: <harness> <model>"
/herdsman run "tasks: four parts of one change, one pull request; implementers: <harness> <model>; integrator: the first implementer that is free; reviewers: two <harness> <model>"
```

## 0. Preconditions

```bash
test "${HERDR_ENV:-}" = 1
```

Stop if this fails. Then, in one call: `herdr agent start --help` (kinds), `<harness> --version` for every harness in the route, `gh auth status`, and the git remote of the target repository. Read [`references/herdr.md`](references/herdr.md) for the command surface.

## 1. One interview, then a dispatch plan

Ask once, in one question round, everything the route needs. Do not ask again mid-route unless a step is unsafe without an answer.

- Tasks: the list, one implementer per task. Each task has a branch name and a base branch.
- Integration: one pull request per task (the implementers publish), or one combined branch (an integrator publishes). For a combined branch: the merge order, the target branch, and whether the integrator is a new agent or an implementer whose task was accepted.
- Worktrees: "own worktree per task" (default) or "existing checkout". Worktree root, for example `<repo parent>/worktrees/<letter>`. The integrator gets its own worktree too.
- Push policy: push and open a READY pull request, or draft, or push only, or ship reviewed commits straight to the default branch and a deploy branch ([`references/shipping.md`](references/shipping.md)). For the last one: who pushes when an agent's guard refuses the push, and how the deploy branch follows.
- Test infrastructure: local database or services the agents may start (container, port, credentials), one throwaway database per task and per agent.
- Implementer harness, model and effort; the same for the integrator in an integration route. Default: the user's recorded preference; with none, ask.
- Reviewer harness, model and effort, and the review skills (for example a React review skill for React files and a code-quality skill for TypeScript, plus any security or domain skill the user names). Default: the user's recorded preference; with none, ask.
- Severity gate: report P0 and P1 only (default).
- Optional techniques ([`references/optional-techniques.md`](references/optional-techniques.md)): plan gate, contract pre-review, progress file, locked proof tests, visual check, guide feedback. Default: none. Offer the ones that fit the route's size and risk, with their cost.
- Merge policy: the user merges, or the orchestrator squash-merges on double approval (local pass plus bot approval on the same head). Whether the remote branch is deleted after a merge.
- Context policy (user rule): every implementer, integrator and reviewer is compacted (when a follow-up of its own work comes next) or cleared (for a new task) right after it finishes, with its harness's commands; when compaction is refused, clear. Never mid-turn.
- Orchestrator checkpointer (user rule): a pane named `checkpointer` running [`scripts/checkpointer.sh`](scripts/checkpointer.sh) against the orchestrator pane (step 4).
- GitHub account for `gh` when the default account cannot see the repository (`GH_TOKEN=$(gh auth token --user <account>)`).

Then present a dispatch plan in the reply (tasks, branches, worktrees, panes, harnesses, models, report paths, what will be created on disk, in Herdr and on GitHub) and wait for the user's go. Every Herdr tab, container start, contract file and agent launch is a mutation that needs that go. Do not launch anything before it.

## 2. Route folder and contracts

Create the route folder at `<writable root>/.herdsman/<route>-<date>/` (user rule: every route artifact, contracts, reports and reviews, lives in a `.herdsman` folder, and `.herdsman/` is in the project's `.gitignore`; add the line if it is missing). Write every file with the harness's file tools. Files:

- `00-route.md`: decisions from the interview, task table (task, branch, worktree, agent name, pane), and a dated run-state list the orchestrator appends to. It also holds a "Must prove" list taken from the route's goal: the product paths the goal requires to work for real (which harnesses, through which execution path, with which logins, which deploy, which end-to-end checks). Every contract and the reviewer contract quote it.
- `10-shared-rules.md`: setup, code standard, tests, commit, push, pull-request body rules, review-bot loop, report format. Template in [`references/contracts.md`](references/contracts.md).
- `1n-task-<x>.md`: one contract per task: problem, scope, out of scope, acceptance, branch, worktree, test database, report path.
- `1n-task-i.md` (integration route): the integrator contract, one section per phase: combine (merge order, handoffs to apply), final-review fixes, add a late task, push (fetch, stop if the remote branch moved, fast-forward only, one reply per review thread, review-bot loop). Each phase starts only on an orchestrator prompt. Template in [`references/contracts.md`](references/contracts.md).
- `20-reviewer.md`: the reviewer contract: setup, per-review steps, severity definitions, report format. Template in [`references/contracts.md`](references/contracts.md).

Report files must live where every agent can write. A sandboxed harness writes only under its writable root, which is why the `.herdsman` folder sits under it. Use `<route folder>/reports/<task>.md` and `<route folder>/reports/review-<pr>-<n>.md`.

## 3. Launch, one Herdr mutation per call

Order: tab, panes, agents, prompts. Never two Herdr mutations in one shell call or in parallel tool calls. Read each result before the next call. Details and the verified command forms in [`references/herdr.md`](references/herdr.md).

1. Implementer tab: `herdr tab create --workspace <w> --cwd <writable root> --label "Implementer Agents" --no-focus`. Note `root_pane.pane_id`.
2. Panes: for four implementers, split the root pane right, then each column down (2 by 2). One split per call. Note every pane id from the JSON.
3. Reviewer tab: `herdr tab create --workspace <w> --cwd <writable root> --label "Reviewer" --no-focus`.
4. Implementer agents, one per call, with the launch flags from the harness notes:
   `herdr agent start im-<x> --kind <kind> --pane <id> --timeout 90000 -- <model, effort, sandbox and workspace flags>`
   When a safety classifier refuses a sandbox-off launch, do not retry: give the user the exact launch lines to run.
5. Reviewer agent, the same way: `herdr agent start rv-1 --kind <kind> --pane <id> --timeout 90000 -- <model, effort, permission and extra-folder flags>`. The reviewer needs read access to the route folder and the scratch folder.
6. Prompts, one per call. A harness with a goal mode (for example `/goal`) takes the brief as a goal; others take plain text:
   `herdr agent prompt im-<x> "<goal prefix if any>Read <shared rules> and then <task file>. Do task <X> exactly as those two files specify: worktree, branch, tests first, commit, push, open a ready pull request, follow the review-bot loop, then write the report file and reply with only its path."` In an integration route the implementer commits to its local branch only: no push, no pull request.
   The reviewer is prompted only when a pull request or branch is ready for review.
   Time box every prompt, first tasks and fix rounds alike: pick the minutes from the task size, run `date` first, compute the due time (`date -v+<N>M +%s` on macOS, `date -d "+<N> min" +%s` with GNU date), write `Due: <HH:MM>` in the brief, append the launch and the due time to `00-route.md`, and pass `HERDSMAN_DUE_im_<x>=<epoch>` to the waiter until that agent reports (step 4). Drop the variable once the report arrives.
7. Integrator (integration route): start it when the first branches have passed review, not at launch. Either start a new agent (`herdr agent start int-1 ...`) or clear an implementer whose task was accepted (it keeps its Herdr name; record the new job in the task table). Prompt one phase at a time: `<goal prefix if any>Read <shared rules> and then <integrator contract>. Do phase <n> exactly as specified, then update the report file and reply with only its path.` Give each phase a due time like any prompt, and pass the name to the waiter as `HERDSMAN_INTEGRATOR` with its report name (`HERDSMAN_TASK_int_1=i`).
8. Verify each pane's text after its prompt (`herdr pane read <id> --lines 30`): the prompt is echoed and the harness's working marker shows (see its notes).
9. Every prompt is a full brief, never a pointer (user rule): end-state goal the user would see, numbered steps with files, a Check per step, a leave-alone list, hard rules and time limits, the report shape.
10. Checkpointer pane (`checkpoint start`; one split, one rename, one run): `herdr pane split <orchestrator pane> --direction right --ratio 0.7 --no-focus`, `herdr pane rename <new> checkpointer`, `herdr pane run <new> "TARGET=<orchestrator pane> /bin/bash <skill>/scripts/checkpointer.sh"` (prefix `INTERVAL=`, `LIMIT=`, `PROMPT=`, `COMPACT=` from the options). Probe first with `--probe` (prints the transcript and context tokens, sends nothing).

## 4. Wait for events with a background command that exits

In-session monitors do not wake the orchestrator between turns: their events arrive only while a turn runs or when the user writes. A background shell command that exits does wake it. So the loop's clock is [`scripts/wait-event.sh`](scripts/wait-event.sh), run as a background command with a 10-minute timeout:

```
HERDSMAN_REPORTS=... HERDSMAN_STATE=... HERDSMAN_IMPLEMENTERS="im-a im-b" HERDSMAN_PANE_im_a=... HERDSMAN_DUE_im_a=<epoch> HERDSMAN_INTEGRATOR=int-1 HERDSMAN_TASK_int_1=i HERDSMAN_PANE_int_1=... HERDSMAN_REVIEWERS="rv-1:<pane>" /bin/bash <skill>/scripts/wait-event.sh
```

The integrator is watched like an implementer: its report file, its pane and its due time. It polls every 60 s and exits on the first event pass, or after 540 s with `TICK`. On every wake: handle the events, then re-arm the same command. Events: REPORT (a task report file appeared), REPORT-UPDATED (the report changed after a fix round), REVIEW (a new review file), BLOCKED (password, approval, model-switch, usage-limit or queued-question text; once per screen change), GOAL-DONE-NO-REPORT (the harness shows its goal-finished marker but no report exists), STALL (working marker but unchanged screen for three polls), OVERDUE (an implementer's or the integrator's `HERDSMAN_DUE_<name>` has passed and its report was not written or changed since that due time was set; once per due time). [`references/watchers.md`](references/watchers.md) has the variables, the bash 3 constraint and the sanity test in [`scripts/test/run-tests.sh`](scripts/test/run-tests.sh). The two monitor scripts in `scripts/` remain for use inside a long running turn only.

On every wake, also run [`scripts/agent-status.sh`](scripts/agent-status.sh) (`HERDSMAN_STATUS_SPECS="name:pane:tree ..."`). User rule: check every agent every ten minutes and do not let background jobs run long. Every implementer and the integrator already has a due time from its prompt (step 3); a job flagged over 30 minutes, or a tree with no writes in 10 minutes, gets an earlier one if its due time is further out. Past the due time (the waiter's OVERDUE event), steer or interrupt. The orchestrator enforces deadlines, not the agent: agents ignored stop times written in their own briefs. At the deadline queue a stop-and-report steer (cancel every run, start none, write the report from existing results with `STATUS: gaps`, reply with the path); a queued steer lands only when the current turn ends, so with no report within about 10 minutes send the harness's interrupt key (usually `esc`): the turn ends and the queued steer runs next (re-prompt only if nothing was queued). Test logs written outside the tree (for example under a temp folder) show as "NO WRITES"; check that folder before calling an agent hung. Any agent idle since its finish and not yet compacted or cleared is an open action.

The checkpointer ([`scripts/checkpointer.sh`](scripts/checkpointer.sh), in its own pane) bounds the orchestrator's own context: every 15 minutes, if the orchestrator's transcript grew, it waits for idle (never blocked), sends the memory checkpoint prompt (set at the top of the script), waits for that turn, reads the context size from the orchestrator's session transcript (input plus cache tokens of the last main-thread turn) and sends the compact command at 250K tokens or more. The included transcript reader expects JSONL with a usage block per turn; for another format, replace `transcript()` and `context_tokens()`. Keep memory and the route log current so each compaction loses nothing. A background event that wakes the orchestrator between the idle check and the prompt only delays the prompt (the harness queues typed input).

React to each event:

- BLOCKED with a password prompt: prompt the agent with the credential-helper form ([`references/git-and-github.md`](references/git-and-github.md)). Never type a password.
- BLOCKED with a model-switch offer: keep the confirmed model (harness notes).
- BLOCKED with a usage limit: tell the user; never fall back to an API key.
- STALL twice in a row: read the pane in full, then prompt the agent with the exact next step.
- OVERDUE: run [`agent-status.sh`](scripts/agent-status.sh) for that agent. If it is still writing and close, give one stated extension (new `HERDSMAN_DUE_<name>`, logged in `00-route.md`); otherwise queue the stop-and-report steer and follow it with the interrupt key as described above. Never extend twice.
- REPORT from an implementer: read the report, check `STATUS:`, check the pull request (`gh pr view <n> --json headRefOid,mergeable,statusCheckRollup`) or, in an integration route, the local branch head, then hand it to the reviewer (step 5).
- REPORT or REPORT-UPDATED from the integrator: check that its turn ended and that the combined head is new, then start the next step of step 5 (final review, next phase or push).

## 5. Review loop

For each task result with `STATUS: complete` (a pull request with green checks, or a local branch in an integration route):

1. Context first, one rule for every agent: a follow-up of the agent's own earlier work (a re-review of the same pull request, a fix round) keeps its context, compacted as soon as its previous turn finished; a completely new task (a pull request the reviewer has not seen, a new contract for an implementer, the integrator job) starts from a cleared context. Use the harness's compact and clear commands and read the pane after the command. Then prompt the reviewer: `Read <route>/20-reviewer.md and follow it exactly. Review pull request <n> (branch <b>, contract <task file>). Report path: <route folder>/reports/review-<n>-<k>.md. Reply with only that path when done.` For a local branch, name the branch and its head commit instead of the pull request.
2. On REVIEW, read the file. `VERDICT: pass` closes the loop for that pull request or branch; record it and tell the user.
3. `VERDICT: fix`: goal gate first. Check each finding's fix line against the Must-prove list. If a fix would skip, disable, mock or gate a Must-prove path, or needs a credential, token, API key or login the user does not already use, do not forward it: decide by the goal and tell the reviewer why the finding is overruled, or ask the user. A `GOAL-CONFLICT` line goes to the user with both options. Then prompt the task's implementer with the rest: `<goal prefix if any>Read <review file>. Fix every P0 and P1 finding in it on your branch: failing test first, then the code, run the suites, commit, push, reply on any review-bot threads, then update your report file (new head, what changed per finding under decisions:) and reply with only its path. If you disagree with a finding, write the reason under decisions: and still make the smallest change that satisfies its fix line.` (In an integration route: commit only, no push and no threads.) If the prompt answers `agent_blocked`, clear the queued question first (harness notes). Then wait for the implementer's REPORT-UPDATED, then send the reviewer the new head as `review-<n>-<k+1>`. A finding whose fix spans two tasks' files goes into the integrator contract as a handoff ([`references/ownership-and-integration.md`](references/ownership-and-integration.md)).
4. Repeat until pass. A disagreement goes to the user at once: when an implementer disagreed with a finding (under decisions:) and the reviewer raises it again, the user decides before another fix round starts. After three fix rounds on one pull request or branch, stop and show the user the remaining findings.
5. Integration route: when the branches have passed, prompt the integrator with the combine phase. On its report, run the final review of the combined head against the base branch (`review-<pr>-<k>`), in parallel with any late test-only task. Send the final review's findings to the integrator as a fix phase on the combined branch, test first. Add a late task in its own phase, then review the new head again. Only a passing final review starts the push phase. Branch reviews miss defects on paths between tasks, so the final review is never skipped ([`references/review-discipline.md`](references/review-discipline.md)).
6. The reviewer posts nothing on GitHub and pushes nothing. Merges are the user's unless authorized; the authorized form is a squash merge through the asynchronous merge API, pinned to the reviewed head and polled until `merged` or `failed` (an `enqueued` result ends the polling; confirm the merge separately once the queue lands it), when the local verdict is pass AND the bot approval is green on the same head ([`references/git-and-github.md`](references/git-and-github.md), Merges). A stacked pull request merges with every open pull request below it, so each of them must pass first. Before the first merge read the base branch's rules (`gh api repos/<org>/<repo>/rules/branches/<base>`): a required second approval that the author's account cannot give needs the user's choice (admin bypass, a colleague's approval, or a rule change); never bypass on your own. After a merge, when the user asked for it, delete the remote branch if the pull request is MERGED and no open pull request is based on it; keep the local worktree until route close.
7. Parallel implementers that generate database migrations from the same base produce the same migration numbers. Before merging the second such pull request, send its implementer a fix round: rebase onto the new main, regenerate the migration history, run the suites, push. Say this in the shared rules up front when more than one task touches the schema.

A reviewer is one persistent agent; send it one review at a time. If reviews queue up, ask the user before starting a second reviewer pane.

When commits are not allowed, review tree snapshots instead of pull requests ([`references/git-and-github.md`](references/git-and-github.md), No-commit routes).

## 6. Record and close

- Append a run-state line to `00-route.md` at every event that changes state: launch, blocker, report, review verdict, fix round, pass. Use `scripts/route-log.sh <route>/00-route.md "<text>"` (it writes `- HH:MM <text>` from `date`), never a typed time.
- Keep the orchestrator context small: read report files and review files, not pane transcripts, except to unblock.
- At close: table of pull requests (number, branch, head, checks, review verdict, rounds), open items, and the exact things the user still owns (merges, secrets, infrastructure). Write the lessons of the route into memory per the user's memory conventions. When a lesson is general, write it into this skill as a rule with its reason: in the concept file of its topic (list above), or in the tool or harness notes when it is a command quirk; merge it with an older rule on the same point instead of adding a second one. A new concept gets a new file and a line in the Concepts list. No dates, route stories, counts, durations, versions or project names in the skill: those stay in memory.

## Rules that do not bend

- The orchestrator never edits application code, never runs the application's tests itself, never pushes. In a route with one pull request per task the implementers push; in an integration route only the integrator pushes. One exception: when an agent's guard refuses a push to the default branch, the user can grant the orchestrator the push of reviewed commits for that route ([`references/shipping.md`](references/shipping.md), Who pushes).
- Never touch git identity, remotes or global git config, in any pane. Push with the one-shot credential helper only.
- No secrets in contracts, reports or pull-request bodies. No people's names or tool names in pull-request bodies.
- One Herdr mutation per call. Ask before every step that changes Herdr layout, containers, GitHub or cloud state, unless the user gave that exact step its go in the dispatch plan.
- Write files with the harness's file tools, never with heredocs or redirects.
- Read `date` before writing any timestamp.
- Never propose that the user add a new credential, token or API key to make a test or run work; the route uses the user's existing logins.
- A report whose required live test skipped is not complete. Do not send it to review. Send it back to its author to run the test.
- When an agent reports that a permission classifier blocked an environment fix, verify the cause read-only, then give the user the exact command (on the clipboard with `pbcopy` when asked). Never run it past the classifier yourself.
