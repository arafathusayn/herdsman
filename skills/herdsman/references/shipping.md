# Shipping without pull requests

Some routes ship reviewed commits straight to the default branch, and from there to a deploy branch, with no pull request. The review loop stays the same. The gate before each push is stricter, because nothing else stands between the commit and the users.

## The ship gate

- A branch ships only after the reviewer's `VERDICT: pass` on its head.
- When the base branch moved since the branch's last merge, the implementer merges the base and runs every suite on the merged head before the push. A clean merge check is not a tested result: two files can merge without a conflict and still break each other.
- The exact commit to push passes the tests in the same form as CI: the same command, the whole package in one process, the same entry point. Reason: a test that leaks a global (a fake clock, a timer spy) into later files passes in a partial local run and fails in CI.
- Keep the evidence (command, commit, pass counts) in the ship-prep report. No evidence, no push.
- After each push, read CI for that commit within about ten minutes, every expected check. A red check on the default branch is the next task, before any new ship.
- When a branch's last fix is small, run its re-review and its ship prep at the same time. Merging the reviewed base is not a review concern, and a late finding is fixed on top of the merged head. This saves one full suite cycle.
- When the server and the web (or any client) deploy as separate services, a payload whose shape changes must work in BOTH deploy orders: the old client on the new server (the new server keeps the old fields for one release, then a later release removes them) and the new client on the old server (the new client also accepts the old shape). A browser tab left open keeps the old client until a reload. Ask for one test per order that feeds the unchanged other side's raw output; branch reviews tend to check only one order.
- Before each deploy push, list the new migration files between the deploy branch and the commit. Every migration is additive only (a new table, a nullable column, a new column with a default; no drop, rename, column type change, NOT NULL without a default or new constraint on an existing table), because the old server runs on the new schema until the deploy ends. An additive schema change goes to the deployed database while the ship-prep suites run, before the push, so that the new server finds it. It needs the user's go unless the route's grant names it.
- Run the migration step on every delivery, also when the pull request has no migration file, because the branch can carry a teammate's unapplied migration. Run it only with the user's go or the route's grant; without either, give the user the exact command. Check first that the deploy path applies migrations at all (some setups migrate only in a staging workflow). When a server started before its migration landed, redeploy it and poll its health URL until it answers, because it keeps the failed first query.

## Who pushes

- The implementer or the integrator pushes. A harness guard can refuse an agent's push to the default branch. Then the agent commits and writes the exact push command in its report (`push-command:`), and the user runs it or grants the orchestrator the push of reviewed commits for the route.
- Push by commit id, as a fast-forward only. A command on the clipboard can be pasted again later; a fast-forward push of a fixed commit is refused when it is stale and changes nothing.
- Before each push, check that the commit contains the remote default branch (`git merge-base --is-ancestor origin/<default> <sha>`). Never rebase in a worktree where an agent still edits; push the commit from a separate clean worktree.
- When two stacked tasks ship together, push the top branch only: the rebase drops the commits that are already on the default branch.
- A deploy branch that mirrors the default branch moves to the same commit by a fast-forward push. A history rewrite on a protected branch is the user's decision each time, even when a standing rule implies it.
- A delivery script that runs as a background command gets the PATH that the command line gives it, not the user's shell PATH. At the top of the script, check each tool it calls with `command -v` and stop when one is missing, and use `set -o pipefail`. Reason: a step written as `(tool ... | tail -3) || stop` passes when the tool is not found, because the pipe returns the status of `tail`. A migration step then reports "up to date" without having run, and a deploy wait loops on an empty status. When the deploy status cannot be read, prove the deploy by the version that the deployed app itself reports.

## Release by hand

When CI cannot deploy (a billing stop, no minutes), the release goes out by hand, by whoever the route's grant names, and the ship gate above still applies.

- Before the first deploy, record each service's current deployment id as a rollback target and read a baseline of the live state (read-only calls). Reason: a rollback is a redeploy of the recorded ids, and without a baseline the release cannot be proven.
- Deploy from a clean checkout of the merged commit, with the services in dependency order (a service that another one calls goes first). Reason: a clean checkout ships exactly the merged code, and a service that is called must run the new code before its caller does.
- Compare the live state with the committed source (workflows, configuration) from a git ref before the deploy and again after it, and read the baseline again after it. Reason: the first comparison proves the baseline, and the second proves that the live state equals the merged commit.
- Publish the release record last, because publishing it deploys nothing and must name only what runs.

## Acceptance on the deployed app

- After each deploy, the orchestrator checks the shipped change in the deployed app as a user would (a walk-through, not the test suites). This check is the acceptance gate: an agent's own tests and screenshots can pass while the deployed app shows raw server codes, false copy or slow steps. Send each defect to the agent that owns the area, in its current turn when its harness allows that.
- Test DOM environments do no layout. For a table, header or step-bar change, the contract asks for a screenshot at a named width with measured edges (a cell's right edge left of the next cell, a label on one line), and the deployed check looks at those parts first.
- For a speed defect, read the timings in the deployed server log first, then give the fix task a test that counts database round trips. Per-row queries look fast on a local database and slow on a remote one.
- To prove that a deploy serves a change when no browser is available, fetch the app's built script chunks and search them for a new string.
- To check the old client on the new server for real: before the push, load the page in your own tab, open the screen under test, and note the page's script bundle name. After the deploy, use that tab without a reload, then compare its bundle name with a freshly loaded tab. Two different names prove that the old client ran against the new server.
- When a delivered email or message cannot be seen, report the delivery as unconfirmed with the server evidence and ask the user to look. Never open the user's other accounts.
- A fix that runs in a function deployed per site (one copy per customer or school) ships every change merged since that site's deployed commit. Before the deploy, diff the function's inputs since that commit and prove that the site's stored settings parse with the new code; after it, make one supervised run. A sibling function at the same site proves nothing unless it has actually run there.
- An acceptance check on rows that a one-time backfill wrote proves the backfill and the read path, not the new write path. Schedule a check on the first rows the new code writes.

## Rollback

- Roll back the code only. Do not reverse additive migrations, clear new columns or remove new settings, because the old build ignores them, and removing a setting only makes the new routes fail when the new code runs again.
- Plan for the state that stays after the rollback: new columns, rows written in the new shape, and data held by outside services (a chat memory, a stored checkpoint).

## The user's browser

- Open your own background tab for each check and close it after. Never resize, navigate or reuse the user's tabs or window; to check a width, set the viewport of your own tab only.
- While the user is in a live call or a demo, keep checks read-only: no saves and no setting changes on the user's account. Do not use demo data for test runs; create your own test record.
- After each save in a third-party dashboard, reload the page and read the value back. A refused save can show only as a short notice.
