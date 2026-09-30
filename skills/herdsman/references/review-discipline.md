# Review discipline and decisions

A review is useful only when it blocks the right things, proves them, and leads to a decision. The reviewer finds; the orchestrator decides by the goal; the user decides what the goal cannot.

## What a review reports

- P0 and P1 only, with the rule stated first in the reviewer contract. Everything below P1 is left out, not demoted.
- Each finding carries the file and line, what happens, and a one-sentence fix, so the implementer or the integrator can act without the reviewer's reasoning.
- A reviewer that reproduces each finding with a throwaway script against real services and the real entry point gives the implementer a clear target. Ask reviewers to reproduce, not only to read.

## Review the combined result too

- Branch reviews check each task against its own contract, so they miss defects on paths between tasks (a logging path, a stored-data read, an error handler that two tasks both touch). Always run a full review of the combined head against the base branch before anything is pushed.
- Send the final review's fixes to the integrator on the combined branch, test first. Tell it where new tests must not go when a parallel task replaces a file.

## What a fix must show

- A failing test on the code before the fix, then the fix.
- The full fix in the first brief. A remaining defect written into the pull request as a known gap does not close a P1; the reviewer reproduces it again.
- Before you design a fix for a linter or tool warning, read the rule's own source: it shows when the warning fires and the smallest correct fix.

## The goal gate

- Keep a Must-prove list of the goal's real paths in the route file. Check each fix line against it before an implementer or the integrator gets the fix.
- Never forward a fix that skips, mocks or gates a Must-prove path, or that needs a credential the user does not already use. A hermeticity finding forwarded as "skip the live test" leaves the goal's path unproven.

## Orchestrator decisions

- A reviewer flags every change from the task file as a P1, also an implementer's reasoned change of a rule that the orchestrator wrote. When the change serves the goal better, write a dated "Orchestrator amendments" section at the end of the task file, forward only the real findings, and say in the re-review prompt which findings the amendments close.
- Decide conflicts by the user's recorded rules. When a provider's contract disagrees with its own examples, the contract wins; pin the conflict in a test and hand it to the user for the provider.
- A rule that the user gives in the middle of a route overrides the contract: put it in the next prompt and amend the contract, or the next job repeats the old form.
- Send a disagreement to the user at once when an implementer disagreed with a finding and the reviewer raises it again. After three fix rounds on one branch, stop and show the user the remaining findings.

## Beyond the local review

- On every pull request check, read the human reviews and comments too, and bring them to the user. A later approval from the same person does not answer the earlier comments.
- Before you call a pull request ready, read the base branch rules: a required approval that the author cannot give leaves it blocked, and the user chooses the path.

## Reviewer hygiene

- A reviewer works in its own detached worktree with its own test databases, named in its prompt. It never uses `git stash`: the stash is shared across worktrees.
- The reviewer writes a report file and changes nothing else: no code, no commits, no GitHub.
