# Context hygiene

An agent's context is a resource that the orchestrator manages from outside. A follow-up needs the memory of the earlier turn; a new task needs a clean start. A command sent at the wrong moment is refused or lands in the middle of a turn.

## The rule

- Right after an agent finishes, compact it when its next job is a follow-up of its own work (a fix round on its branch, a re-review of its own review), and clear it when its next job is new. Never do either in the middle of a turn. Near the usage limit, skip the compaction, because each one spends quota ([`flow-and-time.md`](flow-and-time.md), Quota is capacity).
- The job decides, not the agent: when several reviewers share a queue, a reviewer's next job is usually a branch it has not seen, so clear it at once instead of compacting first.
- A reviewer whose last review used most of its context gets a fresh agent (or a clear) for the next round, not a compaction, and the prompt names the earlier findings. Reason: a compaction of a full context is slow, and the next round then starts with little room.
- A cleared agent loses nothing that matters, because every prompt names its contract files again.

## Timing the commands

- Harnesses refuse compact and clear while a turn or a compaction runs. Wait until the compaction has ended ("Context compacted" or the empty prompt line), then send the next command.
- Read enough of the pane (at least 25 lines) before you decide that an agent is idle. The working line can sit above the last few lines, and a short read misses it.
- Never prompt a working agent with its next job. Some harnesses deliver the prompt in the middle of the turn; others queue it to a random point.
- After each slash command, read the pane before the next prompt or before you send the command again. A clear can leave a chooser (Codex asks where the new conversation runs) or an unsent input line (Claude Code). A stall error from Herdr on a slash command does not prove that it failed: the command often ran.
- Keep the argument of `/compact` short. A long one is read as pasted text, and the command does not run.

## The orchestrator's own context

- Keep it small: read report and review files, not pane transcripts, except to unblock an agent.
- Send a question that spans many files to one read-only research agent, with numbered questions and a word limit. Its answer becomes the Facts section of the next contracts. A probe of an outside service gets hard rules in its prompt: read-only requests, secrets only in shell variables, output files only in the scratch folder.
- After a compaction, read the user's commit and pull-request rules again before the next commit: the harness can add its default instructions (an attribution trailer, for example) back into the context. Also check that the waiter still runs: a notice that a background task runs can be old, so read the task's completion notice and its output file, and re-arm the waiter when the task has ended ([`watchers.md`](watchers.md)). Reason: a dead waiter sends no event, so the route stops with no sign.
- Keep the waiter wrapper and every file a resumed session needs in the route folder, not in a session scratch folder: the operating system can purge temporary folders during a long route.
- Reusing an agent whose worktree was removed: its session still points at the deleted folder. Re-create the worktree at the approved path before the new prompt, and write "already created, work only there" in the contract. A clear (Claude Code `/clear`) returned the session to its launch folder; Codex `/new` asks "current or new worktree" first (enter = current).
- A checkpointer pane writes memory and compacts the orchestrator on a schedule ([`checkpointer.md`](checkpointer.md)). Keep the route file current so that a compaction loses nothing.

Harness commands and markers: [`codex.md`](codex.md) (Context hygiene), [`claude-code.md`](claude-code.md), [`muse.md`](muse.md).
