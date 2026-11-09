# Security policy

## Reporting a vulnerability

Report it privately through GitHub: the repository's Security tab, "Report a vulnerability". Please do not open a public issue.

Include the version (`herdsman version`), the platform and the steps to reproduce.

## Supported versions

The latest release. herdsman runs on macOS and Linux.

## What protects you

`docs/threat-model.md` lists the inputs herdsman reads and how each is handled. Third-party code is pinned in this repository; `MAINTAINING.md` describes how an upstream fix lands.

Secrets are read from the system keychain once, when a process starts. `HERDSMAN_SECRETS` lists references only, as `NAME=service/account` separated by commas. Every verb except `doctor` and `daemon` refuses to run if one cannot be read, and removes the values from what it prints and from any pane text it reads. `herdsman web` also removes them from its logs, from every response and header, and from what it writes to a route log. `herdsman doctor` reports which restrictions are in force on the machine.

With `[policy] profile = "restricted"`, `herdsman up` gives each coordinator its own clone and a Codex sandbox limited to that clone and the route's folders, refuses other harnesses and flags that would change the sandbox, and requires `--pair manual`. `herdsman new` and `herdsman go pr:<n>` make a dissociated clone instead of a linked worktree. The launch line `herdsman new --agent` prints holds the agent to that clone, and `--integrator` adds the `[policy] roots`. Other harnesses, an agent command with a prelude or environment, and flags that would change the sandbox are refused before the clone is made. That line carries no secret values, so an agent started from it gets none. The web refuses to start agents under this profile.
