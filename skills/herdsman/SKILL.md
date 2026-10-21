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

Read [`references/harness-guards.md`](references/harness-guards.md) before the first shell command. Each step below starts with a "Read first" line: read those files before the step's first action. The route checklist ([`references/checklist.md`](references/checklist.md)) turns the steps into lines that the route file keeps, so that work still open stays visible after the orchestrator's context is compacted; [`scripts/route-check.sh`](scripts/route-check.sh) prints those lines and checks what the route folder shows. Reason: a route outlives the orchestrator's context, and the rules that were read at the start are the ones missed later.

Harness notes: `references/` has one file per harness used so far (launch form, prompt form, slash commands for compact and clear, working and blocked markers, quirks): [`codex.md`](references/codex.md), [`muse.md`](references/muse.md), [`claude-code.md`](references/claude-code.md) and [`agy.md`](references/agy.md). The notes are per harness, not per agent type, because any harness can take any type. Before a route uses a harness with no notes file, probe it (launch in a scratch pane, send a prompt, find its compact, clear and interrupt commands and its working marker) and write the notes file first.

Concepts: the rules that earlier routes taught, with their reasons, one file per concept. Read them before the dispatch plan (step 1), and again when a step touches the concept. With the skill navigator ([`references/optional-techniques.md`](references/optional-techniques.md)), the sections its map names stand in for these full reads, step by step: a rule the map leaves out still binds, and an event or step the map does not cover sends you back to the full files.

- [`references/flow-and-time.md`](references/flow-and-time.md): due times that the orchestrator enforces, the moving bottleneck (reviewers, CI, the integrator), work ahead, idle state and restarts, quota as capacity.
- [`references/context-hygiene.md`](references/context-hygiene.md): compact for a follow-up, clear for a new job, when harnesses refuse the commands, the orchestrator's own context.
- [`references/route-close.md`](references/route-close.md): closing agents, sessions and worktrees without losing work, resume data, disk space.
- [`references/ownership-and-integration.md`](references/ownership-and-integration.md): file and line ownership, handoffs, the integrator's phases, fixes on another author's pull request, migration numbers and stacks.
- [`references/review-discipline.md`](references/review-discipline.md): what a review reports and proves, the goal gate, orchestrator amendments, disagreements, human reviews, review-only routes on someone else's pull request (brief, snapshot, severity rulings, posting one inline review).
- [`references/shared-environment.md`](references/shared-environment.md): test databases, CPU and shared memory on one machine, tests that prove the product.
- [`references/shipping.md`](references/shipping.md): routes that push reviewed commits straight to the default branch: the ship gate, who pushes, release by hand, acceptance on the deployed app, rollback, the user's browser.
- [`references/checkpointer.md`](references/checkpointer.md): the checkpointer requests and numbers (see `checkpoint` below).
- [`references/optional-techniques.md`](references/optional-techniques.md): techniques that are off unless the user picks them: skill navigator, plan gate, contract pre-review, progress file, locked proof tests, visual check, guide feedback, without agent panes.

Tool notes: [`references/checklist.md`](references/checklist.md), [`references/herdr.md`](references/herdr.md), [`references/watchers.md`](references/watchers.md), [`references/git-and-github.md`](references/git-and-github.md), [`references/contracts.md`](references/contracts.md) and the harness files above.

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
- `now`: one checkpoint now. Run `TARGET=<pane> /bin/bash <skill>/scripts/checkpointer.sh --once` as a background command and end the turn: the script waits until the orchestrator is idle, sends the memory prompt, waits for that turn, and compacts when the context is at or over the limit. When background work keeps the orchestrator pane working, the script never sends its prompt, because it waits for an idle pane with no time limit: write the memory checkpoint in the current turn instead, stop the `--once` script by its task id (it would send a second prompt when the pane turns idle), and send the compact command yourself when the context is over the limit.
- `probe`: run `checkpointer.sh --probe` and report the transcript path, why the script chose it, and the context tokens. A stale session id shows as a hint (a newer transcript of the same folder); `TRANSCRIPT=<path>` overrides it. Sends nothing. Run it after every restart.
- `status`: read the last 10 lines of the `checkpointer` pane (`herdr pane read <id> --lines 10`) and report the schedule, the last checkpoint and the last context reading. Report "not running" when no such pane exists.
- `stop`: close the `checkpointer` pane (`herdr pane close <id>`). The invocation is the user's approval.

