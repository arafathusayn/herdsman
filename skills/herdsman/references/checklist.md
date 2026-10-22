# Route checklist

The steps of [`SKILL.md`](../SKILL.md) as lines to tick, each linked to the rule it comes from. The rules stay in their files; a line only says what must be done and where the rule is. Reason: a long route outlives the orchestrator's context, and after a compaction the skill text is gone while the route file stays. A line that is still open in the route file shows what is not done yet, without the skill text in view.

## How to use it

- At step 2, copy the section "Lines" below into `00-route.md` under the heading `## Checklist`, with every link target made absolute: `](harness-guards.md)` becomes `](<skill>/references/harness-guards.md)` and `](../SKILL.md)` becomes `](<skill>/SKILL.md)`, with the skill's folder in place of `<skill>`. Reason: the links here are relative to this file's folder, and they would point into the route folder after the copy. Keep the lines whose tag fits the route and delete the others: `all`; `pr` (one pull request per task); `int` (integration route); `gate2` (two-gate route); `ship` (shipping without pull requests); `ro` (review-only route on someone else's pull request); `db` (a test database); `sbx` (a sandboxed harness runs checks); `nocommit` (a route that may not commit); `nopane` (the user picked [Without agent panes](optional-techniques.md), so the lines about Herdr panes become `[-]` with that reason). Copy the block "Per review unit" once for each task branch, pull request or combined head, with its id in place of `<u>`.
- Tick a line with the Edit tool when it is done: `- [x]` and ` -- <evidence>` at the end of the line (a path, a commit, a pane id, the user's answer). A line that stops applying mid-route becomes `- [-]` and ` -- <reason>`. Never tick a line from memory of what was done: check the evidence first.
- Read the open lines on every wake and after every compaction, before the next action. [`scripts/route-check.sh`](../scripts/route-check.sh) prints them, reports a ticked line without its evidence or reason as BAD, and checks the files that the kept lines require. Its close stage is no line here: it passes only once every line is closed, so it runs after the last tick ([`SKILL.md`](../SKILL.md), step 6).
- The checklist does not replace a step's text. Each step starts with a "Read first" line: read those files before the step's first action, and tick the line.

## Lines

### Step 0: preconditions

- [ ] C0.1 all: Read first: [`harness-guards.md`](harness-guards.md), before the first shell command, and [`herdr.md`](herdr.md).
- [ ] C0.2 all: `HERDR_ENV=1`; `herdr agent start --help`; `<harness> --version` for every harness; `gh auth status`; the target repository's remote ([`SKILL.md`](../SKILL.md), step 0).
- [ ] C0.3 all: Read the skill checkout's git status before any change to this skill ([`SKILL.md`](../SKILL.md), step 6).

### Step 1: interview and dispatch plan

- [ ] C1.1 all: Read first: every concept file and the notes of each harness in the route, or the sections the skill navigator's map names ([`SKILL.md`](../SKILL.md), References).
- [ ] C1.2 all: One question round, through the ask tool when the harness has one, that covers every interview item the request leaves open, including product defaults that change what users see and the optional techniques, the skill navigator always ([`SKILL.md`](../SKILL.md), step 1; [`harness-guards.md`](harness-guards.md), Orchestrator posture).
- [ ] C1.3 db: Read the test database's database count and its container's shared-memory use before the first gate; a high count puts the recreate question in the interview ([`shared-environment.md`](shared-environment.md), Share the load).
- [ ] C1.4 all: Count the reviews the route needs against each harness's usage pool ([`flow-and-time.md`](flow-and-time.md), Quota is capacity).
- [ ] C1.5 all: Dispatch plan in the reply, with the folder-trust answer of each new folder; the go asked, through the ask tool when the harness has one; nothing launched before the go except the skill navigator that the interview picked ([`SKILL.md`](../SKILL.md), step 1).
- [ ] C1.6 all: Each chosen option's full text copied into `00-route.md` ([`SKILL.md`](../SKILL.md), step 1).

### Step 2: route folder and contracts

- [ ] C2.1 all: Read first: [`contracts.md`](contracts.md), [`shared-environment.md`](shared-environment.md), [`checklist.md`](checklist.md).
- [ ] C2.2 all: Route folder under `.herdsman/`, ignored by git; report files under its `reports/`, where every agent can write ([`SKILL.md`](../SKILL.md), step 2).
- [ ] C2.3 all: `00-route.md` holds Decisions, Must prove, Task table, Checklist and Run state ([`SKILL.md`](../SKILL.md), step 2).
- [ ] C2.4 all: A read-only scout checked each assumption the tasks rest on, the numbers already taken and the open pull requests on the same files ([`contracts.md`](contracts.md), Rules learned).
- [ ] C2.5 pr int gate2 ship: `10-shared-rules.md` and one `1n-task-<x>.md` per task (each task-table row whose agent is `im-<x>`), each with every section of its template and a `Due:` line; every contract quotes the Must-prove list; when more than one task changes the schema, the shared rules say how migration numbers collide ([`contracts.md`](contracts.md); [`SKILL.md`](../SKILL.md), step 5 item 7).
- [ ] C2.6 int: `1n-task-i.md` with one section per phase ([`contracts.md`](contracts.md), 1n-task-i.md).
- [ ] C2.7 pr int gate2 ship: `20-reviewer.md`; every review skill it names exists in the reviewer's harness; no package runner with an unpinned tag ([`contracts.md`](contracts.md), 20-reviewer.md).
- [ ] C2.7 ro: One review brief, `20-review-brief.md`, and no contracts ([`review-discipline.md`](review-discipline.md), Review-only routes).
- [ ] C2.8 pr int gate2 ship: `run-check.sh` written from the template, executable, its load limit set from the idle load, and tested: one real check through it, and one run with `HERDSMAN_PROBES=off` ([`shared-environment.md`](shared-environment.md), Share the load).
- [ ] C2.9 sbx: The load publisher runs outside the sandbox, and its stop time is in `00-route.md` ([`shared-environment.md`](shared-environment.md), Share the load).
- [ ] C2.10 db: The shared stack checked (runtime up, database healthy, ports free, leftover stacks recorded) ([`shared-environment.md`](shared-environment.md), Contracts name the environment exactly).
- [ ] C2.11 pr int gate2 ship: The base's gate results known: the default branch's latest CI read, the reviewer's first job set to run the gates on the base, and the known failures listed in the shared rules ([`shared-environment.md`](shared-environment.md), Facts, not guesses).
- [ ] C2.12 all: Outside documents copied into `ref/`, probe files and tool scripts in `probes/` ([`SKILL.md`](../SKILL.md), step 2).

### Step 3: launch

- [ ] C3.1 all: Read first: [`herdr.md`](herdr.md) and the notes of each harness in the route.
- [ ] C3.2 pr int gate2 ship: Tabs and panes created one mutation per call, each labelled; pane ids in the task table ([`SKILL.md`](../SKILL.md), step 3; [`herdr.md`](herdr.md), Etiquette).
- [ ] C3.2 ro: The reviewer panes the route reuses named in the task table, each cleared first ([`review-discipline.md`](review-discipline.md), Review-only routes).
- [ ] C3.2 nopane: Each agent type runs as a Workflow stage or subagent with its contract in its prompt; the task table's Pane column says `none`, and `route-check.sh` skips its pane check ([`optional-techniques.md`](optional-techniques.md), Without agent panes).
- [ ] C3.3 pr int gate2 ship: Each agent started one per call, as step 3 items 4 and 5 say; each folder-trust dialog answered as the plan says; each harness's usage line read ([`SKILL.md`](../SKILL.md), step 3; [`flow-and-time.md`](flow-and-time.md), Quota is capacity).
- [ ] C3.4 all: Each prompt is a full brief with a due time from `date`, logged in `00-route.md`, and passed to the waiter ([`SKILL.md`](../SKILL.md), step 3 items 6 and 9).
- [ ] C3.5 all: Each pane read after its prompt as step 3 item 8 says: the prompt echoed and the working marker shown ([`SKILL.md`](../SKILL.md), step 3).
- [ ] C3.6 all: Checkpointer probed, then started in its own pane, unless the user declined it ([`SKILL.md`](../SKILL.md), step 3 item 10).
- [ ] C3.7 all: `route-check.sh <route folder>` without a MISSING or BAD line before the first agent of the route starts or gets a prompt, and with `launch` once the task table names every pane, or at once in a `nopane` route ([`SKILL.md`](../SKILL.md), step 3).
- [ ] C3.8 pr int gate2 ship: The reviewer's first job, the base gates, prompted with the implementers' first prompts ([`SKILL.md`](../SKILL.md), step 3 item 6).
- [ ] C3.9 int: The integrator started and prompted as step 3 item 7 says, each phase written into its contract first ([`SKILL.md`](../SKILL.md), step 3 item 7).

### Step 4: waiting

- [ ] C4.1 all: Read first: [`watchers.md`](watchers.md), [`flow-and-time.md`](flow-and-time.md).
- [ ] C4.2 all: Waiter wrapper in the route folder; waiter armed as a background command that exits ([`SKILL.md`](../SKILL.md), step 4).

### Every wake (re-read on each wake; never ticked)

- Handle each event as step 4 of [`SKILL.md`](../SKILL.md) says, then re-arm the waiter through its wrapper, or write "idle, waiter not armed" in `00-route.md` when nothing is in flight.
- Run `agent-status.sh`; read each agent's pane after each prompt and after each re-arm ([`SKILL.md`](../SKILL.md), step 4).
- Compact or clear every agent that finished ([`context-hygiene.md`](context-hygiene.md)).
- Log each state change with `route-log.sh` ([`SKILL.md`](../SKILL.md), step 6).
- Read the open lines of this checklist (`route-check.sh <route folder> launch`).

### Per review unit `<u>` (copy once per task branch, pull request or combined head)

- [ ] C5.<u>.1 all: Read first: [`review-discipline.md`](review-discipline.md); in an integration route also [`ownership-and-integration.md`](ownership-and-integration.md).
- [ ] C5.<u>.2 all: The reviewer's context set, then the review prompt with its report path and due time ([`SKILL.md`](../SKILL.md), step 5 item 1); one review at a time per reviewer, and a second reviewer only with the user's go ([`SKILL.md`](../SKILL.md), step 5, after item 7).
- [ ] C5.<u>.3 all: Each review read, its ENV lines first; each ENV line's closing run passed on the same head, or the line became a P0 ([`SKILL.md`](../SKILL.md), step 5 item 2; [`contracts.md`](contracts.md), 20-reviewer.md Severity).
- [ ] C5.<u>.4 all: Each fix line checked against the Must-prove list before it is forwarded; every finding is either in the fix brief word for word or has a recorded ruling that the reviewer was told ([`SKILL.md`](../SKILL.md), step 5 item 3).
- [ ] C5.<u>.5 all: Pass recorded, or the final gate shown to the user; a disagreement that a reviewer raised again went to the user before the next fix round ([`SKILL.md`](../SKILL.md), step 5 item 4).
- [ ] C5.<u>.6 gate2: After the local pass, the pull request opened and passed by the same reviewers ([`SKILL.md`](../SKILL.md), step 5 item 2).

### Step 5: combine, merge and ship

- [ ] C5.1 int: Final review of the combined head against the base branch passed, and every branch review passed or was folded into it, before the push phase ([`SKILL.md`](../SKILL.md), step 5 item 5).
- [ ] C5.2 all: Read first, before the first merge or push: [`git-and-github.md`](git-and-github.md) (Merges) or [`shipping.md`](shipping.md).
- [ ] C5.3 pr int gate2: The base branch's rules read before the first merge; a required approval that the author cannot give asked of the user ([`git-and-github.md`](git-and-github.md), Merges).
- [ ] C5.4 pr int gate2: Each merge only on the merge policy's gate, on the reviewed head ([`SKILL.md`](../SKILL.md), step 5 item 6).
- [ ] C5.5 ship: Each push passed the ship gate, and CI was read for each pushed commit ([`shipping.md`](shipping.md), The ship gate).
- [ ] C5.6 pr: Pull requests whose migrations come from the same base: the update round before the second merge ([`SKILL.md`](../SKILL.md), step 5 item 7).
- [ ] C5.7 nocommit: Tree snapshots reviewed instead of pull requests ([`git-and-github.md`](git-and-github.md), No-commit routes).

### Step 6: record and close

- [ ] C6.1 all: Read first: [`route-close.md`](route-close.md).
- [ ] C6.2 all: Each worktree checked for edits and unpushed commits; each agent's resume data saved in the route folder before it closed ([`route-close.md`](route-close.md), Check before you close; Save, then stop).
- [ ] C6.3 all: Each agent that the route started closed with its harness's exit command, and panes the route reused left open; the waiter and watchers stopped; the checkpointer closed only on the user's go ([`route-close.md`](route-close.md), Save, then stop).
- [ ] C6.4 all: Live sources read before the close report; the close table and what the user still owns are in the reply and in `00-route.md` ([`SKILL.md`](../SKILL.md), step 6; [`harness-guards.md`](harness-guards.md), Orchestrator posture).
- [ ] C6.5 all: Lessons written to memory; general lessons into this skill under its own rules ([`SKILL.md`](../SKILL.md), step 6).
