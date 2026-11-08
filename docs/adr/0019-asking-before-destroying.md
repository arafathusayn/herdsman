# 0019: Asking before destroying

Status: accepted

## Context

Someone should see what will be lost before it goes.

## Decision

On a terminal, a destructive command describes its effects and waits for an answer. `-y` answers for it only when no unsaved work is at stake; uncommitted or unshared changes call for `--force`.

## Consequences

Scripts can run the safe cases unattended but cannot discard work by accident.