Options (environment variable for the script in brackets):

- `--pane <id>`: the orchestrator pane to watch (`TARGET`). Default: this session's pane, `$HERDR_PANE_ID`.
- `--every <minutes>`: the interval (`INTERVAL`, in seconds, so multiply by 60). Default 15. `start` only.
- `--limit <tokens>`: compact at or over this context size (`LIMIT`). Accepts `250k`, `1m` or a plain number. Default 250k.
- `--prompt "<text>"`: the memory checkpoint prompt (`PROMPT`). Default: `DEFAULT_PROMPT` in the script.
- `--compact "<command>"`: the harness's compact command (`COMPACT`). Default `/compact`.
- `--transcript <path>`: the orchestrator's transcript file (`TRANSCRIPT`). Default: the file of the session id that Herdr reports. Set it when `probe` shows that the id is stale ([`references/checkpointer.md`](references/checkpointer.md)).
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

Read first: [`references/harness-guards.md`](references/harness-guards.md), [`references/herdr.md`](references/herdr.md).

```bash
test "${HERDR_ENV:-}" = 1
```

Stop if this fails. Then, in one call: `herdr agent start --help` (kinds), `<harness> --version` for every harness in the route, `gh auth status`, and the git remote of the target repository.

## 1. One interview, then a dispatch plan

Read first: every concept file in the Concepts list and the notes of each harness in the route, before the dispatch plan, or the sections the skill navigator's map names (References above).

Ask once, in one question round, through the ask tool when the harness has one ([`references/harness-guards.md`](references/harness-guards.md), Orchestrator posture), everything the route needs. Do not ask again mid-route unless a step is unsafe without an answer. A product default that changes what users will see (which records a cleanup hides, what stays listed) is a question in this round, never a "may overrule" note in the contract. Reason: the user finds a wrong default only after delivery, and then a late follow-up pull request must fix it.

- Tasks: the list, one implementer per task. Each task has a branch name and a base branch.
- Integration: one pull request per task (the implementers publish), or one combined branch (an integrator publishes). For a combined branch: the merge order, the target branch, and whether the integrator is a new agent or an implementer whose task was accepted.
- Worktrees: "own worktree per task" (default) or "existing checkout". Worktree root, for example `<repo parent>/worktrees/<task id>`. The integrator gets its own worktree too. Name each task by a short id that describes it (for example `stack-landing`), not only by a letter, and use that one id for the agent, the worktree, the test database and the report file, because a descriptive id shows what the task is in every pane, path and log. Keep it short: an agent name has at most 32 characters.
- Push policy: push and open a READY pull request, or draft, or push only, or ship reviewed commits straight to the default branch and a deploy branch ([`references/shipping.md`](references/shipping.md)). For the last one: who pushes when an agent's guard refuses the push, and how the deploy branch follows. For a route with one pull request per task, a two-gate option: the implementer commits locally, the reviewers pass the local head, then the pull request opens and the same reviewers pass it, because a task that is reviewed only after its pull request opens was not checked before it was published.
- Test infrastructure: local database or services the agents may start (container, port, credentials), one throwaway database per task and per agent.
- Implementer harness, model and effort; the same for the integrator in an integration route. Default: the user's recorded preference; with none, ask.
- Reviewer harness, model and effort, and the review skills (for example a React review skill for React files and a code-quality skill for TypeScript, plus any security or domain skill the user names). Default: the user's recorded preference; with none, ask.
- Severity gate: report P0 and P1 only (default).
- Optional techniques ([`references/optional-techniques.md`](references/optional-techniques.md)): skill navigator, plan gate, contract pre-review, progress file, locked proof tests, visual check, guide feedback, without agent panes. Default: none. Always offer the skill navigator; offer the others that fit the route's size and risk, with their cost.
- Merge policy: the user merges, or the orchestrator squash-merges on double approval (local pass plus bot approval on the same head). Whether the remote branch is deleted after a merge.
- Context policy (user rule): every implementer, integrator and reviewer is compacted (when a follow-up of its own work comes next) or cleared (for a new task) right after it finishes, with its harness's commands; when compaction is refused, clear. Never mid-turn. Near the usage limit, skip the compaction ([`references/flow-and-time.md`](references/flow-and-time.md), Quota is capacity).
- Orchestrator checkpointer (user rule): a pane named `checkpointer` running [`scripts/checkpointer.sh`](scripts/checkpointer.sh) against the orchestrator pane (step 4).
- GitHub account for `gh` when the default account cannot see the repository (`GH_TOKEN=$(gh auth token --user <account>)`).

