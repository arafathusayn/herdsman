# 0027: Pausing, stopping and removing a route

Status: accepted

## Context

A route needs states between running and gone.

## Decision

`pause` interrupts agents and `stop` ends their sessions, and `resume` recovers from either with the files intact. Removing a route takes `delete`, which archives its folder beforehand and needs `--force` if agents are still active.

## Consequences

Anything short of `delete` can be undone.
