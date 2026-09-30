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
- A checkpointer pane writes memory and compacts the orchestrator on a schedule (`checkpointer.md`). Keep the route file current so that a compaction loses nothing.

Harness commands and markers: `codex-workers.md` (Context hygiene), `claude-reviewer.md`, `muse-workers.md`.