Copy each chosen option's full text into `00-route.md` (step 2): later steps, such as a push that opens a new pull request, rest on its exact words. Then present a dispatch plan in the reply (tasks, branches, worktrees, panes, harnesses, models, report paths, what will be created on disk, in Herdr and on GitHub), then ask for the user's go, through the ask tool when the harness has one, with the go, a change and a stop as options, and wait for the answer. Every Herdr tab, container start, contract file and agent launch is a mutation that needs that go. Do not launch anything before it. One exception: the skill navigator, whose go is the interview answer that picks it; it reads the skill, returns its map in its reply and writes nothing. The plan also names the answer to the folder-trust dialog (and any tool-server dialog) of each new folder, because the answer grants access to the user's folder; the user's go then covers it ([`references/claude-code.md`](references/claude-code.md), [`references/codex.md`](references/codex.md)).

## 2. Route folder and contracts

Read first: [`references/contracts.md`](references/contracts.md), [`references/shared-environment.md`](references/shared-environment.md), [`references/checklist.md`](references/checklist.md).

Create the route folder at `<writable root>/.herdsman/<route>-<date>/` (user rule: every route artifact, contracts, reports and reviews, lives in a `.herdsman` folder, and `.herdsman/` is in the project's `.gitignore`; add the line if it is missing). Write every file with the harness's file tools. Files:

- `00-route.md`: decisions from the interview, task table (task, branch, worktree, agent name, pane), and a dated run-state list the orchestrator appends to. It also holds a "Must prove" list taken from the route's goal: the product paths the goal requires to work for real (which harnesses, through which execution path, with which logins, which deploy, which end-to-end checks). Every contract and the reviewer contract quote it. It also holds a `## Checklist` section: the lines of [`references/checklist.md`](references/checklist.md) that fit the route, ticked as the route goes.
- `10-shared-rules.md`: setup, code standard, tests, commit, push, pull-request body rules, review-bot loop, report format. Template in [`references/contracts.md`](references/contracts.md).
- `1n-task-<x>.md`: one contract per task: problem, scope, out of scope, acceptance, branch, worktree, test database, report path.
- `1n-task-i.md` (integration route): the integrator contract, one section per phase: combine (merge order, handoffs to apply), final-review fixes, add a late task, push (fetch, stop if the remote branch moved, fast-forward only, one reply per review thread, review-bot loop). Each phase starts only on an orchestrator prompt. Template in [`references/contracts.md`](references/contracts.md).
- `20-reviewer.md`: the reviewer contract: setup, per-review steps, severity definitions, report format. Template in [`references/contracts.md`](references/contracts.md).
- `run-check.sh`: the check wrapper that every contract names (one check at a time on the machine, behind a load gate). Write it from the template in [`references/shared-environment.md`](references/shared-environment.md), make it executable, and test it before any agent launches.

Report files must live where every agent can write. A sandboxed harness writes only under its writable root, which is why the `.herdsman` folder sits under it. Use `<route folder>/reports/<task>.md` and `<route folder>/reports/review-<pr>-<n>.md`.

Everything that agents or their tools must read lives in the route folder. Copy outside documents (specs, guides, design images) into `<route folder>/ref/` before launch, and keep probe files and tool scripts in `<route folder>/probes/`, never in a temp folder. Reason: agents and tools read only inside their granted folders, and the system purges temp folders, so a review can no longer be reproduced.

## 3. Launch, one Herdr mutation per call

Read first: [`references/herdr.md`](references/herdr.md) and the notes of each harness in the route. Before the first launch, run `/bin/bash <skill>/scripts/route-check.sh <route folder>` and fix every MISSING and BAD line first; once the task table names every pane, run it with `launch`.

Order: tab, panes, agents, prompts. Never two Herdr mutations in one shell call or in parallel tool calls. Read each result before the next call. Details and the verified command forms in [`references/herdr.md`](references/herdr.md).

Start every agent in a Herdr pane through `herdr agent start`, also when the user does not say Herdr. Never run a harness as a hidden background process (for example `codex exec`), because the user cannot see or steer it. The one exception is the user's pick of [Without agent panes](references/optional-techniques.md). The skill navigator is not a route agent: it only reads the skill, so it runs as a subagent of the orchestrator's harness.

1. Implementer tab: `herdr tab create --workspace <w> --cwd <writable root> --label "<project>-implementers" --no-focus`. Note `root_pane.pane_id`.
2. Panes: for four implementers, split the root pane right, then each column down (2 by 2). One split per call. Note every pane id from the JSON. Label each pane with `herdr pane rename <id> im-<task>`, one call each, and label the orchestrator's own tab and pane too ([`references/herdr.md`](references/herdr.md), Etiquette).
3. Reviewer tab: `herdr tab create --workspace <w> --cwd <writable root> --label "<project>-reviewers" --no-focus`; label its panes `reviewer-<harness>`.
4. Implementer agents, one per call, with the launch flags from the harness notes:
   `herdr agent start im-<x> --kind <kind> --pane <id> --timeout 90000 -- <model, effort, sandbox and workspace flags>`
   When a safety classifier refuses a sandbox-off launch, do not retry: give the user the exact launch lines to run.
5. Reviewer agent, the same way: `herdr agent start rv-1 --kind <kind> --pane <id> --timeout 90000 -- <model, effort, permission and extra-folder flags>`. The reviewer needs read access to the route folder and the scratch folder.
6. Prompts, one per call. A harness with a goal mode (for example `/goal`) takes the brief as a goal; others take plain text:
   `herdr agent prompt im-<x> "<goal prefix if any>End state: <end-state goal>. Read <shared rules> and then <task file>. Do task <X> exactly as those two files specify: worktree, branch, tests first, commit, push, open a ready pull request, follow the review-bot loop, then write the report file and reply with only its path."` In an integration route, and at the first gate of a two-gate route, the implementer commits to its local branch only: no push, no pull request.
   The reviewer is prompted only when a pull request or branch is ready for review, with one exception in a route with implementers: its first job, the base gates ([`references/shared-environment.md`](references/shared-environment.md), Facts, not guesses), prompted together with the implementers' first prompts, with a due time and the report path `<route folder>/reports/review-base-1.md`.
   Time box every prompt, first tasks and fix rounds alike: pick the minutes from the task size, run `date` first, compute the due time (`date -v+<N>M +%s` on macOS, `date -d "+<N> min" +%s` with GNU date), write `Due: <HH:MM>` in the brief, append the launch and the due time to `00-route.md`, and pass `HERDSMAN_DUE_im_<x>=<epoch>` to the waiter until that agent reports (step 4). Drop the variable once the report arrives.
7. Integrator (integration route): start it when the first branches have passed review, not at launch; when the branches own disjoint files, it may combine while their reviews run, and each later fix round comes in through a re-merge phase ([`references/flow-and-time.md`](references/flow-and-time.md), Work ahead). Either start a new agent (`herdr agent start int-1 ...`) or clear an implementer whose task was accepted (it keeps its Herdr name; record the new job in the task table). Prompt one phase at a time: `<goal prefix if any>End state: <end-state goal>. Read <shared rules> and then <integrator contract>. Do phase <n> exactly as specified, then update the report file and reply with only its path.` Give each phase a due time like any prompt, and pass the name to the waiter as `HERDSMAN_INTEGRATOR` with its report name (`HERDSMAN_TASK_int_1=i`).
8. Verify each pane's text after its prompt (`herdr pane read <id> --lines 40`; read at least 30 lines, because a pinned tip menu can push the echo out of a short read): the prompt is echoed and the harness's working marker shows (see its notes). When your own text sits unsent in an idle input (for example `[Pasted text #N]`), send `enter` once. After a timeout or an error from Herdr, read the pane before you send the prompt again, because the prompt may have arrived and a second send duplicates it.
9. Every prompt is a full brief, never a bare pointer (user rule): it names the end-state goal the user would see (the `<end-state goal>` slot above), the files to read, the hard rules and time limits, and the report shape. The contract file that it names holds the numbered steps, a Check per step and the leave-alone list. Reason: the pane then shows what the agent is to do, and the long steps stay in one file that the orchestrator can amend.
10. Checkpointer pane (`checkpoint start`; one split, one rename, one run): `herdr pane split --pane <orchestrator pane> --direction right --cwd <orchestrator's working folder> --no-focus` (when the session id has no file, `--probe` names the newest transcript of that folder as a hint: [`references/checkpointer.md`](references/checkpointer.md)), `herdr pane rename <new> checkpointer`, `herdr pane run <new> "TARGET=<orchestrator pane> /bin/bash <skill>/scripts/checkpointer.sh"` (prefix `INTERVAL=`, `LIMIT=`, `PROMPT=`, `COMPACT=`, `TRANSCRIPT=` from the options). Probe first with `--probe` (prints the transcript and context tokens, sends nothing).

## 4. Wait for events with a background command that exits

Read first: [`references/watchers.md`](references/watchers.md), [`references/flow-and-time.md`](references/flow-and-time.md).

On every wake, and before any other action after a compaction, read the open lines of the route checklist (`/bin/bash <skill>/scripts/route-check.sh <route folder> launch`).

In-session monitors do not wake the orchestrator between turns: their events arrive only while a turn runs or when the user writes. A background shell command that exits does wake it. So the loop's clock is [`scripts/wait-event.sh`](scripts/wait-event.sh), run as a background command with a 30-minute timeout (`1800000`):

```
HERDSMAN_REPORTS=... HERDSMAN_STATE=... HERDSMAN_IMPLEMENTERS="im-a im-b" HERDSMAN_PANE_im_a=... HERDSMAN_DUE_im_a=<epoch> HERDSMAN_INTEGRATOR=int-1 HERDSMAN_TASK_int_1=i HERDSMAN_PANE_int_1=... HERDSMAN_REVIEWERS="rv-1:<pane>" HERDSMAN_STATUS_SPECS="im-a:<pane>:<worktree> ..." /bin/bash <skill>/scripts/wait-event.sh
```

The integrator is watched like an implementer: its report file, its pane and its due time. It polls every 60 s and exits on the first event pass, or after 1500 s with `TICK`. Keep `DEADLINE` below the harness's limit for background commands: each `TICK` is a whole orchestrator turn, so a short deadline costs turns and context without seeing more. On every wake: handle the events, then re-arm the same command. Events: REPORT (a task report file appeared), REPORT-UPDATED (the report changed after a fix round), REVIEW (a new review file), BLOCKED (password, approval, model-switch, usage-limit, queued-question or stalled-goal text; once per screen change; stalled-goal text counts only where no working marker shows), GOAL-DONE-NO-REPORT (the harness shows its goal-finished marker but no report exists), STALL (working marker but unchanged screen for three polls), HEALTH (every `HERDSMAN_HEALTH_EVERY` seconds, default 300, the waiter runs [`scripts/agent-status.sh`](scripts/agent-status.sh) with `HERDSMAN_STATUS_SPECS` and reports each flag once until it clears: a job over 30 minutes, a tree with no writes in 10 minutes, a machine load above its core count, free memory under 10%), OVERDUE (an implementer's or the integrator's `HERDSMAN_DUE_<name>` has passed and its report was not written or changed since that due time was set; once per due time). [`references/watchers.md`](references/watchers.md) has the variables, the bash 3 constraint and the sanity test in [`scripts/test/run-tests.sh`](scripts/test/run-tests.sh). The two monitor scripts in `scripts/` remain for use inside a long running turn only.

On every wake, also run [`scripts/agent-status.sh`](scripts/agent-status.sh) (`HERDSMAN_STATUS_SPECS="name:pane:tree ..."`). User rule: check every agent every five minutes and do not let background jobs run long. The waiter runs that check every five minutes between wakes and wakes the orchestrator only for a new flag (HEALTH). Also read each agent's pane at once after each prompt and after each waiter re-arm (step 3, item 8), because a check that waits for a report or a tick can starve for a long time. Every implementer and the integrator already has a due time from its prompt (step 3); a job flagged over 30 minutes, or a tree with no writes in 10 minutes, gets an earlier one if its due time is further out. Past the due time (the waiter's OVERDUE event), steer or interrupt. The orchestrator enforces deadlines, not the agent: agents ignored stop times written in their own briefs. At the deadline queue a stop-and-report steer (cancel every run, start none, write the report from existing results with `STATUS: gaps`, reply with the path); a queued steer lands only when the current turn ends, so with no report within about 10 minutes send the harness's interrupt key (usually `esc`): the turn ends and the queued steer runs next (re-prompt only if nothing was queued). Test logs written outside the tree (for example under a temp folder) show as "NO WRITES"; check that folder before calling an agent hung. Any agent idle since its finish and not yet compacted or cleared is an open action.

