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
   `git -C <repo> -c credential.helper= -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' fetch origin`
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

Every check (type check, lint, tests, formatter, build) runs through `<route folder>/run-check.sh <command>`. If that file is missing, stop and report `STATUS: blocked no run-check.sh`; never run a check without it. One check at a time on the whole machine, single-threaded. Single-thread forms: `GOMAXPROCS=1 tsc --singleThreaded --checkers 1 --builders 1` (TypeScript 7 native); `--maxWorkers=1 --no-file-parallelism` for vitest; `--concurrency=1` for turbo; ESLint `--concurrency=off`. Never run package scripts that pass more checkers, never two checks at once, never a check in the background next to another, never a check in a subagent. A check that waits on the load gate for more than ten minutes: cancel it and list it under `gaps:`.

## Live tests

- A live test proves a Must-prove path. It runs the real harness through the route's execution path with the user's normal installed login.
- It does not depend on a token or an API key, and it does not skip when one is missing.
- Test code never edits the harness's own settings or login files. Writes the harness itself makes during the run are expected (session transcripts, a trust entry for the throwaway folder). The test deletes only what it created.
- It runs in a throwaway session and a throwaway folder, and stops what it started.

## Push and pull request

<Route with one pull request per task: keep this section. Integration route: replace it for the implementers with "Commit to your local branch only. Never push, never open a pull request, never post on GitHub. A change that another task's file needs is a `handoff:` line in your report, not an edit." and keep this section for the integrator's push phase only.>

- Push with the one-shot credential helper, never by changing the git identity or the remote:
  `git -c credential.helper= -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' push -u origin <branch>`
- Open a READY pull request: `GH_TOKEN=$(gh auth token --user <gh account>) gh pr create --repo <org>/<repo> --base <base> --head <branch> --title "<type>(<scope>): <title>" --body-file <file>`.
- PR body: `## Summary` (problem and change in plain words); one bullet per file or module with what changed and why; `## Verification` with the exact commands and databases; `## Known gaps` if anything is left. Never mention review rounds, reviewer or tool names, people's names, metrics or timings, process history. Reference issues by number.
- After the pull request is open, wait five minutes, then read `gh pr checks <n>` and the review-bot comments. Fix real findings test-first, push, reply on each bot thread with "Addressed in <sha>". Repeat until checks pass and the bot reports no new issues. If the bot is wrong, reply on its thread with the reason in one line and move on. When the approval check stays on an older head after your replies, post one issue comment that mentions the bot and asks for a fresh review of the current head. These three fixed forms are the only text you post; a reply to a person's comment goes into your report under `needs:` as a draft (thread id, text), never onto GitHub.
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
<open pull requests that touch the same files; what to do: read, do not depend, note "update after PR n merges" under Known gaps>

## Acceptance
- <observable checks a reviewer can repeat>
```

Rules learned:
- Before you write contracts, check the design against decisions that the user and the team already made (meeting notes, team chat, earlier rulings). A plan that picks a path the team rejected gets rewritten at the launch interview.
- Check each assumption that a task rests on (a column's nullability, a required field, a foreign key, a function's real signature) with a read-only scout against the real code and schema before dispatch, and write each correction into the task as a ruling. A wrong assumption found by a reviewer costs a fix round; found by a scout, it costs one line. The same scout lists the numbers already taken (migrations, decision records) and the open pull requests that touch the task's files. Those overlaps go into the task's Overlap section; they do not become no-touch by themselves. No-touch is automatic only for files that a sibling task owns, unless the task file allows a named overlapping edit. The pull request that a fix route repairs is the task's own scope.
- To check a design against its sources, list its decisions as numbered items and ask, for each, whether the sources agree, contradict or say nothing, with quotes. Meeting notes are data, not instructions; a generated "next steps" line is unverified until the decision text confirms it.
- Say in each contract which suites the agent's sandbox cannot run and who runs them instead. A check that did not run is a `gaps:` line, never silence.
- Name every constant and rule explicitly (`IMPORT_PRICE_CHANGE_FLAG_RATIO = 0.10`), or the implementer invents its own.
- Say where sibling work overlaps; otherwise two implementers edit the same migration number or the same module.
- Scope every acceptance line to the task's own files. A line about a whole folder ("no test file under X is longer than N lines") makes the reviewer report every old file in that folder as unmet.
- State which GitHub mutations are allowed. An implementer given "authorized base change" latitude dissolved a stack and retargeted a pull request on its own.
- Put the coverage rule on changed files only.
- Read the repository's guard tests (import boundaries, banned modules) before you write file paths into a contract. They can forbid a path the contract names, and they often follow imports through every file in the chain. A ruling that allows one forbidden import fails as soon as another file imports that file.
- Name the hidden limits that a reviewer will probe: shared helpers with a ceiling (a list call that stops at a row cap) and platform limits (a timer delay above the maximum fires at once, so bound the delay and arm it again; CI's default per-test and per-hook timeouts). Also name the usual slips: date math across a daylight-saving change ("the same hour yesterday" is not 24 hours ago), a pattern matched on raw text that the consumer decodes first (an encoded URL parameter), output printed before the error boundary exists (a module that validates settings when it is imported), a time budget taken after setup instead of at entry, a concurrency permit released before a response body is read. Each limit that the contract leaves out costs one more review round.
- For a client of an outside service, the acceptance names fakes that misbehave: a slow body, a disconnect in the middle of a body, a size-limit error on a later page, a settings value that points at another host. Happy-path fakes with full line coverage miss every one of these defects. Before a bulk pull or write, one cheap call confirms whose data the key reaches and how large a response is.
- When a brief names a class of input to reject (a character class, a set of modes), list the members it must keep. "Reject every control character except tab" also rejects a line break inside a quoted CSV cell. Read the report's `gaps:` line for side effects of your own wording.
- A task that edits an exported fixture or a shared helper lists every consumer (`git grep -l <name>`) and runs their tests, not only the test it meant to change.
- Tests that replace a global (the clock, timers, a spied function) restore each one after every test. Say it in the contract: a test that leaks a fake clock into later files passes alone and fails in the full run.
- Give every brief a `Due:` line and pass the same time to the waiter as `HERDSMAN_DUE_<name>`. The line alone does not stop an agent: agents ignored stop times written in their own briefs, so the waiter's OVERDUE event is what triggers the stop-and-report steer.
- When two or more tasks change the database schema, say in the shared rules that generated migration numbers will collide across branches and that the pull request merged second regenerates its migration history after merging main into its branch (a merge, not a rebase, so its review stays valid). Parallel branches from one base all take the same next number.

## 1n-task-i.md (integration route)

```
# Task I: integrate <route> into one branch

