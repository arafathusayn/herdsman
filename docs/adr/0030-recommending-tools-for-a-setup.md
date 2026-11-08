# 0030: Recommending tools for a setup

Status: accepted

## Context

People need to discover what fits the harnesses they use.

## Decision

`herdsman suggest` searches bundled, personal, project and pinned remote entries and ranks them against what it detects on the machine. Applying one prints its recipe; executing that needs `--run` and hook-style approval.

## Consequences

A remote catalog can inform but cannot run anything by itself.
