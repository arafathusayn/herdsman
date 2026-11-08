# 0031: Proving the speed claim

Status: accepted

## Context

A claim of speed has to be checkable.

## Decision

On a quiet machine, `make bench` times the worktree commands against `wt` and requires a win beyond the noise of a same-build control, judged by 95% confidence intervals.

## Consequences

The checked-in results back the claim, and a CI run on shared hardware cannot block a merge.
