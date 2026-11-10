# 0032: herdr-core runs the agents of new installations

Status: accepted

## Context

Herdr now ships herdr-core, a server that owns terminals, and a native app where a person sees them, beside the older TUI. Changing a built-in default would move every installation that upgrades, which record 0003 forbids, and would break `herdsman up` where core and the app are absent.

## Decision

`[runtime] backend` selects `herdr-core` or `herdr-tui`, and `--backend` overrides it for one run. The installation default is a record, not a constant: the store migration writes `herdr-tui` on a store that already exists and `herdr-core` on a new one, and `config init` writes the backend into every new file. An existing route keeps the backend it was started with. There is no automatic fallback from one backend to the other. If a gate that depends on herdr-core is still open at a release, the backend ships as an experiment behind `experimental = true` in `[runtime.core]`.

An agent on core is identified by its process, its PID and start time, plus a generation, never by a terminal address, a name, a label or a status.

## Consequences

Existing installations keep the TUI until their owner changes one setting. Rollback is a config change on the new binary; an older binary refuses the migrated store. A new harness kind on core needs a herdsman adapter.
