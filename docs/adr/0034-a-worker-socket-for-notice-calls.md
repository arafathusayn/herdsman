# 0034: A worker socket for notice calls

Status: accepted

## Context

A sandboxed worker cannot reach the daemon's main socket, and opening the sandbox to the network would also open herdr-core's socket to it.

## Decision

The daemon listens on a second socket that serves only the notice calls of the calling execution, identified by its process. Every managed agent uses it. It has its own request limit, and a held wait takes no request slot. A sandbox may allow this socket by its exact path and nothing wider.

## Consequences

A worker reaches its own notices and nothing else of the daemon. Agent traffic cannot take the slots that route control needs. Codex 0.164.0 is qualified with `network_proxy`, a fresh per-launch permissions profile and an exact Unix-socket allowance. Each profile grants only resolved writable roots and the worker socket. A fresh name is necessary because Codex merges permission tables: reusing a fixed name can retain older write grants. The wrapper verifies the resolved profile and proxy before registration. User arguments cannot change the sandbox, selected profile or proxy. Claude Code remains unqualified for the restricted profile.
