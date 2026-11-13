# 0032: Herdr-core runs the agents of new installations

Status: accepted; default decision updated 2026-11-13

## Context

Herdr now ships herdr-core, a server that owns terminals, and a native app where a person sees them, beside the older TUI. Changing a built-in default would move every installation that upgrades, which record 0003 forbids, and would break `herdsman up` where core and the app are absent.

## Decision

`[runtime] backend` selects `herdr-core` or `herdr-tui`; `--backend` overrides it for a new route. The account store records the installation default once. New installations and new `config init` files select `herdr-core`. Existing stores, explicit choices, and existing routes retain their backend. `config update` does not add a backend. There is no automatic fallback.

An agent on core is identified by its process, its PID and start time, plus a generation, never by a terminal address, a name, a label or a status.

## Consequences

Existing installations keep the TUI until their owner changes one setting. Rollback is a config change on the new binary; an older binary refuses the migrated store. A new harness kind on core needs a herdsman adapter.

The old `[runtime.core] experimental` key is accepted and has no effect. Earlier execution records retain their historical experiment label; new core records do not acquire it. The native app remains mandatory for visibility. The optional TUI attachment does not provide agent identity or readiness.

Harness qualification is separate from the default. Claude Code holds a background notice wait. Codex receives queued notice wakes, but interruptions and paused goals still need a person. Both adapters require the operator to submit their initial fixed start line after startup dialogs.
