# 0020: Landing requires the reviewed head

Status: accepted

## Context

A review only means something for the commit it looked at.

## Decision

Before landing, herdsman fetches, runs the checks, and stops if a route's most recent review covered a different commit. Policy picks squash, rebase or merge. The target is a local fast-forward, a push, or GitHub's merge API held to exactly that commit, and `down` follows once the result is confirmed.

## Consequences

Unreviewed code cannot slip in behind an approval.
