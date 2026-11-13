# 0035: Optional core views in the Herdr TUI

Status: accepted

## Context

Core owns the execution and the native app provides required visibility. Users
may also want a TUI attachment, whose lifecycle must remain independent of the
agent. A persistent TUI status override can outlive its reporter, so a bridge
must not leave one behind after a crash.

## Decision

`[runtime.tui_view] enabled = true` with an explicit absolute `socket` enables a convenience attachment. The core backend and mandatory native app continue independently of the TUI.

Herdsman persists a unique workspace creation intent before creating a dedicated pane. It binds the socket incarnation, immutable TUI terminal ID, helper process evidence, core identity, immutable core address and registered execution process. Public pane IDs are refreshed after moves. A lost create response is reconciled by the unique intent; an ambiguous match never authorizes another launch.

A controlled helper attaches through explicitly scoped `HC_SOCKET`, after proving the recorded core process. It retries transport failures at most three times. User detach and mirror close are sticky; `runtime show` may create a fresh attachment. Neither action kills core.

The helper reports only custom display metadata, with its own durable sequence and a three-second TTL. It never calls `agent.prompt`, publishes native harness session identity, or installs a persistent lifecycle override. A fifteen-second daemon lease bounds its authority. Core loss, helper death or daemon loss expires the display state. No persistent lifecycle claim needs a separate cleanup watchdog because none is installed.

## Consequences

The TUI's native agent status and session discovery are not runtime evidence. The helper's display title carries the core program state; task completion remains Herdsman's separate state. Dedicated TUI sessions are recommended when legacy routes enforce a whole-session inventory. Mapping and lease files live under Herdsman's private daemon directory, not in core documents.
