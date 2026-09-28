# Contract templates

Contracts are the only channel from the orchestrator to a worker. A worker reads two files (shared rules, its task) and follows them. Write them so that a careful engineer with no chat history can act. Plain American English, short sentences, no em dashes, no names of people, no secrets.

## 10-shared-rules.md

```
# Shared rules for every worker

Repository: <absolute path of the checkout> (remote `origin` = GitHub <org>/<repo>). <one line on the stack and the package layout>. Read <the repo's agent guide, specs and ADRs> before coding.

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

## Push and pull request

- Push with the one-shot credential helper, never by changing the git identity or the remote:
  `git -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' push -u origin <branch>`
- Open a READY pull request: `GH_TOKEN=$(gh auth token --user <gh account>) gh pr create --repo <org>/<repo> --base <base> --head <branch> --title "<type>(<scope>): <title>" --body-file <file>`.
- PR body: `## Summary` (problem and change in plain words); one bullet per file or module with what changed and why; `## Verification` with the exact commands and databases; `## Known gaps` if anything is left. Never mention review rounds, reviewer or tool names, people's names, metrics or timings, process history. Reference issues by number.
- After the pull request is open, wait five minutes, then read `gh pr checks <n>` and the review-bot comments. Fix real findings test-first, push, reply on each thread with "Addressed in <sha>". Repeat until checks pass and the bot reports no new issues. If the bot is wrong, reply with the reason and move on.
- Do not change a pull request's base branch, dissolve a stack, force-push over someone else's commits, or close a pull request without an instruction in your task file.

## Report

When done, write `<writable root>/reports/<task>.md` with, in this order: `STATUS: complete` or `STATUS: blocked <reason>`; branch; worktree; PR URL; head commit; the exact test commands and their pass counts; `decisions:` (every judgement call); `gaps:`. Under 60 lines. Then reply in the terminal with only the path of that file.
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
<what the worker must not touch, including sibling tasks' files>

## Overlap
<open pull requests that touch the same files; what to do: read, do not depend, note "rebase after PR n merges" under Known gaps>

## Acceptance
- <observable checks a reviewer can repeat>
```

Rules learned:
- Name every constant and rule explicitly (`IMPORT_PRICE_CHANGE_FLAG_RATIO = 0.10`), or the worker invents its own.
- Say where sibling work overlaps; otherwise two workers edit the same migration number or the same module.
- State which GitHub mutations are allowed. A worker given "authorized base change" latitude dissolved a stack and retargeted a pull request on its own.
- Put the coverage rule on changed files only.
- Give every brief a `Due:` line and pass the same time to the waiter as `HERDSMAN_DUE_<name>`. The line alone does not stop a worker: workers ignored stop times written in their own briefs, so the waiter's OVERDUE event is what triggers the stop-and-report steer.
- When two or more tasks change the database schema, say in the shared rules that generated migration numbers will collide across branches and that the pull request merged second regenerates its migration history after a rebase onto main (seen 2026-09-22: three open branches all held 0014 and 0015).

## 20-reviewer.md

```
# Reviewer contract, route <name>

You review pull requests of <org>/<repo> that the implementation workers open. You write no application code, push nothing, post nothing on GitHub. Your output is one report file per review.

## Setup (once)
- Clone: <path>. Your worktree: <worktrees root>/r. Create it if missing: `git -C <clone> worktree add <worktrees root>/r --detach origin/<base>`.
- Remote git commands use the credential helper: `git -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' fetch origin`.
- `gh` runs as `GH_TOKEN=$(gh auth token --user <gh account>) gh ...`.
- Never change git identity, remotes or global config. Do not run `git push`.
- Tests: `TEST_DATABASE_URL=...<prefix>_r_test`; create once, drop and recreate between pull requests.

## Per review
Input: a pull request number and a report path, given in the prompt.
1. Fetch, `git checkout --detach origin/<head branch>` (`gh pr view <n> --json headRefName,baseRefName,headRefOid`). Record the head commit.
2. Changed files: `gh pr diff <n> --name-only`.
3. React files (<paths>): run the `/react-review` skill on those files; skip the runtime layer.
4. All TypeScript in the diff: run the `/thermo-nuclear-code-quality-review` skill on the diff against the base branch.
5. Run the test commands from the PR body's Verification section with your database. A failing test is a P0.
6. Check the pull request against its task contract (<map of branch to contract file>) and the shared rules. An unmet requirement is at least a P1.

## Severity, report only these
- P0: wrong behaviour, data loss or corruption, a tenancy leak, a security hole, a failing or skipped test, a hand-written or row-breaking migration.
- P1: wrong behaviour in an edge case the contract names, changed behaviour without a test, an unmet contract requirement, a maintainability problem that will cost within the next few stories, a React effect that belongs in render or in an event handler and causes a visible bug or a needless network call.
- Everything below P1 is left out entirely. No style notes, no naming notes, no optional refactors.

## Report
Write the file at the path given in the prompt. Under 80 lines:
VERDICT: pass | fix
PR: <number> <head branch> <head commit>
TESTS: <command> -> <pass>/<total> (one line per command)
FINDINGS:
- P0 | <file>:<line> | <what is wrong and what happens> | fix: <one sentence>
- P1 | ...
`VERDICT: pass` means zero P0 and zero P1. Then reply in the terminal with only the path.
```

Rules learned:
- Give the reviewer its own worktree; it must not touch a worker's worktree while the worker is mid-fix.
- The severity words must be defined in the contract; reviewers otherwise mix vocabularies (major, high, blocking). Gate on the `VERDICT:` line, not on adjectives.
- A skill named in the contract may not exist in the reviewer's harness. Check `ls ~/.claude/skills` before writing the contract, and keep a fallback line ("if the skill is missing, say so in the report and do a direct pass with the same checklist").
- One report per review round, numbered; never overwrite, so the fix history stays readable.
