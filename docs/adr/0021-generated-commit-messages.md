# 0021: Generated commit messages

Status: accepted

## Context

Agents produce many commits, and history must stay readable.

## Decision

Diffs go to a generator limited in time and cost, by default an installed harness, and the output has to be a valid Conventional Commit. Output that fails gets one retry and is then refused. Should the model itself fail, a message assembled from the branch and diff stat is used, and `-m` skips generation.

## Consequences

History stays uniform, and a model outage does not stop a commit.
