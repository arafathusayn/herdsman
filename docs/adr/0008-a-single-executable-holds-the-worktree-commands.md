# 0008: A single executable holds the worktree commands

Status: accepted

## Context

The worktree layer needs a command surface that fits the existing tool.

## Decision

These are subcommands of `herdsman`; no second program ships. `teardown` ends a task's processes, `clean` reclaims its disk, and `down` runs the pair.

## Consequences

Someone can halt a task's processes and come back to its files later.
