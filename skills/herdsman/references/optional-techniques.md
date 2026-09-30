# Optional techniques

These techniques are off unless the user picks them in the interview (step 1). Each one adds a step, so offer it when it fits the route: its size, its risk, and the user's time. They work with any harness and any model.

## Plan gate

- What: the implementer writes its plan before it writes code, and waits for approval.
- When: a task with high risk (schema, security, money, a public API) or an approach that the contract cannot fix in advance.
- How: the first prompt asks for the plan only (files, approach, the tests it will write first, risks) in the report file with `STATUS: plan`, then a stop. The waiter reports it as REPORT. The orchestrator checks the plan against the contract and the Must-prove list, or sends it to a reviewer (`review-plan-<x>-<k>.md`). On approval, prompt "Plan approved, implement it" as a follow-up (compact, do not clear).
- Cost: one round trip per task. A change of course costs an edit to a document, not a fix round.

## Contract pre-review

- What: a reviewer reads the route's contracts before any agent starts.
- When: a route with many tasks, a new repository, or a spec from another team.
- How: give the reviewer the goal, the Must-prove list, the shared rules, every task file, the reviewer contract and the spec. It reports P0 and P1 only: contradictions between files, file ownership that overlaps, acceptance lines that cannot pass, and environment facts that the project refuses (for example a database name that a reset guard rejects). Report path `<route folder>/reports/review-contracts-<k>.md` (the waiter reports it as REVIEW). Fix the contracts, then launch.
- Cost: one review and its quota before the first line of code.

## Progress file

- What: the agent keeps a checklist of its brief in a file while it works.
- When: long tasks, and tasks that can outlive one context window.
- How: the brief asks the agent to keep `reports/progress-<x>.md`: each numbered step of its brief marked done or open, the current step, and the last command that passed. The orchestrator reads it on OVERDUE or STALL instead of the pane. After a compaction or a restart, the agent resumes from it. The waiter does not watch this file.
- Cost: a few writes per task.

## Locked proof tests

- What: a fix round must not weaken the test that proves the fix.
- When: bug fixes and review fix rounds where a failing test is the evidence.
- How: the fix brief names the proving test files. The re-review compares them between the old and the new head (`git diff <old> <new> -- <test files>`) and reports a removed or loosened assertion as a P0. If the harness supports edit hooks, the user can add a hook that blocks edits to those files during the round; this is the user's configuration, never the agent's.
- Cost: one diff per re-review.

## Visual check

- What: the implementer and the reviewer compare the running screen with the design, not with a description of it.
- When: user interface tasks with design images or mockups.
- How: the task file lists the design image paths and the screens to compare. The implementer runs the app, takes a screenshot of each screen with the harness's browser tool, compares it with the design, and lists the screenshot paths in its report. The reviewer opens the same screens.
- Cost: a running app, a browser tool in each harness, and time per screen.

## Guide feedback

- What: a mistake that repeats becomes a rule in the target repository's agent guide (the file its agents read at the start of every session).
- When: the same class of finding appears in two tasks or two rounds of one route, or a review shows that the guide is out of date.
- How: write one short rule with its reason and show it to the user. On the user's go, the implementer or the integrator commits it in a separate commit with the fix.
- Cost: a change to the target repository outside the task's scope. It needs the user's go each time.
