# 0023: Project commands around the lifecycle

Status: accepted

## Context

Projects need their own steps when a task starts, lands or ends.

## Decision

A project may run commands before or after each lifecycle verb, each time-limited and logged per task. Nothing from a project runs until a person has approved that command text.

## Consequences

Editing an approved hook means approving it again.