The checkpointer ([`scripts/checkpointer.sh`](scripts/checkpointer.sh), in its own pane) bounds the orchestrator's own context: every 15 minutes, if the orchestrator's transcript grew, it waits for idle (never blocked), sends the memory checkpoint prompt (set at the top of the script), waits for that turn, reads the context size from the orchestrator's session transcript (input plus cache tokens of the last main-thread turn) and sends the compact command at 250K tokens or more. The included transcript reader expects JSONL with a usage block per turn; for another format, replace `transcript()` and `context_tokens()`. Keep memory and the route log current so each compaction loses nothing. A background event that wakes the orchestrator between the idle check and the prompt only delays the prompt (the harness queues typed input).

React to each event:

- BLOCKED with a password prompt: prompt the agent with the credential-helper form ([`references/git-and-github.md`](references/git-and-github.md)). Never type a password.
- BLOCKED with a model-switch offer: keep the confirmed model (harness notes).
- BLOCKED with a usage limit: tell the user; never fall back to an API key.
- STALL twice in a row: read the pane in full, then prompt the agent with the exact next step.
- HEALTH for an agent: as for the same flag on a wake (an earlier due time, or a look at the folder its test logs go to). HEALTH for the machine: start no new heavy work; check that the route's `run-check.sh` has its load limit and that the publisher still runs ([`references/shared-environment.md`](references/shared-environment.md)).
- OVERDUE: run [`agent-status.sh`](scripts/agent-status.sh) for that agent. If it is still writing and close, give one stated extension (new `HERDSMAN_DUE_<name>`, logged in `00-route.md`); otherwise queue the stop-and-report steer and follow it with the interrupt key as described above. Never extend twice.
- REPORT from an implementer: read the report, check `STATUS:`, check the pull request (`gh pr view <n> --json headRefOid,mergeable,statusCheckRollup`) or, in an integration route, the local branch head, then hand it to the reviewer (step 5).
- REPORT or REPORT-UPDATED from the integrator: check that its turn ended and that the combined head is new, then start the next step of step 5 (final review, next phase or push).

