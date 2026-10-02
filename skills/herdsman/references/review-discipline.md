# Review discipline and decisions

A review is useful only when it blocks the right things, proves them, and leads to a decision. The reviewer finds; the orchestrator decides by the goal; the user decides what the goal cannot.

## What a review reports

- P0 and P1 only, with the rule stated first in the reviewer contract. Everything below P1 is left out, not demoted.
- Each finding carries the file and line, what happens, and a one-sentence fix, so the implementer or the integrator can act without the reviewer's reasoning.
- A reviewer that reproduces each finding with a throwaway script against real services and the real entry point gives the implementer a clear target. Ask reviewers to reproduce, not only to read: reviewers also report defects that do not exist. Record each refuted finding with its reason in the route file and the reviewer contract, so that later reviews do not raise it again; keep each reproduction as the check for its fix.
- Prefer a reviewer from another model family than the implementers'. It does not share their blind spots.
- Give the reviewer a list of silent successes to look for: an assertion that cannot fail, a test that pins a count instead of the property, a suite that proves only refusals and never the allowed path, a test script that no gate runs, skipped tests, exit 0 with zero rows processed, a misspelled config key that the tool ignores, a citation that does not resolve. A check names the path it exercised: a validation that stops before the risky branch leaves that branch unproven.
- After each fix wave, also compare the public surface (API description, exported types) with the previous head.
- Pair a security fix with a separate tests-only task, written test first against the unfixed base. It reads the problem without the fix's assumptions and can find a second path to the same data that the fixing task missed.
- Brief a reviewer without your conclusions: the goal, the hard requirements ("do not question them"), a block of facts already verified (so it spends its effort elsewhere), what to read, and findings with a guarantee per fix, not a rewrite. A review anchored on the orchestrator's reasoning returns that reasoning, and the orchestrator's own design is where it is blind. After the fix, send the same reviewer a numbered list of what changed, keyed to its finding numbers: it checks the code, not the list.
- A re-review after a fix round covers the diff since the last reviewed head plus the findings it must close, and carries the other verdicts forward. Say so in the prompt. It takes minutes instead of a full review.
- A report written before a steer landed is stale. Every steer prompt asks for the report to be written again, also when the steer needs no code change (then the report says so and names the unchanged head): the waiter sees only a changed report, so a steer answered without one leaves the route waiting. Hold the review until the waiter's REPORT-UPDATED, and check that the report answers the steer and names the head to review.

## Review the combined result too

- Branch reviews check each task against its own contract, so they miss defects on paths between tasks (a logging path, a stored-data read, an error handler that two tasks both touch). Always run a full review of the combined head against the base branch before anything is pushed.
- A finding about a value that several tasks send (an organization id, a trace id, an event name) is checked in every task's branch, not only in the reviewed one. Amend every contract that sends the value with one rule, so that the combined branch agrees with itself. A branch that already passed and is in the combined branch does not read its contract again: its part of the fix goes to the integrator as a handoff, with a regression test.
- Send the final review's fixes to the integrator on the combined branch, test first. Tell it where new tests must not go when a parallel task replaces a file.
- When a final review finds a leak of private data, ask the fix to list every boundary where that data enters storage, errors or logs (procedure input, insert, send, webhook, read, shared error handlers) and to test each one. A fix of only the reported line moves the leak to the next boundary, one review round at a time. For a command-line entry point, name every exit in the brief: argument parsing, missing settings, imports that validate settings when they load, top-level awaits, unhandled rejections, each explicit exit.
- Tell the final reviewer to test the spec's premises too ("the field is already stripped", "the same rows come again later"), not only the code against the spec. Task reviews that all pass can still ship a false premise.
- A diff review does not see old code that ships with the change. For a privacy or security goal, the final review reads the whole code of the touched entry points, not only the diff.
- Reviewer contracts often exclude findings that already exist on the base. When the base is the pull request's own unmerged head (a fix route on an open pull request), that code ships too: read the NOT CHECKED and excluded lines of every review and bring such items into scope yourself. Read them before you accept any pass, too: a reviewer that could run no gates may say so only there.
- A fix that would change code on the base branch that the whole app shares (a shared error logger, a global handler) is a `handoff:` line, not part of the pull request. The orchestrator decides the scope, gives it to the user as a separate item, and tells the next reviewer that it is a decision.

## What a fix must show

