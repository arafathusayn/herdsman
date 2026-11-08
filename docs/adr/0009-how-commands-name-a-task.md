# 0009: How commands name a task

Status: accepted

## Context

Every resource of a task needs a name a person can type.

## Decision

Commands take a task id and derive everything else from it. Whatever else a person has to hand, such as a path, a pull request number or a URL, resolves to the same task.

## Consequences

One resolver serves every command, so selectors behave alike.
