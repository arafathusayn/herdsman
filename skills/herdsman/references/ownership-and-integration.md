# Ownership and one integrator

Parallel implementers in one repository collide on files, numbers and branches. Give each task exclusive ownership, keep every task local, and let one integrator combine, gate and publish. Conflicts then happen in one place, on purpose.

## Ownership

- Each task owns named files and line ranges. A change in another task's file is a `handoff:` line in the implementer's report, not an edit. When a branch cannot compile without a one-line change in another task's file, allow that line in the task file.
- New tests go in a new file per task, and a shared list (error codes, migrations, exports) takes one block per task, at the end. Merges then append instead of conflicting.
- Split a large change into parallel parts on disjoint files plus a wiring part on the combined branch.
- Name every GitHub mutation that a task may make. An implementer with latitude over base branches can dissolve a stack of pull requests on its own.
- Two agents on one feature: fix the shared module's contract (paths, names, signatures, defaults) in the task file. One agent creates it as its first commit and writes `commit1: <sha>` in its report at once; the other codes against that contract and merges that commit only on the orchestrator's prompt. At the end the first agent merges the second branch, and one review covers the result.
- A fix round with independent parts (review findings in some files, a new service in others) goes faster split by files across two free implementers: part A on the task branch, part B on a new branch from the same head in another clean worktree. Part A merges part B on the orchestrator's prompt, and one re-review covers the combined head.
- A task based on an unshipped feature branch misses everything that reached the default branch after that branch started. Check `git merge-base --is-ancestor origin/<default> <base>` before you name a base, and when it fails, the contract says "merge origin/<default> before step N" (lock-file conflicts: keep both sides, then regenerate the lock file with the package manager).

## One integrator

- In an integration route, implementers commit to local `wip/` branches and never touch GitHub. The integrator merges the accepted branches in a fixed order in its own worktree, applies every handoff, fixes only integration breakage, runs the full gates, and is the only agent that pushes, opens pull requests and answers review threads.
- The integrator works in phases, and each phase starts with an orchestrator prompt: combine, fixes from the final review, add a late task, push. A phase that the contract does not name yet goes into the contract first. Reason: the integrator reads its contract, not the chat, and the reviewer checks the result against the same text.
- The integrator can be an implementer whose task was accepted. Clear it first: integration is a new job, and the old task's context makes it fix things in its own old files.
- A finding whose fix spans two tasks' files (a column from one task, its writer in another task's file) cannot be fixed on either branch. Accept both branches and write the fix, with its regression test, into the integrator contract. The final review checks it.
- Review the combined head against the base branch before the push phase, and send its findings back to the integrator as a fix phase. Branch reviews do not see the paths between tasks ([`review-discipline.md`](review-discipline.md)).

## Fixing another author's pull request

- Branch every task from the exact head commit under review.
- The integrator fetches, stops if the remote branch moved, and pushes only as a fast-forward. It answers every review thread once, with the fix commit or the reason.
- When the user reads texts before they are posted, split the push phase in two. First: push, write every reply into one drafts file (thread id, path and line, text), and run the review-bot loop without posting. Then, after the user's go: post exactly the approved file, once per thread, after a check for replies that already exist. Reason: the push was approved in the dispatch plan, the texts were not, and a posted reply cannot be taken back.
- A fix that belongs in the pull request description (for example a migration range) is not the integrator's: the orchestrator edits the description on the user's go, then the integrator posts the reply.

## Numbers and stacks

- Parallel tasks that change the schema from the same base all take the next migration and decision-record number. Merge them one at a time and send each remaining branch a rebase round that regenerates its migration.
- The cut point of a stacked pull request, after its base was squash-merged, is the base's head before the rebase, not a merge base against the moved local branch.
- Deleting a merged base branch closes every open pull request based on it: retarget the dependents first, and delete only when none is left ([`git-and-github.md`](git-and-github.md), Merges).
