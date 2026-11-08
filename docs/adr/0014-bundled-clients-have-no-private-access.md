# 0014: Bundled clients have no private access

Status: accepted

## Context

Others must be able to build on what the bundled tools use.

## Decision

The CLI and web app reach the daemon only through the published SDK, whose protobuf contracts are written ahead of any code. Third parties get the same clients and a conformance suite.

## Consequences

Whatever the bundled tools can do, an outside client can do too.
