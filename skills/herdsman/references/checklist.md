# Route checklist

The steps of [`SKILL.md`](../SKILL.md) as lines to tick, each linked to the rule it comes from. The rules stay in their files; a line only says what must be done and where the rule is. Reason: a long route outlives the orchestrator's context, and after a compaction the skill text is gone while the route file stays. A line that is still open in the route file shows what is not done yet, without the skill text in view.

## How to use it

- At step 2, copy the section "Lines" below into `00-route.md` under a heading `## Checklist`. Keep the lines whose tag fits the route and delete the others: `all`; `pr` (one pull request per task); `int` (integration route); `gate2` (two-gate route); `ship` (shipping without pull requests); `ro` (review-only route on someone else's pull request); `db` (a test database); `sbx` (a sandboxed harness runs checks). Copy the block "Per review unit" once for each task branch, pull request or combined head, with its id in place of `<u>`.
- Tick a line with the Edit tool when it is done: `- [x]` and ` -- <evidence>` at the end of the line (a path, a commit, a pane id, the user's answer). A line that stops applying mid-route becomes `- [-]` with the reason. Never tick a line from memory of what was done: check the evidence first.
- Read the open lines on every wake and after every compaction, before the next action. [`scripts/route-check.sh`](../scripts/route-check.sh) prints the open lines and checks what it can see on disk.
- The checklist does not replace a step's text. Each step starts with a "Read first" line: read those files before the step's first action, and tick the line.

## Lines

### Step 0: preconditions

- [ ] C0.1 all: Read first: [`harness-guards.md`](harness-guards.md), before the first shell command.
- [ ] C0.2 all: `HERDR_ENV=1`; `herdr agent start --help`; `<harness> --version` for every harness; `gh auth status`; the target repository's remote ([`SKILL.md`](../SKILL.md), step 0).
- [ ] C0.3 all: Read the skill checkout's git status before any change to this skill ([`SKILL.md`](../SKILL.md), step 6).

### Step 1: interview and dispatch plan

- [ ] C1.1 all: Read first: every concept file in the Concepts list of [`SKILL.md`](../SKILL.md), the harness notes of each harness in the route, and [`optional-techniques.md`](optional-techniques.md). With the Skill navigator technique, read the sections its map names instead, and record the map's path.
- [ ] C1.2 all: One question round through the harness's ask tool that covers every interview item the request leaves open ([`SKILL.md`](../SKILL.md), step 1), including product defaults that change what users see and the optional techniques.
- [ ] C1.3 db: Read the test database's database count and its container's shared-memory use before the first gate; a high count puts the recreate question in the interview ([`shared-environment.md`](shared-environment.md), Share the load).
- [ ] C1.4 all: Count the reviews the route needs against each harness's usage pool ([`flow-and-time.md`](flow-and-time.md), Quota is capacity).
- [ ] C1.5 all: Dispatch plan in the reply, with the folder-trust answer of each new folder; the go asked through the ask tool; nothing launched before the go.
- [ ] C1.6 all: Each chosen option's full text copied into `00-route.md`.

### Step 2: route folder and contracts

- [ ] C2.1 all: Read first: [`contracts.md`](contracts.md) and [`shared-environment.md`](shared-environment.md).
- [ ] C2.2 all: Route folder under `.herdsman/`, and `.herdsman/` ignored by git.
- [ ] C2.3 all: `00-route.md` holds Decisions, Must prove, Task table, Checklist and Run state.
- [ ] C2.4 all: A read-only scout checked each assumption the tasks rest on, the numbers already taken and the open pull requests on the same files ([`contracts.md`](contracts.md), Rules learned).
- [ ] C2.5 pr int gate2 ship: `10-shared-rules.md` and one `1n-task-<x>.md` per task, each with a `Due:` line; every contract quotes the Must-prove list.
- [ ] C2.6 int: `1n-task-i.md` with one section per phase.
- [ ] C2.7 all: `20-reviewer.md` (or the review brief of a review-only route); every review skill it names exists in the reviewer's harness; no package runner with an unpinned tag.
- [ ] C2.8 all: `run-check.sh` written from the template, executable, its load limit set from the idle load, and tested: one real check through it, and one run with `HERDSMAN_PROBES=off` ([`shared-environment.md`](shared-environment.md), Share the load).
- [ ] C2.9 sbx: The load publisher runs outside the sandbox, and its stop time is in `00-route.md`.
- [ ] C2.10 db: The shared stack checked (runtime up, database healthy, ports free, leftover stacks recorded) ([`shared-environment.md`](shared-environment.md), Contracts name the environment exactly).
- [ ] C2.11 all: The base's gate results known: the default branch's latest CI read, the reviewer's first job runs the gates on the base, and the known failures listed in the shared rules ([`shared-environment.md`](shared-environment.md), Facts, not guesses).
- [ ] C2.12 all: Outside documents copied into `ref/`, probe files and tool scripts in `probes/`.

### Step 3: launch

- [ ] C3.1 all: Read first: [`herdr.md`](herdr.md) and the harness notes of each harness.
- [ ] C3.2 all: Tabs and panes created one mutation per call, each labelled ([`herdr.md`](herdr.md), Etiquette); pane ids in the task table.
- [ ] C3.3 all: Each agent started one per call; each folder-trust dialog answered as the plan says; each harness's usage line read.
- [ ] C3.4 all: Each prompt is a full brief with a due time from `date`, logged in `00-route.md`, and passed to the waiter.
- [ ] C3.5 all: Each pane read (at least 30 lines) after its prompt: the prompt echoed and the working marker shown.
- [ ] C3.6 all: Checkpointer probed, then started in its own pane, unless the user declined it.

### Step 4: waiting

- [ ] C4.1 all: Read first: [`watchers.md`](watchers.md).
- [ ] C4.2 all: Waiter wrapper in the route folder; waiter armed as a background command that exits.

### Every wake (re-read on each wake; never ticked)

- Handle each event as step 4 of [`SKILL.md`](../SKILL.md) says, then re-arm the waiter through its wrapper, or write "idle, waiter not armed" in `00-route.md` when nothing is in flight.
- Run `agent-status.sh`; read each pane that got a prompt since the last wake.
- Compact or clear every agent that finished ([`context-hygiene.md`](context-hygiene.md)).
- Log each state change with `route-log.sh`.
- Read the open lines of this checklist.

### Per review unit `<u>` (copy once per task branch, pull request or combined head)

- [ ] C5.<u>.1 all: Read first: [`review-discipline.md`](review-discipline.md).
- [ ] C5.<u>.2 all: The reviewer's context set (compact for a follow-up, clear for a new unit), then the review prompt with its report path and due time.
- [ ] C5.<u>.3 all: Each review read, its ENV lines first; each ENV line's closing run passed on the same head, or the line became a P0.
- [ ] C5.<u>.4 all: Each fix line checked against the Must-prove list before it is forwarded; the brief holds as many finding lines as the review.
- [ ] C5.<u>.5 all: Pass recorded, or the final gate after the third fix round shown to the user.
- [ ] C5.<u>.6 gate2: After the local pass, the pull request opened and passed by the same reviewers.

### Step 5: combine, merge and ship

- [ ] C5.1 int: Final review of the combined head against the base branch passed, and every branch review passed or was folded into it, before the push phase.
- [ ] C5.2 all: Read first, before the first merge or push: [`git-and-github.md`](git-and-github.md) (Merges) or [`shipping.md`](shipping.md).
- [ ] C5.3 pr int gate2: The base branch's rules read before the first merge; a required approval that the author cannot give asked of the user through the ask tool.
- [ ] C5.4 pr int gate2: Each merge only on the merge policy's gate, on the reviewed head.
- [ ] C5.5 ship: Each push passed the ship gate, and CI was read for each pushed commit.

### Step 6: record and close

- [ ] C6.1 all: Read first: [`route-close.md`](route-close.md).
- [ ] C6.2 all: Each worktree checked for edits and unpushed commits; each agent's resume data saved in the route folder before it closed.
- [ ] C6.3 all: Each agent closed with its harness's exit command; the waiter and watchers stopped; the checkpointer closed only on the user's go.
- [ ] C6.4 all: Live sources read before the close report; the close table and what the user still owns are in the reply and in `00-route.md`.
- [ ] C6.5 all: Lessons written to memory; general lessons into this skill under its own rules ([`SKILL.md`](../SKILL.md), step 6).
