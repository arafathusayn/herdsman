# 0015: Preparing a new worktree

Status: accepted

## Context

A fresh checkout is not yet somewhere an agent can work.

## Decision

An agent starts only once its worktree is usable: ports reserved, approved `.env` files in place, dependencies installed, and ignored caches cloned with copy-on-write where the filesystem supports it.

## Consequences

Setup stays quick on large caches. Hard links are not used, because one worktree could then change another's files.
