# 0011: Layout and versioning of config files

Status: accepted

## Context

Keys without sections eventually need new names, and each new name invalidates files people have committed.

## Decision

A config file is made only of sections and names its schema with a SemVer string. A misspelled setting normally earns a warning. Under policy or launch the command halts, because a typo there could switch off protection.

## Consequences

Old files keep loading, and `config update` can move them forward.