## 5. Review loop

Read first: [`references/review-discipline.md`](references/review-discipline.md); in an integration route, [`references/ownership-and-integration.md`](references/ownership-and-integration.md); before the first merge, [`references/git-and-github.md`](references/git-and-github.md) (Merges), or [`references/shipping.md`](references/shipping.md) before the first push of a shipping route.

For each task result with `STATUS: complete` (a pull request with green checks, or a local branch in an integration route or at the first gate of a two-gate route):

1. Context first, one rule for every agent: a follow-up of the agent's own earlier work (a re-review of the same pull request, a fix round) keeps its context, compacted as soon as its previous turn finished (two exceptions: a reviewer whose last review used most of its context, [`references/context-hygiene.md`](references/context-hygiene.md); and any agent near the usage limit, where the compaction is skipped, [`references/flow-and-time.md`](references/flow-and-time.md), Quota is capacity); a completely new task (a pull request the reviewer has not seen, a new contract for an implementer, the integrator job) starts from a cleared context. Use the harness's compact and clear commands and read the pane after the command. Then prompt the reviewer: `End state: <end-state goal>. Read <route>/20-reviewer.md and follow it exactly. Review pull request <n> (branch <b>, contract <task file>). Report path: <route folder>/reports/review-<n>-<k>.md. Reply with only that path when done.` For a local branch, name the branch and its head commit instead of the pull request.
2. On REVIEW, read the file, its ENV lines first. `VERDICT: pass` with no open ENV line closes the loop for that pull request or branch; record it and tell the user. At the first gate of a two-gate route, the pass closes only that gate: prompt the implementer to push and open the pull request as the push policy says, then send the same reviewers the pull request as the second gate; the second pass closes the loop. An ENV line (a suite that the reviewer's sandbox denied, or a test database out of shared memory) stays open until its closing run passes on the same head: an agent outside that sandbox runs the suite, or the suite runs again after the database is recreated. A failed closing run turns the line into a P0 ([`references/contracts.md`](references/contracts.md), reviewer Severity).
3. `VERDICT: fix`: goal gate first. Check each finding's fix line against the Must-prove list. If a fix would skip, disable, mock or gate a Must-prove path, or needs a credential, token, API key or login the user does not already use, do not forward it: decide by the goal and tell the reviewer why the finding is overruled, or ask the user. A `GOAL-CONFLICT` line goes to the user with both options. Then prompt the task's implementer with the rest. Copy each remaining finding line from the review into the brief and compare the count of lines in the brief with the count in the review, because a hand summary can drop a finding, and the next review then finds it a round later. The prompt: `<goal prefix if any>End state: <end-state goal>. Read <review file>. Fix every P0 and P1 finding in it on your branch: failing test first, then the code, run the suites, commit, push, reply on any review-bot threads, then update your report file (new head, what changed per finding under decisions:) and reply with only its path. If you disagree with a finding, write the reason under decisions: and still make the smallest change that satisfies its fix line.` (In an integration route, and at the first gate of a two-gate route: commit only, no push and no threads.) If the prompt answers `agent_blocked`, clear the queued question first (harness notes). Then wait for the implementer's REPORT-UPDATED, then send the reviewer the new head as `review-<n>-<k+1>`. A finding whose fix spans two tasks' files goes into the integrator contract as a handoff ([`references/ownership-and-integration.md`](references/ownership-and-integration.md)).
4. Repeat until pass. A disagreement goes to the user at once: when an implementer disagreed with a finding (under decisions:) and the reviewer raises it again, the user decides before another fix round starts. Only fix rounds count toward the limit of three, not reviews, because counting reviews stops a pull request that is still fixable. The review after the third fix round on one pull request or branch is the final gate: when it still finds P0 or P1, stop and show the user the remaining findings.
5. Integration route: when the branches have passed, prompt the integrator with the combine phase (or earlier, see step 3 item 7); a branch fix round after the combine goes to the integrator as a re-merge phase. On its report, run the final review of the combined head against the base branch (`review-<pr>-<k>`), in parallel with any late test-only task; when a branch re-review is still pending, fold it in and name its earlier report and open findings in the prompt ([`references/flow-and-time.md`](references/flow-and-time.md)). Send the final review's findings to the integrator as a fix phase on the combined branch, test first. Add a late task in its own phase, then review the new head again. Only a passing final review (no open ENV line) starts the push phase, and only when every branch review has passed too or was folded into that final review with each of its findings answered `closed` under `CLOSED:`; a branch review still pending holds the push. Branch reviews miss defects on paths between tasks, so the final review is never skipped ([`references/review-discipline.md`](references/review-discipline.md)).
6. The reviewer posts nothing on GitHub and pushes nothing. Merges are the user's unless authorized; the authorized form is a squash merge through the asynchronous merge API, pinned to the reviewed head and polled until `merged` or `failed` (an `enqueued` result ends the polling; confirm the merge separately once the queue lands it), when the local verdict is pass with no open ENV line AND the bot approval is green on the same head ([`references/git-and-github.md`](references/git-and-github.md), Merges). A stacked pull request merges with every open pull request below it, so each of them must pass first. Before the first merge read the base branch's rules (`gh api repos/<org>/<repo>/rules/branches/<base>`): a required second approval that the author's account cannot give needs the user's choice (admin bypass, a colleague's approval, or a rule change); never bypass on your own. After a merge, when the user asked for it, delete the remote branch if the pull request is MERGED and no open pull request is based on it; keep the local worktree until route close, or remove it as soon as the pull request merges when the user agrees ([`references/route-close.md`](references/route-close.md), Disk space).
7. Parallel implementers that generate database migrations from the same base produce the same migration numbers. Before merging the second such pull request, send its implementer a fix round: merge the new main into its branch (a merge, not a rebase: [`references/ownership-and-integration.md`](references/ownership-and-integration.md), Numbers and stacks), regenerate the migration history, run the suites, push. Say this in the shared rules up front when more than one task touches the schema.

A reviewer is one persistent agent; send it one review at a time. If reviews queue up, ask the user before starting a second reviewer pane.

When commits are not allowed, review tree snapshots instead of pull requests ([`references/git-and-github.md`](references/git-and-github.md), No-commit routes).

## 6. Record and close

Read first: [`references/route-close.md`](references/route-close.md), before the first close of an agent, a session or a worktree.

- Append a run-state line to `00-route.md` at every event that changes state: launch, blocker, report, review verdict, fix round, pass. Use `scripts/route-log.sh <route>/00-route.md "<text>"` (it writes `- HH:MM <text>` from `date`), never a typed time.
- Keep the orchestrator context small: read report files and review files, not pane transcripts, except to unblock.
- At close: `route-check.sh <route folder> close` shows no open checklist line (each one done, or `[-]` with its reason); table of pull requests (number, branch, head, checks, review verdict, rounds), open items, and the exact things the user still owns (merges, secrets, infrastructure). Before you close an agent, stop a session or remove a worktree, follow [`references/route-close.md`](references/route-close.md). Write the lessons of the route into memory per the user's memory conventions.
- When a lesson is general, write it into this skill as a rule with its reason: in the concept file of its topic (list above), or in the tool or harness notes when it is a command quirk; merge it with an older rule on the same point instead of adding a second one. A new concept gets a new file and a line in the Concepts list. No dates, route stories, counts, durations, versions or project names in the skill: those stay in memory.
- Before you change this skill, read its checkout's git status and diff. Never commit, discard or stash edits that another session made: make your change in a separate worktree from the default branch, and tell the user about the other edits. Compare each idea with the skill first and add only the gaps. Before a commit, scan the added lines for project names, people's names, private paths, email addresses, secrets and dates, and run the skill's tests (`scripts/test/run-tests.sh`), because the repository is public and a pushed line stays visible. Commit and push only on the user's go.

## Rules that do not bend

- The orchestrator never edits application code, never runs the application's tests itself, never pushes. In a route with one pull request per task the implementers push; in an integration route only the integrator pushes. One exception: when an agent's guard refuses a push to the default branch, the user can grant the orchestrator the push of reviewed commits for that route ([`references/shipping.md`](references/shipping.md), Who pushes).
- Never touch git identity, remotes or global git config, in any pane. Push with the one-shot credential helper only.
- No secrets in contracts, reports or pull-request bodies. No people's names or tool names in pull-request bodies.
- One Herdr mutation per call. Ask before every step that changes Herdr layout, containers, GitHub or cloud state, unless the user gave that exact step its go in the dispatch plan.
- Write files with the harness's file tools, never with heredocs or redirects.
- Read `date` before writing any timestamp.
- Never propose that the user add a new credential, token or API key to make a test or run work; the route uses the user's existing logins. A push that changes workflow files and needs a token scope is the owner's own step ([`references/git-and-github.md`](references/git-and-github.md), Identity and credentials).
- A report whose required live test skipped is not complete. Do not send it to review. Send it back to its author to run the test.
- When an agent reports that a permission classifier blocked an environment fix, verify the cause read-only, then give the user the exact command (on the clipboard with `pbcopy` when asked). Never run it past the classifier yourself.
