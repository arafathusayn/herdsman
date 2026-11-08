# 0026: A terminal form for launching

Status: accepted

## Context

`up` has many inputs, and a form lowers the barrier.

## Decision

With no arguments, `up` collects its inputs in a form, validates them and shows the plan. The terminal toolkit is written here rather than imported, and scripts pass flags instead.

## Consequences

New users are guided, and automation is unaffected.