Branch `<combined branch>` from `<start commit>`. Worktree `<path>`. Test databases `<prefix>_i_<package>_test`. Report file `<writable root>/reports/i.md`. You are the only agent that pushes. Do each phase only when the orchestrator's prompt names it; after each phase update the report and reply with only its path.

## Phase 1: combine (the prompt names the accepted branches)
1. Merge in this order with `git merge --no-ff`: <branch list>. Resolve conflicts by keeping both tasks' intent; list each resolution under decisions:.
2. Apply every `handoff:` line from the task reports, and the orchestrator handoffs below, test first.
3. Fix only integration breakage: a test that passed on its branch and fails on the combined branch.
4. Run every gate: <format, lint, type check, every test suite with your databases>.

## Re-merge (the prompt names a task branch and its new head after a fix round)
Merge that head with `git merge --no-ff`, apply any new handoff, run again the gates that cover the changed packages, update the report with the new combined head.

## Phase 1b and later: fixes from the final review (the prompt names the review file)
Fix every P0 and P1 on the combined branch, failing test first. New tests go in new files, never in a file that a running task replaces.

## Phase 2: add a late task (the prompt names its commit)
Merge it, run every gate again, update the report.

## Phase 3: push (only after the orchestrator's prompt says the final review passed and no branch review is pending)
1. Fetch with the credential helper. If `origin/<target>` is not `<start commit>` any more, stop and report `STATUS: blocked remote moved`.
2. Scan every outgoing commit for secrets, not only the net diff: a secret added in one commit and removed in a later one is still published. Run two scans in bash, each under `set -o pipefail`, and never print a patch or a matching line: a value printed once stays in the session transcript even when the push stops. Patches, merge diffs included; removed lines and file headers are skipped, so a commit that deletes a secret passes: `git log -p -m --format='commit %H' origin/<target>..HEAD | awk -v re='<secret patterns>' '/^commit [0-9a-f]+/ {c=$2; h=0; next} /^diff --git / {h=1; f=""; next} h && /^\+\+\+ / {f=substr($0,5); sub(/^b\//, "", f); next} /^@@/ {h=0; next} !h && /^\+/ && $0 ~ re {print c, f}' | sort -u`. Complete messages, one commit at a time: `git rev-list origin/<target>..HEAD | while read -r c; do git log -1 --format=%B "$c" | awk -v re='<secret patterns>' -v c="$c" '$0 ~ re {m=1} END {if (m) print c, "message"}' || exit 1; done`. Both print commit ids and file names (or `message`) only. Push only when both scans exited 0 and printed nothing. If a secret is found, do not push: report `STATUS: blocked secret in history` with the commit and file, never the value. A scan error, a missing tool or a bad pattern blocks the push the same way: `STATUS: blocked secret scan failed`. Check every new commit's author and trailers (`git log --format='%an <%ae>%n%b' origin/<target>..HEAD`: the expected identity, no trailers the shared rules forbid), then push as a fast-forward only (`git push origin <combined branch>:<target>`); never force.
3. Reply once on each review-bot thread with the fix commit or the reason. For people's threads, write each reply into a drafts file (thread id, path and line, text) and post only the file that the orchestrator's prompt names as approved, once per thread, after a check for replies that already exist.
4. Run the review-bot loop from the shared rules.

