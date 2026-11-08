# 0022: Naming branches by kind of change

Status: accepted

## Context

A branch name should say what sort of work it holds.

## Decision

`new` builds names as `<type>/<task>` in lowercase kebab-case, at most 60 characters, where `type` is one of those Conventional Commits defines, and rejects anything else.

## Consequences

A glance at the branch list shows what each change is.
