# 0013: Default location of worktrees

Status: accepted

## Context

A worktree nested in the checkout gets picked up by tools that walk the tree.

## Decision

Each repository gets a companion `<repo>.worktrees` folder next to it, with one subfolder per task. The location can be configured.

## Consequences

Agents can be confined to the companion folder while a config file beside it stays out of their reach.