## Orchestrator handoffs
<fixes that span two tasks' files, each with its regression test>

## Never
Change a task's intent, edit application code outside a merge, a handoff or a review fix, force-push, or post anything the phases do not name.
```

Rules learned:
- Start the integrator when the first branches pass review, not at launch, and give it the merge order in the contract. Conflicts then happen in one place, in a known order. When the branches own disjoint files, the combine may start while their reviews run, with fix rounds brought in by the re-merge phase ([`flow-and-time.md`](flow-and-time.md), Work ahead).
- A push that opens a new pull request needs the user's go for both steps: the push and the new pull request. An approved dispatch plan or an interview answer is that go only when its text names both; a text that names only the push (the push-only policy) covers the push, and the pull request waits for its own yes. Copy the exact option text into `00-route.md`, and check that text, not a summary, before the push phase.
- Write a phase into the contract before its prompt, as a dated "Orchestrator amendments" section when the contract already exists; the integrator reads the contract, not the chat.

## 20-reviewer.md

```
# Reviewer contract, route <name>

You review the pull requests or local branches of <org>/<repo> that the implementers produce and, in an integration route, the integrator's combined branch against the base branch (the final review). You write no application code, push nothing, post nothing on GitHub. Your output is one report file per review.

## Must prove

<the Must-prove list from 00-route.md, quoted as written>

## CPU

Every check runs through `<route folder>/run-check.sh <command>`, one at a time on the whole machine, single-threaded (the same forms as the shared rules). A check that waits on the load gate for more than ten minutes: cancel it and write it under NOT CHECKED. Reuse a check result from an earlier review only when nothing that check reads has changed (its files, everything they import, including shared helpers and exported fixtures, its configuration, the lock file, the environment), and say so. Run again the checks of every consumer of a changed helper or fixture.

## Setup (once)
- Clone: <path>. Your worktree: <worktrees root>/r. Create it if missing: `git -C <clone> worktree add <worktrees root>/r --detach origin/<base>`.
- Remote git commands use the credential helper: `git -c credential.helper= -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' fetch origin`.
- `gh` runs as `GH_TOKEN=$(gh auth token --user <gh account>) gh ...`.
- Never change git identity, remotes or global config. Do not run `git push`.
- Tests: `TEST_DATABASE_URL=...<prefix>_r_test`; create once, drop and recreate between pull requests.

## Per review
Input: a pull request number, or a local branch and its head commit, and a report path, given in the prompt. A re-review, or a final review that a pending branch re-review folds into, also names earlier review reports and the findings in them to close; check each one.
1. Fetch, `git checkout --detach origin/<head branch>` (`gh pr view <n> --json headRefName,baseRefName,headRefOid`); for a local branch, `git checkout --detach <head commit>`. Record the head commit.
2. Changed files: `gh pr diff <n> --name-only`, or `git diff --name-only origin/<base>...<head commit>` for a local branch. The final review of an integration route uses the combined branch against the base branch and checks every task contract, the integrator contract and the paths between tasks.
3. React files (<paths>): run the `/react-review` skill on those files; skip the runtime layer.
4. All TypeScript in the diff: run the `/thermo-nuclear-code-quality-review` skill on the diff against the base branch.
5. Run the test commands from the PR body's Verification section with your database. A failing test is a P0.
6. Check the pull request against its task contract (<map of branch to contract file>) and the shared rules. An unmet requirement is at least a P1.

## Severity, report only these
- P0: wrong behaviour, data loss or corruption, a tenancy leak, a security hole, a failing or skipped test, a row-breaking migration, a hand-written migration or hand-written SQL that the task contract does not allow (name each allowed one here: <task, migration, why>).
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
CLOSED: <earlier report> <finding> | closed | open: <why> (one line per finding the prompt names; omit when it names none)
NOT CHECKED: <what this review could not check, for example remote CI, a live service or a GitHub state, one line each>
`VERDICT: pass` means zero P0, zero P1 and no named finding left open. Then reply in the terminal with only the path.
```

Rules learned:
- Give the reviewer its own worktree; it must not touch an implementer's or the integrator's worktree while that agent is mid-fix.
- The severity words must be defined in the contract; reviewers otherwise mix vocabularies (major, high, blocking). Gate on the `VERDICT:` line, not on adjectives.
- A skill named in the contract may not exist in the reviewer's harness. Check `ls ~/.claude/skills` before writing the contract, and keep a fallback line ("if the skill is missing, say so in the report and do a direct pass with the same checklist").
- One report per review round, numbered; never overwrite, so the fix history stays readable.