- A failing test on the code before the fix, then the fix.
- The full fix in the first brief. A remaining defect written into the pull request as a known gap does not close a P1; the reviewer reproduces it again.
- Before you design a fix for a linter or tool warning, read the rule's own source: it shows when the warning fires and the smallest correct fix.
- When a fix repairs one instance of a repeated pattern (one control of several with the same defect), search the same file and its siblings for the other instances before the round starts. Otherwise the next check finds the next instance, one round at a time.
- A fix for a test that leaks state into later test files is proven by pairing: the leaking file and one failing file in one process, before and after the fix. A whole-package run on another machine can order the files differently and hide the leak. Find the leaking test by narrowing with the runner's name filter.

## The goal gate

- Keep a Must-prove list of the goal's real paths in the route file. Check each fix line against it before an implementer or the integrator gets the fix.
- Never forward a fix that skips, mocks or gates a Must-prove path, or that needs a credential the user does not already use. A hermeticity finding forwarded as "skip the live test" leaves the goal's path unproven.
- A screen, step, control or state that the design sources do not show is a product decision. It belongs to the user, not to an implementer, a reviewer or the orchestrator. Put "every screen, step and control traces to the design; anything else is a GOAL-CONFLICT" in the Must-prove list. Before such a branch ships, send the user one flag list: what differs, screenshots of the built screen and the design, the options, and a recommendation. Ship only what the user approves. Text changes that keep the copy true are allowed; list the visible ones too.

## Orchestrator decisions

- A reviewer flags every change from the task file as a P1, also an implementer's reasoned change of a rule that the orchestrator wrote. When the change serves the goal better, write a dated "Orchestrator amendments" section at the end of the task file, forward only the real findings, and say in the re-review prompt which findings the amendments close.
- A reviewer's fix line can undo a decision that the implementer recorded for a good reason. Rule with a narrower fix that satisfies both, write it as an amendment, and name it in the fix prompt and in the re-review prompt.
- Check each fix line, not only each finding: a reviewer's suggested fix can be incomplete or too wide.
- A note such as "run X again after Y" enforces nothing. Make it a failing check, or an open item in the route file with an owner.
- A rule that the orchestrator wrote into a contract is a design claim, and a review can prove it wrong. When a contract line conflicts with a safety or audit rule (for example "write no row of any kind" against an audit log that must record every read), the safety rule wins: amend the contract and ask for a test that pins the corrected rule, never code that hides the audit record. A rule of the form "skip it now, it comes again later" (a deduplication ledger, a retry window) needs an acceptance test that proves the later read happens.
- A finding that comes from the orchestrator's own extra gate (a rule it added to a contract, a test skip that was there before) gets a ruling in the task contract and in the reviewer contract. With the ruling in one file only, the next re-review raises it again.
- A first report with `STATUS: gaps` gets one numbered amendment: a ruling per gap (fix it, or a decision with its reason), one prompt and a new due time. A user message for that agent that arrives meanwhile becomes the next numbered item.
- Decide conflicts by the user's recorded rules. When a provider's contract disagrees with its own examples, the contract wins; pin the conflict in a test and hand it to the user for the provider.
- A rule that the user gives in the middle of a route overrides the contract: put it in the next prompt and amend the contract, or the next job repeats the old form. For a running agent, amend the contract first and then send a one-line steer that points to the amendment; do not restart the task. An idle agent gets it as a short note that ends "no task now, reply only 'noted'", so that the note does not start work.
- Send a disagreement to the user at once when an implementer disagreed with a finding and the reviewer raises it again. After three fix rounds on one branch, stop and show the user the remaining findings.

## Beyond the local review

- On every pull request check, read the human reviews and comments too, including reviews in the COMMENTED state, and bring them to the user. A later approval from the same person does not answer the earlier comments.
- Before any reply that says a pull request is open or waits on someone, read its live state (`gh pr view <n> --json state,mergedAt,reviews,comments`). The user and teammates approve and merge outside the session, without a word to it.
- Before you call a pull request ready, read the base branch rules: a required approval that the author cannot give leaves it blocked, and the user chooses the path.

## Reviewer hygiene

- A reviewer works in its own detached worktree with its own test databases, named in its prompt. It never uses `git stash`: the stash is shared across worktrees.
- The reviewer writes a report file and changes nothing else: no code, no commits, no GitHub.
