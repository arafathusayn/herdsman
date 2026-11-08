# Threat model

herdsman runs coding agents on a developer's machine, with that developer's privileges. It defends the machine's owner from other local users, from web pages in the owner's browser, and from data that agents and remote services hand it. It does not defend against code running as the owner, which can already do anything herdsman can.

Review this file in the yearly routine (`MAINTAINING.md`), and whenever a new input is added.

## Inputs today

| Input | From | Handling | Where |
| --- | --- | --- | --- |
| Daemon requests | A process on the Unix socket | Socket only; the peer's credentials are checked | `internal/daemon` |
| Web API and page requests | A browser, over TCP on the host `herdsman web` binds | Pairing code and token | `internal/web` |
| Messages between agents | Files in a route's inbox | A pair must be approved before messages flow; content is data, never a command | `internal/message`, `internal/inbox` |
| Route folders | Files agents can write | Parsed as data | `internal/route`, `internal/routedb` |
| Agent and terminal output | Harnesses through Herdr | Displayed or matched as text | `internal/herdr`, `internal/watch` |
| Output of `git`, `gh` and other programs | Subprocesses | Parsed as text | Callers of each program |
| Dependencies at build time | Upstream releases | Pinned copies, offline release build, daily advisories | `third_party/`, `vendor/`, `MAINTAINING.md` |
| CI actions | GitHub Actions | New or changed actions are pinned to a commit | `.github/workflows/` |

## Inputs still to come

Each lands with its own guard, recorded here by the task that adds it: configuration files in four formats, RPC bodies and WebSocket frames, plugins as processes, project hooks, remote `suggest` catalogs, and the MCP server. The rules are in the Longevity and Guardrails records under `docs/adr/`.

## Rules

- herdsman reports nothing home, never asks for more privileges than the user has, and never applies an update on its own.
- A remote catalog can run nothing until someone approves it.
- A binding of the web server beyond localhost is the owner's explicit choice, and the pairing token is then the only barrier.

## Out of scope

- Code running as the same user, including the agents themselves when they are given full access.
- A compromised operating system, toolchain or GitHub account.
- Windows, which herdsman does not support.
