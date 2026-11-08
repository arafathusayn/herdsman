# 0016: Stopping only what a task owns

Status: accepted

## Context

Stopping the wrong process is worse than leaving one running.

## Decision

Before anything is killed, herdsman consults a ledger of what the task launched and re-verifies that the resource belongs to that task alone. Stopping deletes nothing.

## Consequences

A process that merely might be the task's stays alive.
