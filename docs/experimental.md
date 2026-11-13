# Experimental surfaces

Most of herdsman follows the compatibility promise in [ADR 0003](adr/0003-public-interfaces-only-ever-grow.md): a released interface can gain things but cannot lose or change them. The surfaces on this page are the exceptions. They are here so they can be tried and reshaped before anyone has to depend on them.

| Surface | How to turn it on | Version label |
| --- | --- | --- |
| Plugin protocol | Not released yet | To be set when it ships |
| `herdsman eval` and its reports | `--experimental-eval` on every use | `eval/<version>`, in the `experimental` field of the JSON report |
| `[automations]` in the configuration, `herdsman daemon automations` | `experimental = true` in the section, and a person approves each action | `automations/0.1.0`, on each line of the action log |

## What experimental means

- **You opt in each time.** Without the flag, the command stops with a usage error that names it.
- **You are told.** One line on stderr per run says the surface is experimental and gives its version.
- **Anything it writes says which version wrote it,** so a later release can tell old output from new.
- **It can change incompatibly between minor releases.** No old name is kept as an alias. The release notes describe each change, and stored data gets a migration or written manual steps.
- **Nothing stable relies on it.** You can ignore this page and lose nothing.

The herdr-core backend has graduated: new installations select it by default. The former `[runtime.core] experimental` marker is accepted without effect. Existing installations and routes retain their recorded backend. See [ADR 0032](adr/0032-herdr-core-runs-the-agents-of-new-installations.md).

## When a surface becomes stable

It stops needing the flag, and the normal promise applies from that release on. The flag is still accepted and does nothing, so scripts that pass it keep working.

## Deprecated is different

An experimental surface can change without notice. A stable one that is replaced is deprecated instead: the old name keeps working, and `herdsman doctor` names it with its replacement and the fix. `herdsman doctor --json` gives the same in each check's `deprecation` field.
