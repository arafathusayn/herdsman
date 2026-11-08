# 0028: Tasks stay inside their project

Status: accepted

## Context

Worktrees, agents and reports need something to belong to.

## Decision

Tasks are created within a project and never move to another, and each can span several repositories the project holds. The daemon stores both, with ULIDs and human-readable slugs.

## Consequences

`task list` and the worktree commands refer to the same thing by the same slug.
