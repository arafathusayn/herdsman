---
name: herdsman
description: "Run an orchestrator, implementer, reviewer pull-request loop inside Herdr: the current Claude Code session orchestrates, Codex or Muse workers implement one task each (optionally in their own git worktree and branch), a Claude Code reviewer on a stricter model reviews every pull request (or tree snapshot, when no commits are allowed) with named review skills and reports only P0 and P1 findings, and the loop of fixes and re-reviews runs until each pull request passes. A checkpointer pane keeps the orchestrator's memory current and its context compacted. Use when the user says herdsman (formerly claudex), asks to orchestrate coding tasks with Codex or Muse workers and a Claude reviewer through Herdr, or asks for parallel implementation agents with a review loop. Requires HERDR_ENV=1."
---

# herdsman: orchestrator, implementers, reviewer

One Claude Code session (this one) orchestrates. Codex workers implement, one task each. One Claude Code reviewer on a stricter model reviews every pull request. The orchestrator writes no application code: it writes contracts, launches, waits, forwards findings, verifies, records.

Read `references/harness-guards.md` before the first shell command. Read the other references when the step names them.

## 0. Preconditions

```bash
test "${HERDR_ENV:-}" = 1
```

Stop if this fails. Then, in one call: `herdr agent start --help` (kinds), `codex --version`, `claude --version`, `gh auth status`, and the git remote of the target repository. Read `references/herdr.md` for the command surface.

## 1. One interview, then a dispatch plan

Ask once, with one AskUserQuestion, everything the route needs. Do not ask again mid-route unless a step is unsafe without an answer.

- Tasks: the list, one worker per task. Each task has a branch name and a base branch.
- Worktrees: "own worktree per task" (default) or "existing checkout". Worktree root, for example `<repo parent>/worktrees/<letter>`.
- Push policy: push and open a READY pull request, or draft, or push only.
- Test infrastructure: local database or services the workers may start (Docker container, port, credentials), one throwaway database per task.
- Implementer model and effort (default `gpt-6-sol`, `model_reasoning_effort=medium`; the user moved from gpt-6-astra high to astra medium and then to Sol on 2026-09-23).
- Reviewer model and effort (default `claude-opus-5-5[1m]`, `--effort xhigh`; Fable 5.1 xhigh until 2026-09-23) and the review skills (default `/react-review` for React files, `/thermo-nuclear-code-quality-review` for TypeScript, plus any security or domain skill the user names).
- Severity gate: report P0 and P1 only (default).
- Merge policy: the user merges, or the orchestrator squash-merges on double approval (local pass plus bot approval on the same head). Whether the remote branch is deleted after a merge.
- Context policy (user rule 2026-09-27, overrides the 2026-09-22 50% threshold): every worker and reviewer is compacted (`/compact`, when a follow-up of its own work comes next) or cleared (`/clear`, `/new` for Codex) right after it finishes; when compaction is refused, clear. Never mid-turn.
- Orchestrator checkpointer (user request 2026-09-27): a pane named `checkpointer` running `scripts/checkpointer.sh` against the orchestrator pane (step 4).
- GitHub account for `gh` when the default account cannot see the repository (`GH_TOKEN=$(gh auth token --user <account>)`).

Then present a dispatch plan in the reply (tasks, branches, worktrees, panes, models, report paths, what will be created on disk, in Herdr and on GitHub) and wait for the user's go. Every Herdr tab, Docker start, contract file and worker launch is a mutation that needs that go. Do not launch anything before it.

## 2. Route folder and contracts

