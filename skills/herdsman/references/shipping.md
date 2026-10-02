# Shipping without pull requests

Some routes ship reviewed commits straight to the default branch, and from there to a deploy branch, with no pull request. The review loop stays the same. The gate before each push is stricter, because nothing else stands between the commit and the users.

## The ship gate

- A branch ships only after the reviewer's `VERDICT: pass` on its head.
- When the base branch moved since the branch's last merge, the implementer merges the base and runs every suite on the merged head before the push. A clean merge check is not a tested result: two files can merge without a conflict and still break each other.
- The exact commit to push passes the tests in the same form as CI: the same command, the whole package in one process, the same entry point. Reason: a test that leaks a global (a fake clock, a timer spy) into later files passes in a partial local run and fails in CI.
- Keep the evidence (command, commit, pass counts) in the ship-prep report. No evidence, no push.
- After each push, read CI for that commit within about ten minutes, every expected check. A red check on the default branch is the next task, before any new ship.
- When a branch's last fix is small, run its re-review and its ship prep at the same time. Merging the reviewed base is not a review concern, and a late finding is fixed on top of the merged head. This saves one full suite cycle.
- Before each deploy push, list the new migration files between the deploy branch and the commit. An additive schema change (a new column with a default) goes to the deployed database while the ship-prep suites run, before the push, so that the new server finds it. It needs the user's go unless the route's grant names it.

## Who pushes

- The implementer or the integrator pushes. A harness guard can refuse an agent's push to the default branch. Then the agent commits and writes the exact push command in its report (`push-command:`), and the user runs it or grants the orchestrator the push of reviewed commits for the route.
- Push by commit id, as a fast-forward only. A command on the clipboard can be pasted again later; a fast-forward push of a fixed commit is refused when it is stale and changes nothing.
- Before each push, check that the commit contains the remote default branch (`git merge-base --is-ancestor origin/<default> <sha>`). Never rebase in a worktree where an agent still edits; push the commit from a separate clean worktree.
- When two stacked tasks ship together, push the top branch only: the rebase drops the commits that are already on the default branch.
- A deploy branch that mirrors the default branch moves to the same commit by a fast-forward push. A history rewrite on a protected branch is the user's decision each time, even when a standing rule implies it.

## Acceptance on the deployed app

- After each deploy, the orchestrator checks the shipped change in the deployed app as a user would (a walk-through, not the test suites). This check is the acceptance gate: an agent's own tests and screenshots can pass while the deployed app shows raw server codes, false copy or slow steps. Send each defect to the agent that owns the area, in its current turn when its harness allows that.
- Test DOM environments do no layout. For a table, header or step-bar change, the contract asks for a screenshot at a named width with measured edges (a cell's right edge left of the next cell, a label on one line), and the deployed check looks at those parts first.
- For a speed defect, read the timings in the deployed server log first, then give the fix task a test that counts database round trips. Per-row queries look fast on a local database and slow on a remote one.
- To prove that a deploy serves a change when no browser is available, fetch the app's built script chunks and search them for a new string.
- When a delivered email or message cannot be seen, report the delivery as unconfirmed with the server evidence and ask the user to look. Never open the user's other accounts.

## The user's browser

- Open your own background tab for each check and close it after. Never resize, navigate or reuse the user's tabs or window; to check a width, set the viewport of your own tab only.
- While the user is in a live call or a demo, keep checks read-only: no saves and no setting changes on the user's account. Do not use demo data for test runs; create your own test record.
- After each save in a third-party dashboard, reload the page and read the value back. A refused save can show only as a short notice.
