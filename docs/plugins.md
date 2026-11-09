# Plugins

A plugin is a separate executable that adds to herdsman without being compiled into it. Plugins are not in a release yet and are [experimental](experimental.md): the protocol can change between minor releases.

## What a plugin can add

| Extension point | What it does |
| --- | --- |
| Config codec | Reads and writes another configuration language |
| Resource detector | Finds resources a task created |
| Landing backend | Lands work on a forge other than the built-in ones |
| Message generator | Writes commit messages |
| List column | Adds a column to worktree listings |

A plugin declares its extension points in a manifest. Every plugin serves `ManifestService`, plus the service of each point it declares.

## How one runs

herdsman starts the executable and reads a handshake line that names its socket and the checksum of its manifest. It then calls the plugin over that socket. A plugin that fails the handshake is skipped by name and the command carries on.

A plugin runs only after a person approves its exact checksum at a terminal, so a changed binary needs a new approval.

## Commands

Every use needs `--experimental-plugins`:

| Command | What it does |
| --- | --- |
| `herdsman plugin --experimental-plugins list` | Lists the plugins found, usable or not |
| `... describe <name>` | Prints a plugin's manifest |
| `... install <path\|name>` | Copies an executable into the plugin folder and asks for approval |
| `... remove <name>` | Deletes an installed plugin and its approval |
| `... verify <name>` | Runs the conformance checks for its extension points |
| `... run <name>` | Starts it in the foreground, for development |

## Writing one

The contract is the `herdsman.plugin.v1` package, printed by `herdsman spec proto`. In Go, `sdk/plugin` provides `Serve` and the checks behind `verify`. `examples/plugins/` holds one working plugin per extension point; start from the one closest to what you need, and run `plugin verify` before you share it.