Create a route folder the orchestrator owns, for example `<project>/docs/plans/<route>-<date>/` (git-ignored or committed, the user's choice). Write every file with the Write tool. Files:

- `00-route.md`: decisions from the interview, task table (task, branch, worktree, worker name, pane), and a dated run-state list the orchestrator appends to.
- `10-shared-rules.md`: setup, code standard, tests, commit, push, pull-request body rules, review-bot loop, report format. Template in `references/contracts.md`.
- `1n-task-<x>.md`: one contract per task: problem, scope, out of scope, acceptance, branch, worktree, test database, report path.
- `20-reviewer.md`: the reviewer contract: setup, per-review steps, severity definitions, report format. Template in `references/contracts.md`.

Report files must live inside the workers' writable root (the Codex `-C` directory), not in the route folder, unless the route folder is under that root. Use `<writable root>/reports/<task>.md` and `<writable root>/reports/review-<pr>-<n>.md`. See `references/codex-workers.md`.

## 3. Launch, one Herdr mutation per call

Order: tab, panes, agents, prompts. Never two Herdr mutations in one Bash call or in parallel tool calls. Read each result before the next call. Details and the verified command forms in `references/herdr.md`.

1. Worker tab: `herdr tab create --workspace <w> --cwd <writable root> --label "Worker Agents" --no-focus`. Note `root_pane.pane_id`.
2. Panes: for four workers, split the root pane right, then each column down (2 by 2). One split per call. Note every pane id from the JSON.
3. Reviewer tab: `herdr tab create --workspace <w> --cwd <writable root> --label "Reviewer" --no-focus`.
4. Worker agents, one per call (form in `references/codex-workers.md`):
   `herdr agent start wk-<x> --kind codex --pane <id> --timeout 90000 -- --model <model> -c model_reasoning_effort=<effort> -a never --sandbox workspace-write -c sandbox_workspace_write.network_access=true -C <writable root>`
5. Reviewer agent (form in `references/claude-reviewer.md`):
   `herdr agent start rv-1 --kind claude --pane <id> --timeout 90000 -- --model <model> --effort <effort> --permission-mode auto --add-dir <route folder root> --add-dir <scratchpad root>`
6. Prompts, one per call. Codex workers take `/goal`:
   `herdr agent prompt wk-<x> "/goal Read <shared rules> and then <task file>. Do task <X> exactly as those two files specify: worktree, branch, tests first, commit, push, open a ready pull request, follow the review-bot loop, then write the report file and reply with only its path."`
   The reviewer takes plain text (no `/goal`), sent only when a pull request is ready for review.
7. Verify each pane's text after its prompt (`herdr pane read <id> --lines 30`): Codex shows "Goal active" and "Pursuing goal"; Claude shows the prompt echoed and a working line.
8. Every prompt is a full brief, never a pointer (user rule 2026-09-27, "prompt better"): end-state goal the user would see, numbered steps with files, a Check per step, a leave-alone list, hard rules and time limits, the report shape. Muse workers: `references/muse-workers.md`.
9. Checkpointer pane (one split, one rename, one run): `herdr pane split <orchestrator pane> --direction right --ratio 0.7 --no-focus` (right side, user preference 2026-09-27), `herdr pane rename <new> checkpointer`, `herdr pane run <new> "TARGET=<orchestrator pane> /bin/bash <skill>/scripts/checkpointer.sh"`. Probe first with `--probe` (prints the transcript and context tokens, sends nothing).

## 4. Wait for events with a background command that exits

Monitor tasks do not wake the session between turns: their events arrive only while a turn runs or when the user writes. A background Bash command that exits does re-invoke the session. So the loop's clock is `scripts/wait-event.sh`, run as:

```
Bash(run_in_background: true, timeout: 600000, command: "HERDSMAN_REPORTS=... HERDSMAN_STATE=... HERDSMAN_PANE_wk_a=... HERDSMAN_REVIEWER_PANE=... /bin/bash <skill>/scripts/wait-event.sh")
```

It polls every 60 s and exits on the first event pass, or after 540 s with `TICK`. On every wake: handle the events, then re-arm the same command. Events: REPORT (a task report file appeared), REPORT-UPDATED (the report changed after a fix round), REVIEW (a new review file), BLOCKED (password, approval, model-switch, usage-limit or queued-question text; once per screen change), GOAL-DONE-NO-REPORT (Codex shows "Goal achieved" but wrote no report), STALL (working marker but unchanged screen for three polls). `references/watchers.md` has the variables, the bash 3 constraint and the sanity test in `scripts/test/run-tests.sh`. The two Monitor scripts in `scripts/` remain for use inside a long running turn only.

On every wake, also run `scripts/worker-status.sh` (`HERDSMAN_STATUS_SPECS="name:pane:tree ..."`). User rule 2026-09-27: check every agent every ten minutes and do not let background jobs run long. A job flagged over 30 minutes, or a tree with no writes in 10 minutes, gets a stated deadline; past it, steer or interrupt. The orchestrator enforces deadlines, not the worker: a "kill at HH:MM" line in a worker's brief was ignored (2026-09-27). At the deadline queue a stop-and-report steer (cancel every run, start none, write the report from existing results with `STATUS: gaps`, reply with the path); a queued steer lands only when the current turn ends, so with no report within about 10 minutes send `esc`: the turn ends and the queued steer runs next (re-prompt only if nothing was queued). Test logs written outside the tree (for example `/private/tmp/...`) show as "NO WRITES"; check that folder before calling a worker hung. Any agent idle since its finish and not yet compacted or cleared is an open action.

The checkpointer (`scripts/checkpointer.sh`, in its own pane) bounds the orchestrator's own context: every 15 minutes, if the orchestrator's transcript grew, it waits for idle (never blocked), sends `/memory-with-dag` with the user's standard reproduction prompt, waits for that turn, reads the context size (input + cache tokens of the last main-thread assistant turn in `~/.claude/projects/*/<session>.jsonl`) and sends `/compact` at 250K or more. Keep memory and the route log current so each compaction loses nothing. A background event that wakes the orchestrator between the idle check and the prompt only delays the prompt (Claude Code queues it).

React to each event:

- BLOCKED with a password prompt: prompt the worker with the credential-helper form (`references/git-and-github.md`). Never type a password.
- BLOCKED with a model-switch offer: keep the confirmed model (`references/codex-workers.md`).
- BLOCKED with a usage limit: tell the user; never fall back to an API key.
- STALL twice in a row: read the pane in full, then prompt the worker with the exact next step.
- REPORT: read the report, check `STATUS:`, check the pull request (`gh pr view <n> --json headRefOid,mergeable,statusCheckRollup`), then hand the pull request to the reviewer (step 5).

## 5. Review loop

For each pull request with `STATUS: complete` and green checks:

1. Context first, one rule for every agent: a follow-up of the agent's own earlier work (a re-review of the same pull request, a fix round) keeps its context, compacted when above 50% (`/compact`); a completely new task (a pull request the reviewer has not seen, a new contract for a worker) starts from a cleared context (`/clear` for Claude Code, `/new` for Codex). Read the pane after the command. Then prompt the reviewer: `Read <route>/20-reviewer.md and follow it exactly. Review pull request <n> (branch <b>, contract <task file>). Report path: <writable root>/reports/review-<n>-<k>.md. Reply with only that path when done.`
2. On REVIEW, read the file. `VERDICT: pass` closes the loop for that pull request; record it and tell the user.
3. `VERDICT: fix`: prompt the task's worker: `/goal Read <review file>. Fix every P0 and P1 finding in it on your branch: failing test first, then the code, run the suites, commit, push, reply on any review-bot threads, then update your report file (new head, what changed per finding under decisions:) and reply with only its path. If you disagree with a finding, write the reason under decisions: and still make the smallest change that satisfies its fix line.` If the prompt answers `agent_blocked`, clear the queued question first (`references/codex-workers.md`). Context hygiene before the fix-round dispatch: read the worker's context percentage from `herdr agent list`; above 50% send `/compact` first and wait for it to finish (a fix round is a follow-up, so no `/new`); never mid-goal. Then wait for the worker's REPORT-UPDATED, then send the reviewer the new head as `review-<n>-<k+1>`.
4. Repeat until pass. After three fix rounds on one pull request, stop and show the user the remaining findings; a disagreement between worker and reviewer is the user's call.
5. The reviewer posts nothing on GitHub and pushes nothing. Merges are the user's unless authorized; the authorized form is a squash merge when the local verdict is pass AND the bot approval is green on the same head (`references/git-and-github.md`, Merges). Before the first merge read the base branch's rules (`gh api repos/<org>/<repo>/rules/branches/<base>`): a required second approval that the author's account cannot give needs the user's choice (admin bypass, a colleague's approval, or a rule change); never bypass on your own. After a merge, when the user asked for it, delete the remote branch if the pull request is MERGED and no open pull request is based on it; keep the local worktree until route close.
6. Parallel workers that generate database migrations from the same base produce the same migration numbers. Before merging the second such pull request, send its worker a fix round: rebase onto the new main, regenerate the migration history, run the suites, push. Say this in the shared rules up front when more than one task touches the schema.

The reviewer is one persistent agent; send it one pull request at a time. If reviews queue up, ask the user before starting a second reviewer pane.

## 6. Record and close

- Append a dated line (run `date` first) to `00-route.md` run-state at every event that changes state: launch, blocker, report, review verdict, fix round, pass.
- Keep the orchestrator context small: read report files and review files, not pane transcripts, except to unblock.
- At close: table of pull requests (number, branch, head, checks, review verdict, rounds), open items, and the exact things the user still owns (merges, secrets, infrastructure). Write the lessons of the route into memory per the user's memory conventions, and into `references/lessons-log.md` of this skill when they are general.

## Rules that do not bend

- The orchestrator never edits application code, never runs the application's tests itself, never pushes. Workers do.
- Never touch git identity, remotes or global git config, in any pane. Push with the one-shot credential helper only.
- No secrets in contracts, reports or pull-request bodies. No people's names or tool names in pull-request bodies.
- One Herdr mutation per call. Ask before every step that changes Herdr layout, Docker, GitHub or cloud state, unless the user gave that exact step its go in the dispatch plan.
- Write files with the Write and Edit tools, never with heredocs or redirects.
- Read `date` before writing any timestamp.
