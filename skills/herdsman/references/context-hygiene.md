# Context hygiene

An agent's context is a resource that the orchestrator manages from outside. A follow-up needs the memory of the earlier turn; a new task needs a clean start. A command sent at the wrong moment is refused or lands in the middle of a turn.

## The rule

- Right after an agent finishes, compact it when its next job is a follow-up of its own work (a fix round on its branch, a re-review of its own review), and clear it when its next job is new. Never do either in the middle of a turn.
- The job decides, not the agent: when several reviewers share a queue, a reviewer's next job is usually a branch it has not seen, so clear it at once instead of compacting first.
- A cleared agent loses nothing that matters, because every prompt names its contract files again.

## Timing the commands

- Harnesses refuse compact and clear while a turn or a compaction runs. Wait until the compaction has ended ("Context compacted" or the empty prompt line), then send the next command.
- Read enough of the pane (at least 25 lines) before you decide that an agent is idle. The working line can sit above the last few lines, and a short read misses it.
- Never prompt a working agent with its next job. Some harnesses deliver the prompt in the middle of the turn; others queue it to a random point.
- After a clear, read the pane before the next prompt: a harness can show a chooser (Codex asks where the new conversation runs) or keep an unsent input line (Claude Code).

## The orchestrator's own context

- Keep it small: read report and review files, not pane transcripts, except to unblock an agent.
- Send a question that spans many files to one read-only research agent, with numbered questions and a word limit. Its answer becomes the Facts section of the next contracts. A probe of an outside service gets hard rules in its prompt: read-only requests, secrets only in shell variables, output files only in the scratch folder.
- After a compaction, read the user's commit and pull-request rules again before the next commit: the harness can add its default instructions (an attribution trailer, for example) back into the context.
- Keep the waiter wrapper and every file a resumed session needs in the route folder, not in a session scratch folder: the operating system can purge temporary folders during a long route.
- A checkpointer pane writes memory and compacts the orchestrator on a schedule ([`checkpointer.md`](checkpointer.md)). Keep the route file current so that a compaction loses nothing.

Harness commands and markers: [`codex.md`](codex.md) (Context hygiene), [`claude-code.md`](claude-code.md), [`muse.md`](muse.md).
