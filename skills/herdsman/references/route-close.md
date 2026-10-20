# Route close

Closing an agent, stopping a session or removing a worktree can lose work that is not saved. Check first, close second, and tell the user what you closed.

## Check before you close

- Read each pane, and check each worktree for uncommitted and unpushed work: `git -C <worktree> status --short` for edits, and `git -C <worktree> log --oneline HEAD --not --remotes` (after `git -C <worktree> fetch`) for commits that no remote branch holds. Use literal paths, one command per line ([`harness-guards.md`](harness-guards.md)). Reason: work that is not on the remote cannot be recovered after the close.
- A squash merge puts a new commit on the default branch, so after the branch is deleted, no remote branch holds the task's own commits, and the check above lists them. That work is saved when `gh pr view <n> --json state,headRefOid` shows `MERGED`, its `headRefOid` equals `git -C <worktree> rev-parse HEAD`, and the status shows no edits. A commit after that head is unsaved work.
- Close an agent only when it is idle. When it is not, wait for its turn to end, or ask the user.
- Close only the panes and worktrees that this route created (the task table in `00-route.md` names them), and never your own pane (`$HERDR_PANE_ID`). Other sessions in the workspace own the rest: ask them to close their own. Reason: a pane or worktree of another session can hold work that you cannot see.

## Save, then stop

- Save each agent's session id and folder to a file in the route folder before you stop it, so that it can resume ([`codex.md`](codex.md), [`muse.md`](muse.md) and [`claude-code.md`](claude-code.md) give the resume forms). Reason: after the close, nothing else holds them.
- Stop each agent with its harness's normal exit command (for example `/exit` or `/quit`), never with a forced kill, because a forced kill loses the work in progress.
- Leave helper processes alone: they are not agents, and other work can still use them.
- Stop the waiter and the pull request watcher by their task ids ([`watchers.md`](watchers.md)). Reason: a watcher with nothing to watch only spends usage and wakes the session. The checkpointer watches the orchestrator pane, not the closed agents, so close the `checkpointer` pane only on the user's go ([`flow-and-time.md`](flow-and-time.md), Idle is a state).

## Remove worktrees

- Remove only a clean worktree whose commits are on the remote (or whose head is the head of a merged pull request, as above), and only with the user's go. `git worktree remove <path>` refuses a worktree with edits; never add `--force`. A worktree that holds copied secrets goes the same way at route close ([`git-and-github.md`](git-and-github.md)).

## Tell the user

- For each closed agent, give its branch, its unpushed state (none, or the commits and edits that remain) and its resume command. Say that later work for a closed agent needs a resumed or a fresh agent.
- Write the same table in the route file, so that a resumed session knows what is closed.

## Disk space

- Read the free disk space (`df -h <worktree root>`) at each checkpoint, and tell the user before it gets low. Reason: each worktree with its dependencies, the test folders that agents leave, and duplicate install payloads take gigabytes, and a full disk stops builds and the machine.
- With the user's go, remove a task's worktree as soon as its pull request merges (clean, commits on the remote), not at route close. This extends the rule to keep the local worktree until route close ([`SKILL.md`](../SKILL.md), step 5 item 6). Tell agents to delete their test temp folders when a task ends.
