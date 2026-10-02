# Contract templates

Contracts are the only channel from the orchestrator to the other agents. An implementer reads two files (shared rules, its task), the integrator reads the shared rules and the integrator contract, and a reviewer reads the reviewer contract. Write them so that a careful engineer with no chat history can act. Plain American English, short sentences, no em dashes, no names of people, no secrets.

## 10-shared-rules.md

```
# Shared rules for every implementer and the integrator

Repository: <absolute path of the checkout> (remote `origin` = GitHub <org>/<repo>). <one line on the stack and the package layout>. Read <the repo's agent guide, specs and ADRs> before coding.

## Must prove

<the Must-prove list from 00-route.md, quoted as written>

## Setup

1. Fetch first with the credential helper (the sandbox has no terminal for a password prompt):
   `git -C <repo> -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' fetch origin`
   The local base branch may be behind; always branch from `origin/<base>`.
2. Worktree (if the route uses worktrees): `git -C <repo> worktree add <worktree path> -b <branch> origin/<base>`. Work only inside your worktree.
3. Install dependencies (`bun install` or the project's command). If the default cache path is not writable, set TMPDIR and the package manager's cache directory to a path under /private/tmp.
4. Test database: <host:port, user, auth>. Create your own database `<prefix>_<task>_test`, run suites with `TEST_DATABASE_URL=... <test command>` inside the package, drop it when you finish. Never touch another database.

## Code standard

- <language and strictness rules of the repo>.
- Migrations: <generated only; how; next free number and where numbering may collide with open pull requests>.
- Tests first for every behaviour change: a failing test, then the code. Coverage: 100% function and line for the files you changed or added; list pre-existing gaps in other files under `gaps:` in the report and do not touch those files.
- Run before commit: <format, lint, type check, test commands>. Everything green.
- Commit messages: conventional (`feat(scope): ...`), one logical change per commit, the body says why.

## CPU

Run at most one TypeScript compiler process, single-threaded: `GOMAXPROCS=1 tsc --singleThreaded --checkers 1 --builders 1` (TypeScript 7 native); never run package scripts that pass more checkers. The test runner gets one worker (`--maxWorkers=1` for vitest). One heavy process at a time, your subagents included.

Run every full test suite, type check and build through the machine's gate: `/bin/bash <skill>/scripts/with-gate.sh <command>` (a command line with `&&` or a pipe goes in one `sh -c '...'`). The gate runs one such command at a time across all agents on this machine, at a lower priority, and says when it waits. Targeted tests of a few files run without it. Never remove the gate's lock folder (`/tmp/herdsman-gate`).

## Live tests

- A live test proves a Must-prove path. It runs the real harness through the route's execution path with the user's normal installed login.
- It does not depend on a token or an API key, and it does not skip when one is missing.
- Test code never edits the harness's own settings or login files. Writes the harness itself makes during the run are expected (session transcripts, a trust entry for the throwaway folder). The test deletes only what it created.
- It runs in a throwaway session and a throwaway folder, and stops what it started.

## Push and pull request

<Route with one pull request per task: keep this section. Integration route: replace it for the implementers with "Commit to your local branch only. Never push, never open a pull request, never post on GitHub. A change that another task's file needs is a `handoff:` line in your report, not an edit." and keep this section for the integrator's push phase only.>

- Push with the one-shot credential helper, never by changing the git identity or the remote:
  `git -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' push -u origin <branch>`
- Open a READY pull request: `GH_TOKEN=$(gh auth token --user <gh account>) gh pr create --repo <org>/<repo> --base <base> --head <branch> --title "<type>(<scope>): <title>" --body-file <file>`.
- PR body: `## Summary` (problem and change in plain words); one bullet per file or module with what changed and why; `## Verification` with the exact commands and databases; `## Known gaps` if anything is left. Never mention review rounds, reviewer or tool names, people's names, metrics or timings, process history. Reference issues by number.
- After the pull request is open, wait five minutes, then read `gh pr checks <n>` and the review-bot comments. Fix real findings test-first, push, reply on each thread with "Addressed in <sha>". Repeat until checks pass and the bot reports no new issues. If the bot is wrong, reply with the reason and move on.
- Do not change a pull request's base branch, dissolve a stack, force-push over someone else's commits, or close a pull request without an instruction in your task file.

## Keep going

Do not stop to ask when the next step needs no input. Stop and write the report only before a destructive action (deleting data, a force push, a change outside your worktree), at a conflict between two rules, at a missing login, or at the due time.

## Report

When done, write `<writable root>/reports/<task>.md` with, in this order: `STATUS: complete` or `STATUS: blocked <reason>`; `needs:` (each decision or approval that the orchestrator or the user must give, or `none`); branch; worktree; PR URL; head commit; the exact test commands and their pass counts; `decisions:` (every judgement call); `handoff:` (changes that another task's files need, integration route); `gaps:`. Under 60 lines. Then reply in the terminal with only the path of that file.
```

## 1n-task-<x>.md

```
# Task <X>: <title>

Branch `<branch>` from `origin/<base>`. Worktree `<path>`. Test database `<prefix>_<x>_test`. Report file `<writable root>/reports/<x>.md`.

Due: <HH:MM> (<N> minutes from launch). If the work is not finished by then, stop starting new work, write the report from what exists with `STATUS: gaps`, and reply with its path. The orchestrator also enforces this time from outside.

## Problem
<what is wrong or missing, with file paths that prove it>

## Scope
1. <numbered, testable items; name files, functions, constants, rules>
...

## Out of scope
<what the implementer must not touch, including sibling tasks' files>

## Overlap
<open pull requests that touch the same files; what to do: read, do not depend, note "rebase after PR n merges" under Known gaps>

## Acceptance
- <observable checks a reviewer can repeat>
```

Rules learned:
- Name every constant and rule explicitly (`IMPORT_PRICE_CHANGE_FLAG_RATIO = 0.10`), or the implementer invents its own.
- Say where sibling work overlaps; otherwise two implementers edit the same migration number or the same module.
- Scope every acceptance line to the task's own files. A line about a whole folder ("no test file under X is longer than N lines") makes the reviewer report every old file in that folder as unmet.
- State which GitHub mutations are allowed. An implementer given "authorized base change" latitude dissolved a stack and retargeted a pull request on its own.
- Put the coverage rule on changed files only.
- Read the repository's guard tests (import boundaries, banned modules) before you write file paths into a contract. They can forbid a path the contract names, and they often follow imports through every file in the chain. A ruling that allows one forbidden import fails as soon as another file imports that file.
- Name the hidden limits that a reviewer will probe: shared helpers with a ceiling (a list call that stops at a row cap) and platform limits (a timer delay above the maximum fires at once, so bound the delay and arm it again). Each limit that the contract leaves out costs one more review round.
- Tests that replace a global (the clock, timers, a spied function) restore each one after every test. Say it in the contract: a test that leaks a fake clock into later files passes alone and fails in the full run.
- Give every brief a `Due:` line and pass the same time to the waiter as `HERDSMAN_DUE_<name>`. The line alone does not stop an agent: agents ignored stop times written in their own briefs, so the waiter's OVERDUE event is what triggers the stop-and-report steer.
- When two or more tasks change the database schema, say in the shared rules that generated migration numbers will collide across branches and that the pull request merged second regenerates its migration history after a rebase onto main. Parallel branches from one base all take the same next number.

## 1n-task-i.md (integration route)

```
# Task I: integrate <route> into one branch

Branch `<combined branch>` from `<start commit>`. Worktree `<path>`. Test databases `<prefix>_i_<package>_test`. Report file `<writable root>/reports/i.md`. You are the only agent that pushes. Do each phase only when the orchestrator's prompt names it; after each phase update the report and reply with only its path.

## Phase 1: combine (the prompt names the accepted branches)
1. Merge in this order with `git merge --no-ff`: <branch list>. Resolve conflicts by keeping both tasks' intent; list each resolution under decisions:.
2. Apply every `handoff:` line from the task reports, and the orchestrator handoffs below, test first.
3. Fix only integration breakage: a test that passed on its branch and fails on the combined branch.
4. Run every gate: <format, lint, type check, every test suite with your databases>.

## Phase 1b and later: fixes from the final review (the prompt names the review file)
Fix every P0 and P1 on the combined branch, failing test first. New tests go in new files, never in a file that a running task replaces.

## Phase 2: add a late task (the prompt names its commit)
Merge it, run every gate again, update the report.

## Phase 3: push (only after the orchestrator's prompt says the final review passed)
1. Fetch with the credential helper. If `origin/<target>` is not `<start commit>` any more, stop and report `STATUS: blocked remote moved`.
2. Push as a fast-forward only (`git push origin <combined branch>:<target>`); never force.
3. Reply once on each review thread with the fix commit or the reason (texts from the task reports).
4. Run the review-bot loop from the shared rules.

## Orchestrator handoffs
<fixes that span two tasks' files, each with its regression test>

## Never
Change a task's intent, edit application code outside a merge, a handoff or a review fix, force-push, or post anything the phases do not name.
```

Rules learned:
- Start the integrator when the first branches pass review, not at launch, and give it the merge order in the contract. Conflicts then happen in one place, in a known order.
- Write a phase into the contract before its prompt, as a dated "Orchestrator amendments" section when the contract already exists; the integrator reads the contract, not the chat.

## 20-reviewer.md

```
# Reviewer contract, route <name>

You review the pull requests or local branches of <org>/<repo> that the implementers produce and, in an integration route, the integrator's combined branch against the base branch (the final review). You write no application code, push nothing, post nothing on GitHub. Your output is one report file per review.

## Must prove

<the Must-prove list from 00-route.md, quoted as written>

## CPU

Run at most one TypeScript compiler process, single-threaded: `GOMAXPROCS=1 tsc --singleThreaded --checkers 1 --builders 1` (TypeScript 7 native); never run package scripts that pass more checkers. The test runner gets one worker (`--maxWorkers=1` for vitest). One heavy process at a time, your subagents included.

Run every full test suite, type check and build through the machine's gate: `/bin/bash <skill>/scripts/with-gate.sh <command>` (a command line with `&&` or a pipe goes in one `sh -c '...'`). The gate runs one such command at a time across all agents on this machine, at a lower priority, and says when it waits. Targeted tests of a few files run without it. Never remove the gate's lock folder (`/tmp/herdsman-gate`).

## Setup (once)
- Clone: <path>. Your worktree: <worktrees root>/r. Create it if missing: `git -C <clone> worktree add <worktrees root>/r --detach origin/<base>`.
- Remote git commands use the credential helper: `git -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' fetch origin`.
- `gh` runs as `GH_TOKEN=$(gh auth token --user <gh account>) gh ...`.
- Never change git identity, remotes or global config. Do not run `git push`.
- Tests: `TEST_DATABASE_URL=...<prefix>_r_test`; create once, drop and recreate between pull requests.

## Per review
Input: a pull request number, or a local branch and its head commit, and a report path, given in the prompt.
1. Fetch, `git checkout --detach origin/<head branch>` (`gh pr view <n> --json headRefName,baseRefName,headRefOid`); for a local branch, `git checkout --detach <head commit>`. Record the head commit.
2. Changed files: `gh pr diff <n> --name-only`, or `git diff --name-only origin/<base>...<head commit>` for a local branch. The final review of an integration route uses the combined branch against the base branch and checks every task contract, the integrator contract and the paths between tasks.
3. React files (<paths>): run the `/react-review` skill on those files; skip the runtime layer.
4. All TypeScript in the diff: run the `/thermo-nuclear-code-quality-review` skill on the diff against the base branch.
5. Run the test commands from the PR body's Verification section with your database. A failing test is a P0.
6. Check the pull request against its task contract (<map of branch to contract file>) and the shared rules. An unmet requirement is at least a P1.

## Severity, report only these
- P0: wrong behaviour, data loss or corruption, a tenancy leak, a security hole, a failing or skipped test, a hand-written or row-breaking migration.
- P1: wrong behaviour in an edge case the contract names, changed behaviour without a test, an unmet contract requirement, a maintainability problem that will cost within the next few stories, a React effect that belongs in render or in an event handler and causes a visible bug or a needless network call.
- Everything below P1 is left out entirely. No style notes, no naming notes, no optional refactors.
- Test isolation: judge it by what the test code writes, not by what the harness writes during a live run.
- GOAL-CONFLICT: a finding whose fix would skip or gate a Must-prove test. Report it with both options, not as P0. The orchestrator takes it to the user.

## Report
Write the file at the path given in the prompt. Under 80 lines:
VERDICT: pass | fix
PR: <number> <head branch> <head commit>
TESTS: <command> -> <pass>/<total> (one line per command)
FINDINGS:
- P0 | <file>:<line> | <what is wrong and what happens> | fix: <one sentence>
- P1 | ...
- GOAL-CONFLICT | <file>:<line> | <the risk> | option 1: <keep the Must-prove test, fix this> | option 2: <skip or gate it>
NOT CHECKED: <what this review could not check, for example remote CI, a live service or a GitHub state, one line each>
`VERDICT: pass` means zero P0 and zero P1. Then reply in the terminal with only the path.
```

Rules learned:
- Give the reviewer its own worktree; it must not touch an implementer's or the integrator's worktree while that agent is mid-fix.
- The severity words must be defined in the contract; reviewers otherwise mix vocabularies (major, high, blocking). Gate on the `VERDICT:` line, not on adjectives.
- A skill named in the contract may not exist in the reviewer's harness. Check `ls ~/.claude/skills` before writing the contract, and keep a fallback line ("if the skill is missing, say so in the report and do a direct pass with the same checklist").
- One report per review round, numbered; never overwrite, so the fix history stays readable.
