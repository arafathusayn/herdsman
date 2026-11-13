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
| Agent and terminal output | Harnesses through Herdr, or through herdr-core's read on that backend | Displayed or matched as text; a side-agent card is relayed to the adjutant as data through the approved pair's channel; nothing is typed or answered | `internal/herdr`, `internal/watch`, `internal/daemon` |
| Another execution's screen text on herdr-core | The runtime's read | Read by the daemon's card watch and shown to a person; never served as an MCP tool, since it can hold task content the pair channel does not carry | `internal/daemon`, `internal/mcp` |
| Harness session files | Claude Code's projects folder and Codex's sessions folder, writable by any process of the user | Read only under those two folders, never through a link out of them, within size bounds; a session counts only when its recorded folder is the execution's, it was written since registration and no other execution holds it; when two qualify, only a notice id of the execution settles it, otherwise nothing is recorded. The id is evidence for transcripts, never identity | `internal/runtime` |
| Output of `git`, `gh` and other programs | Subprocesses | Parsed as text | Callers of each program |
| Dependencies at build time | Upstream releases | Pinned copies, offline release build, daily advisories | `third_party/`, `vendor/`, `MAINTAINING.md` |
| CI actions | GitHub Actions | New or changed actions are pinned to a commit | `.github/workflows/` |
| Terminal status on herdr-core (OSC 7501) | Any program in a core terminal | Can block a send; never permits one alone and never proves identity | `internal/runtime` |
| The herdr-core API | Any process of the same user | Not a boundary: labels, names and addresses prove nothing; registration and process evidence do | `internal/hc`, `internal/runtime` |
| herdr-core state on disk | herdr-core, in plain text | Holds each terminal's launch environment, screens and history; herdsman puts no secret in a launch environment, label, argument or title | `internal/launch` |
| Other agents on a shared core | The user's other work | Outside a route's inventory by default; a fault under the restricted profile, which needs a core of its own | `internal/runtime` |
| Worker socket requests | Commands of a managed agent, sandboxed or not | Serves only the calling execution's notice calls, identified by its process; own request and wait limits | `internal/daemon`, `internal/notice` |
| Restricted Codex configuration | User and project configuration layers | A fresh permission profile prevents inherited grants; resolved diagnostics must prove the selected profile, restricted filesystem and network proxy before registration; only the exact worker socket is allowed | `internal/profile`, `internal/launch` |
| Codex queue wakes | The notice outbox | A fixed line only, with process, endpoint, version and notice-backed conversation provenance checked before writing; uncertain calls are never replayed | `internal/notice`, `internal/runtime/core` |
| Optional TUI mirror | A separate Herdr server | Immutable terminal mapping and foreground evidence; expiring display metadata and daemon lease; no lifecycle override, native conversation identity or authority over core dispatch | `internal/coreview` |
| A person's typed submit | `herdsman runtime submit` over the main socket | Needs the person's confirmation; refused when the caller's process descends from an execution; one outstanding per execution; sent once with `if` and force off, never retried; the text is never stored | `internal/daemon`, `internal/runtime` |
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
