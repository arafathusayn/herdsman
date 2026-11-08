# 0006: Designed to be maintained for decades

Status: accepted

## Context

A tool people depend on should outlive its authors' attention.

## Decision

Whatever users keep is stored in a format other tools can read. Upkeep is written down as a once-a-year checklist, releases can be rebuilt with no network, and rules that matter are tested rather than remembered.

## Consequences

Adding a rule means adding the test or CI job that enforces it.
