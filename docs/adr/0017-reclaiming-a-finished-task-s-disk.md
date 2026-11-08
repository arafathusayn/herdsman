# 0017: Reclaiming a finished task's disk

Status: accepted

## Context

Removing a worktree can destroy work nobody pushed.

## Decision

A task with live processes is turned away. For the rest, commits that exist nowhere else go to an archive ref and a bundle before the folder, branch, volumes and caches disappear. Uncommitted or unshared work goes only with `--force`, and the archive is made regardless.

## Consequences

Work removed this way can be recovered from its archive.
