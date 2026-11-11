# 0034: A worker socket for notice calls

Status: accepted

## Context

A sandboxed worker cannot reach the daemon's main socket, and opening the sandbox to the network would also open herdr-core's socket to it.

## Decision

The daemon listens on a second socket that serves only the notice calls of the calling execution, identified by its process. Every managed agent uses it. It has its own request limit, and a held wait takes no request slot. A sandbox may allow this socket by its exact path and nothing wider.

## Consequences

A worker reaches its own notices and nothing else of the daemon. Agent traffic cannot take the slots that route control needs. A harness whose sandbox cannot allow one socket by path is not qualified for the restricted profile on core. In this release no harness is: the restricted profile is refused on core, and every managed agent there is unrestricted and still uses this socket.
