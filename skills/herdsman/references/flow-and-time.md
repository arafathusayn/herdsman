# Flow and time

The orchestrator owns time and capacity. Agents do not keep their own deadlines, and the slowest stage of a route moves from coding to review to integration. Plan for the stage that is slow now, not for the one that was slow at launch.

## The orchestrator enforces time

- Every prompt, first task and fix round alike, carries a due time, and the waiter gets it as `HERDSMAN_DUE_<name>`. Reason: agents ignore stop times written in their own briefs.
- Past the due time, queue a stop-and-report steer, then send the interrupt key if no report comes. Reason: a queued steer lands only when the current turn ends, and a long turn may not end.
- Size due times from the durations seen on the route, so that OVERDUE means something. A due time far beyond the real duration never fires.
- A reviewer can have a due time too: add its name to the waiter's `HERDSMAN_IMPLEMENTERS` list with `HERDSMAN_TASK_<name>=review-<pr>-<k>`.
- An agent that is inside its final gates at the due time gets a few more minutes in the waiter, not a stop steer: a stop there throws away a nearly finished run.
- When the user asks whether the route can go faster, give an honest estimate for each remaining step and offer concrete speed-ups with their risks, so the user picks with the cost in view.

## A fix the user names

- Give its arrival time in the first answer. Give it its own small commit and its own early review, or its own ship, as soon as it is coded; it must not wait behind a large fix round.
- When it cannot ship alone, start the review on the current head while the implementer finishes the rest, and say so.
- Run each later review of that branch next to the next fix, never after it. Reason: each round can find new issues elsewhere on the branch, and the named fix then waits behind all of them.
- After the second review round, ask the user once: ship now with the open findings as follow-up tasks, or wait for the next round. A finding about wrong data still blocks.

## The bottleneck moves

- Review rounds, not coding, cost the most. Keep one review per artifact per round, report P0 and P1 only, and gate on a verdict line. A fix round by a fast implementer can take minutes while a full review takes several times longer, so keep re-reviews limited to the diff since the last reviewed head ([`review-discipline.md`](review-discipline.md)).
- A red CI result and the review findings that arrive at the same time go to the owner as one fix round, not two.
- A branch re-review that is still pending when the combined branch is ready can fold into the final review. The final-review prompt then names each earlier review report and the finding lines it must close, and the report answers each one under `CLOSED:`.
- Match the ceremony to the change. On a mid-size feature, per-task paperwork (hashed review packages, per-task review of minor findings) costs more than it catches. The default is review per task for P0 and P1 only, plus one whole-branch review.
- When implementers finish fast, the reviewers become the queue: add a reviewer, or run reviews that do not depend on each other at the same time (for example the final review of a combined branch while a test-only task runs; the test-only task gets its own review).
- When an integrator waits for CI, CI becomes the critical path: run reviews while CI runs, and review a pushed head before its checks end.
- When the only pushing agent is busy, do not stall the reviewer: review the implementer's local wip branch. If the published head equals the reviewed commit, no second review is needed.

## Work ahead, but only on safe ground

- Write the next wave's contracts while the integrator combines, so implementers start the moment its report lands.
- When the task branches own disjoint files, the combine can start while their reviews still run. It finds conflicts early and settles handoffs that a branch already did. Name each head in the prompt as provisional. A fix round on a branch then reaches the combined branch through a re-merge phase (merge the new commits, run again the gates that cover them), and the final review covers the result. A provisional branch's own review still gates the push: it passes, or it folds into the final review with its findings closed there.
- When the integrator's next merge is free of conflicts, branch the next part from the implementer's wip branch instead of waiting for the merge.
- A report or a finished goal does not free an implementer: its branch can still fail review, and the fix round then has no owner. Give new work only after the branch passes review.
- A new urgent side task fits a running team: add a contract to the same route folder and give it to an idle implementer (cleared first). Before you decide that no implementer is free, list every agent: panes of an earlier wave can be idle.
- An agent that waits for a late task gets preparation work (the plan for the live checks, a draft of the pull-request body) instead of idle time.

## Open questions do not hold the launch

- A question that affects only a later or reversible step does not hold the route: record a default in the route file, say it to the user, and gate only the irreversible step (a live call to an outside service, production) on the answer.
- Small design calls that an implementer's report raises are the orchestrator's: rule, record the amendment, and tell the user, who can overrule.

## Idle is a state

- When nothing is in flight, do not re-arm the waiter: it would only TICK. Write "idle, waiter not armed" in the route file so a resumed session knows.
- An agent that finished and was not compacted or cleared is an open action ([`context-hygiene.md`](context-hygiene.md)).

## Quota is capacity

- Implementers, the integrator, reviewers and a review bot on one model account share one weekly pool. Read the figure before a long round, and count what one review costs on this route.
- Before the first dispatch, count the reviews the route still needs (one review per task branch, the final review, and a reserve for re-reviews, since their number is not known yet) against that cost. When they do not fit, offer the user another reviewer then, not when the pool is empty. When the pool runs short during the route, spend what is left on the smallest pending review and let work that needs no reviewer go on (integration and its gates). Never start a review that will hit the limit: pause with every agent idle and the waiter stopped, and ask the user.
- At zero, a running goal stops and in-flight work is lost; resume after the reset ([`codex.md`](codex.md), Usage quota). Never fall back to an API key; moving work to another harness is the user's decision.

## Dispatch without copy errors

- A small helper or a wrapper script for repeated commands (clear, then the goal prompt, then a check for the working marker; the waiter settings) removes copy errors from long routes.
