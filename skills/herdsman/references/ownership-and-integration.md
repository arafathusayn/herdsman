# Ownership and one integrator

Parallel implementers in one repository collide on files, numbers and branches. Give each task exclusive ownership, keep every task local, and let one integrator combine, gate and publish. Conflicts then happen in one place, on purpose.

## Ownership

- Each task owns named files and line ranges. A change in another task's file is a `handoff:` line in the implementer's report, not an edit. When a branch cannot compile without a one-line change in another task's file, allow that line in the task file.
- New tests go in a new file per task, and a shared list (error codes, migrations, exports) takes one block per task, at the end. Merges then append instead of conflicting.
- Split a large change into parallel parts on disjoint files plus a wiring part on the combined branch.
- Name every GitHub mutation that a task may make. An implementer with latitude over base branches can dissolve a stack of pull requests on its own.

## One integrator

- In an integration route, implementers commit to local `wip/` branches and never touch GitHub. The integrator merges the accepted branches in a fixed order in its own worktree, applies every handoff, fixes only integration breakage, runs the full gates, and is the only agent that pushes, opens pull requests and answers review threads.
- The integrator works in phases, and each phase starts with an orchestrator prompt: combine, fixes from the final review, add a late task, push. A phase that the contract does not name yet goes into the contract first. Reason: the integrator reads its contract, not the chat, and the reviewer checks the result against the same text.
- The integrator can be an implementer whose task was accepted. Clear it first: integration is a new job, and the old task's context makes it fix things in its own old files.
- A finding whose fix spans two tasks' files (a column from one task, its writer in another task's file) cannot be fixed on either branch. Accept both branches and write the fix, with its regression test, into the integrator contract. The final review checks it.
- Review the combined head against the base branch before the push phase, and send its findings back to the integrator as a fix phase. Branch reviews do not see the paths between tasks ([`review-discipline.md`](review-discipline.md)).

## Fixing another author's pull request

- Branch every task from the exact head commit under review.
- The integrator fetches, stops if the remote branch moved, and pushes only as a fast-forward. It answers every review thread once, with the fix commit or the reason.

## Numbers and stacks

- Parallel tasks that change the schema from the same base all take the next migration and decision-record number. Merge them one at a time and send each remaining branch a rebase round that regenerates its migration.
- The cut point of a stacked pull request, after its base was squash-merged, is the base's head before the rebase, not a merge base against the moved local branch.
- Deleting a merged base branch closes every open pull request based on it: retarget the dependents first, and delete only when none is left ([`git-and-github.md`](git-and-github.md), Merges).
