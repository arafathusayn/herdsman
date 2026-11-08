# 0003: Public interfaces only ever grow

Status: accepted

## Context

Scripts and integrations break silently when a tool changes under them.

## Decision

A released interface can gain things but cannot lose or change them. Tools learn what is deprecated from data rather than prose, everything published states its version, and upgrading leaves existing defaults alone.

## Consequences

Callers written today keep working, at the price of carrying old names indefinitely.
