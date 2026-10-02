# Review discipline and decisions

A review is useful only when it blocks the right things, proves them, and leads to a decision. The reviewer finds; the orchestrator decides by the goal; the user decides what the goal cannot.

## What a review reports

- P0 and P1 only, with the rule stated first in the reviewer contract. Everything below P1 is left out, not demoted.
- Each finding carries the file and line, what happens, and a one-sentence fix, so the implementer or the integrator can act without the reviewer's reasoning.
- A reviewer that reproduces each finding with a throwaway script against real services and the real entry point gives the implementer a clear target. Ask reviewers to reproduce, not only to read.
- Pair a security fix with a separate tests-only task, written test first against the unfixed base. It reads the problem without the fix's assumptions and can find a second path to the same data that the fixing task missed.

## Review the combined result too

- Branch reviews check each task against its own contract, so they miss defects on paths between tasks (a logging path, a stored-data read, an error handler that two tasks both touch). Always run a full review of the combined head against the base branch before anything is pushed.
- Send the final review's fixes to the integrator on the combined branch, test first. Tell it where new tests must not go when a parallel task replaces a file.
- When a final review finds a leak of private data, ask the fix to list every boundary where that data enters storage, errors or logs (procedure input, insert, send, webhook, read, shared error handlers) and to test each one. A fix of only the reported line moves the leak to the next boundary, one review round at a time.
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
- A finding that comes from the orchestrator's own extra gate (a rule it added to a contract, a test skip that was there before) gets a ruling in the task contract and in the reviewer contract. With the ruling in one file only, the next re-review raises it again.
- A first report with `STATUS: gaps` gets one numbered amendment: a ruling per gap (fix it, or a decision with its reason), one prompt and a new due time. A user message for that agent that arrives meanwhile becomes the next numbered item.
- Decide conflicts by the user's recorded rules. When a provider's contract disagrees with its own examples, the contract wins; pin the conflict in a test and hand it to the user for the provider.
- A rule that the user gives in the middle of a route overrides the contract: put it in the next prompt and amend the contract, or the next job repeats the old form. For a running agent, amend the contract first and then send a one-line steer that points to the amendment; do not restart the task.
- Send a disagreement to the user at once when an implementer disagreed with a finding and the reviewer raises it again. After three fix rounds on one branch, stop and show the user the remaining findings.

## Beyond the local review

- On every pull request check, read the human reviews and comments too, and bring them to the user. A later approval from the same person does not answer the earlier comments.
- Before you call a pull request ready, read the base branch rules: a required approval that the author cannot give leaves it blocked, and the user chooses the path.

## Reviewer hygiene

- A reviewer works in its own detached worktree with its own test databases, named in its prompt. It never uses `git stash`: the stash is shared across worktrees.
- The reviewer writes a report file and changes nothing else: no code, no commits, no GitHub.
