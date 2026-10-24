# Adjutant: the optional front for the user

The adjutant is an optional agent that talks with the user, relays full briefs and answers to the orchestrator through herdsman messages ([`messages.md`](messages.md)), and runs approved pane administration. It never implements, tests, publishes, commits, pushes or runs project services. Reason: a front that may also do "a little" of the work blurs who did what and skips the review loop. The route without an adjutant (the user talks to the orchestrator) stays available and unchanged.

## What the adjutant does

- Closed capabilities: write message bodies, run the channel scripts, read the approved route files, manifests and panes, keep its own memory, ask the user through the ask tool, and run approved pane administration. The clipboard holds approved text with no secrets only.
- It hands application edits, server or preview setup, worktrees, installs, tests, authentication checks and script repairs to the owning task agents, through the orchestrator.
- It keeps full briefs and decisions. A summary never replaces the dispatch scope. Work is ready only when the required checks and reviews passed; a preview names the gates still open.
- It asks the user the ordinary questions. The orchestrator sends a `question` message and waits for the answer ([`harness-guards.md`](harness-guards.md), Orchestrator posture). Permission and password prompts stay local to their pane and are never answered by automation.

## Before the role is enabled

- Prove positive channel access and negative project-write, publish and test access in the adjutant's own harness profile. Tool allow and deny patterns are defense in depth, not a sandbox.
- Audit its current session transcript, named in the private setup, by tool-call records, not prose. Flag project Write, Edit or apply_patch calls, shell `git commit` or `git push`, `gh` or API writes, tracker or chat writes, project test or package runners, and service or container commands outside an exact administrative grant. Report the tool name, the call ID and a redacted target to the human and stop. A missing or unsupported transcript adapter is an audit gap, never a clean audit.
- Enable each coordinator and harness pair only after its role limits, channel and socket access, background wake-up, question ownership, transcript identity and audit, and checkpoint adapter are proven. Codex support as a task agent does not prove Codex support as a coordinator, and the Claude checkpointer's usage parser is not a Codex adapter.

## Commands

- `/herdsman adjutant [--name <name>] [--orchestrator <pane>] [--inbox <root>] [--launch]`: attach this session as the adjutant (default); `--launch` starts a new adjutant pane and needs the approved setup plan.
- `/herdsman orchestrator [--adjutant <name> --name <own-name> --inbox <root>] [request]`: with all three adjutant options, enroll as the orchestrator before any work; partial options fail.
- `/herdsman message [status|wait|read [claim]|release <claim>|ack <claim> <id> <record>|send <to> <file> <kind> [reply]]`: `scripts/message.sh` maps it to the scripts with the own name from the durable session setup. Default `status`. Unknown options fail before anything runs. Release fails with no matching claim and with unacked work; an empty active claim can be released.

## Setup

- The setup owner supplies the private inbox root, the installed script folder, both names and their current transcript identities, the socket, the watch targets, the launch inventory, the timing values and the start, restart and stop grants. Check that the root is owned, mode 700, outside every lower-trust writable or reachable set and outside version control.
- The runtime takes a strict external JSON proof in `HERDSMAN_PREFLIGHT`: `inbox`, `excluded:true`, `tracked_files:0` and `rules:[{path,checksum}]` for every effective policy file. The boundary fields are `version:1`, `private_home:true`, `inventory_current:true` and `lower_trust:[{name,harness,launch_hash,sandbox,inbox_denied,socket_denied}]`. Missing boundary evidence permits coordination only, with the human fallback ([`messages.md`](messages.md), Private inbox boundary). Trusted setup owns the proof; never publish it. Test fixture proofs are not evidence of OS isolation.
- Required tools: Bash, Herdr, jq with streaming and `IN` support, `nc` with Unix sockets, POSIX tools. The runtime probes the jq features before enrollment. Check real `nc`, FIFO and socket behavior on an isolated Herdr server before rollout.
- The human writes `watch-targets.json` (an array of full `{pane,session}` identities, every route pane included for card coverage) and `resume-targets.json` (a map of registered task names to `{role:"task-agent",identity}`), both strict JSON with no duplicate keys, and rebuilds them on every identity change. The policy checks and the durable card adapter pass before setup is ready.
- Start every process with literal, shell-quoted absolute values from the plan, because a restored pane does not inherit variables:
  `env HERDSMAN_PREFLIGHT='<proof>' HERDSMAN_INBOX='<root>' HERDR_PANE_ID='<owned-pane>' HERDR_SOCKET_PATH='<socket>' /bin/bash '<skill>/scripts/<script>' <arguments>`
  Enroll: `message-register.sh propose '<name>' '<role>'`. Human approval: `message-register.sh approve '<inspected-checksum>'` and retirement `message-register.sh retire '<name>' '<inspected-checksum>'`, both from the administrator's pane. The courier runs `message-courier.sh` and the notice worker `card-watch.sh`, each in its own supervised courier-service pane; each party runs `message-wait.sh '<own-name>'` as its own harness background task, separate from the route waiter. Keep the configuration, proof, identity, claim and background task IDs in private memory and route state.
- Order: both parties propose, the human verifies and approves the pair, the courier and watcher heartbeats turn fresh, then the seeded handshake ([`messages.md`](messages.md), Intake) runs before the first full brief. Attaching the orchestrator uses the approved administrative `/herdsman orchestrator ...` prompt with target revalidation and focus restoration; it is never a work go.

## On wake and after compaction

Run the route-check launch first. Then restore the configuration, identity and boundary inventory; read file health; then claims, `new/`, quarantine and outgoing work; reconcile goals, cancels and grants; check each background task's completion before re-arming it. The foreground owner gets HEALTH within the configured interval, verifies the service failure and restarts only under its recorded grant. `wait-event.sh` does not carry MESSAGE events yet, so the message waiter and the route waiter are two tracked background tasks.

## Orchestrator side

- Take only messages of the current generation and keep their source attribution. Intake is an immediate durable route record, followed by the tracked work and result obligations.
- Apply a negative or pause instruction at once. An authenticated correction or relayed go under a current boundary updates only its exact scope; under the fallback, the human runs the step ([`messages.md`](messages.md)).
- Resolve a conflict between a goal and an instruction before acting, use one delivery path, and inspect effects after an uncertain completion.
- A side-agent claim is checked against the current code, never treated as a ruling or a human go.

## Published text

- Use plain commands and keep results. `scripts/publish-leak-gate.sh <file> [required heading ...]` is a check on top of a full review and a read-back of the posted text, not a replacement. Its default patterns also match key=value text, endpoint paths and tool or model names. A human-reviewed `LEAK_ALLOW_FILE` allows exact full lines for real project examples, never neighboring lines or secret or private data; `LEAK_PATTERN` adds project patterns. Report any actual disclosure to the user.
