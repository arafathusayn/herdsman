# Flow and time

The orchestrator owns time and capacity. Agents do not keep their own deadlines, and the slowest stage of a route moves from coding to review to integration. Plan for the stage that is slow now, not for the one that was slow at launch.

## The orchestrator enforces time

- Every prompt, first task and fix round alike, carries a due time, and the waiter gets it as `HERDSMAN_DUE_<name>`. Reason: agents ignore stop times written in their own briefs.
- Past the due time, queue a stop-and-report steer, then send the interrupt key if no report comes. Reason: a queued steer lands only when the current turn ends, and a long turn may not end.
- Size due times from the durations seen on the route, so that OVERDUE means something. A due time far beyond the real duration never fires.
- A reviewer can have a due time too: add its name to the waiter's `HERDSMAN_IMPLEMENTERS` list with `HERDSMAN_TASK_<name>=review-<pr>-<k>`.

## The bottleneck moves

- Review rounds, not coding, cost the most. Keep one review per artifact per round, report P0 and P1 only, and gate on a verdict line.
- When implementers finish fast, the reviewers become the queue: add a reviewer, or run reviews that do not depend on each other at the same time (for example the final review of a combined branch while a test-only task runs; the test-only task gets its own review).
- When an integrator waits for CI, CI becomes the critical path: run reviews while CI runs, and review a pushed head before its checks end.
- When the only pushing agent is busy, do not stall the reviewer: review the implementer's local wip branch. If the published head equals the reviewed commit, no second review is needed.

## Work ahead, but only on safe ground

- Write the next wave's contracts while the integrator combines, so implementers start the moment its report lands.
- When the integrator's next merge is free of conflicts, branch the next part from the implementer's wip branch instead of waiting for the merge.
- A report or a finished goal does not free an implementer: its branch can still fail review, and the fix round then has no owner. Give new work only after the branch passes review.
- A new urgent side task fits a running team: add a contract to the same route folder and give it to an idle implementer (cleared first).

## Idle is a state

- When nothing is in flight, do not re-arm the waiter: it would only TICK. Write "idle, waiter not armed" in the route file so a resumed session knows.
- An agent that finished and was not compacted or cleared is an open action (`context-hygiene.md`).

## Quota is capacity

- Implementers, the integrator, reviewers and a review bot on one model account share one weekly pool. Read the figure before a long round, and count what one review costs on this route.
- At zero, a running goal stops and in-flight work is lost; resume after the reset (`codex.md`, Usage quota). Never fall back to an API key; moving work to another harness is the user's decision.

## Dispatch without copy errors

- A small helper or a wrapper script for repeated commands (clear, then the goal prompt, then a check for the working marker; the waiter settings) removes copy errors from long routes.
