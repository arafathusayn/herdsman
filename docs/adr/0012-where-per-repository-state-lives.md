# 0012: Where per-repository state lives

Status: accepted

## Context

Worktrees of one repository share facts that must not sit in any of them.

## Decision

All worktrees of a repository read one SQLite file kept under the common git directory.

## Consequences

State survives a worktree's removal and never appears in a diff.
