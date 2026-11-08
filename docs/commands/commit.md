# herdsman commit

Commit a worktree with a Conventional Commits message.

```text
usage: herdsman commit [--squash <target>] [--dry-run] [-m <message>] [--type <type>] [--scope <scope>] [--json]

Stages the current worktree by its [commit] stage policy and commits. The
message is written by the configured generator, from the staged diff, and must
be a Conventional Commits message. When no generator can answer, the command
writes a plain one from the branch and the file names and says so; an answer
that is still invalid after one retry stops the commit.

--squash folds every commit since the branch left <target> into this one.
--dry-run shows the request, which program gets it and its answer; nothing changes.

exit codes:
  0  committed, or the dry run printed
  1  nothing to commit, an invalid message, or git refused
  2  bad usage
```
