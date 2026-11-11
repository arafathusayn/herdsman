# 0032: Each installation chooses where its agents run

Status: accepted

## Context

Herdr now ships herdr-core, a server that owns terminals, and a native app where a person sees them, beside the older TUI. Changing a built-in default would move every installation that upgrades, which record 0003 forbids, and would break `herdsman up` where core and the app are absent.

## Decision

`[runtime] backend` selects `herdr-core` or `herdr-tui`, and `--backend` overrides it for one run. The installation default is a record, not a constant: the store migration writes `herdr-tui` on a store that already exists, and on a new store the default of the release that creates it, which is `herdr-tui` while core is an experiment. `config init` writes that backend into every new file; `config update` never adds one. An existing route keeps the backend it was started with. There is no automatic fallback from one backend to the other. If a gate that depends on herdr-core is still open at a release, the backend ships as an experiment behind `experimental = true` in `[runtime.core]`.

An agent on core is identified by its process, its PID and start time, plus a generation, never by a terminal address, a name, a label or a status.

## Consequences

Existing installations keep the TUI until their owner changes one setting. Rollback is a config change on the new binary; an older binary refuses the migrated store. A new harness kind on core needs a herdsman adapter.

`v0.11.0` ships herdr-core as that experiment, because gates that depend on upstream are open at the release: no harness passes a long run with no person in every coordinator role, and the Linux and `keep` probes are owed. So the migration writes `herdr-tui` on a new store too, with source `new-store`, and `config init` writes `herdr-tui`. Core is selected only with `backend = "herdr-core"` (or `--backend herdr-core`) and `experimental = true` in `[runtime.core]`; without the marker, a new route on core is refused with `backend_experimental` and nothing starts. A route already started on core keeps running and resuming on core, since a persisted route wins. Every run on core says once on stderr that it is the experiment `runtime.core/0.1.0`, `runtime doctor` reports it, and each execution record the backend writes carries it. The backend graduates in a minor release by dropping the marker; the marker is then accepted and does nothing. Which default a new store records then is that release's decision, and an installation that already has a store keeps its row.
