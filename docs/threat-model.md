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
| Terminal status on herdr-core (OSC 7501) | Any program in a core terminal | Can block a send; never permits one alone and never proves identity | `internal/runtime` |
| The herdr-core API | Any process of the same user | Not a boundary: labels, names and addresses prove nothing; registration and process evidence do | `internal/hc`, `internal/runtime` |
| herdr-core state on disk | herdr-core, in plain text | Holds each terminal's launch environment, screens and history; herdsman puts no secret in a launch environment, label, argument or title | `internal/launch` |
| Other agents on a shared core | The user's other work | Outside a route's inventory by default; a fault under the restricted profile, which needs a core of its own | `internal/runtime` |
| Worker socket requests | Commands of a managed agent, sandboxed or not | Serves only the calling execution's notice calls, identified by its process; own request and wait limits | `internal/daemon`, `internal/notice` |
| Final-report notices | The daemon's stop operation | Fixed text, one per execution, only while the route is stopping; no API call issues one | `internal/notice` |
| Launch environment before registration | An authenticated launch wrapper | Granted secrets reach the prelude and the harness only; authentication gives no standing as an agent | `internal/launch` |
| Terminals a member starts on core | A managed agent | Classified by evidence; an agent that reports nothing and carries no known harness name is not recognised | `internal/runtime` |
| Recovery shells | herdr-core, after a lost program | May be a person's shell by then; never stopped unless a person names it | `internal/runtime`, `internal/ledger` |

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
