# 0018: Stop and reclaim in one step

Status: accepted

## Context

Most finished tasks want both halves.

## Decision

`down` combines `teardown` and `clean` behind a single prompt. Landing a task and closing a route both finish by invoking it.

## Consequences

Finishing a task takes one command, and the safety checks of both halves still apply.
