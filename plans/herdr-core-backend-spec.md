# Herdsman on herdr-core

**Status:** Implemented in PR #121; release qualification remains open. Updated 2026-11-13 with herdr-core as the new-installation default, mandatory main-app visibility and an optional TUI attachment. [The implementation and review record](herdr-core-backend-review.md) reports the code coverage, P0/P1 review, fixes and actual validation results. This document originated from source inspection and isolated probes on 2026-11-10 and real Claude Code/Codex probes on 2026-11-11 (N19–N32 in §2). §3 preserves the design-review findings and their resolutions. Implementation does not itself certify every release gate in §17.

**Date:** 2026-11-10

**Decision:** Make **herdr-core the execution backend that new installations and new config files select**, and leave each existing installation on the backend it has until its owner changes one setting (§5). Make the **Herdr desktop app the primary, required visibility integration**: its binding to the core is verified before any dispatch, and later launches confirm in it without waiting for the window (§7). Keep **Herdr TUI optional**, both as a legacy execution backend and, separately, as an optional view of core terminals.

This supersedes the earlier opt-in-core proposal and the assumption that a core-to-TUI bridge is required for main Herdr visibility. In this document, **app** means `herdr-app`; **TUI** means the older `herdr` program and its server; **core** means `hc` / herdr-core. They are distinct products.

## 1. Outcome and boundaries

Herdsman should benefit from core's persistent terminals, native status, input handling, and event API while retaining its own orchestration: routes, roles, tasks, reviews, messages, budgets, policies, worktrees, journals, and recovery decisions.

An agent started by Herdsman must be a real core terminal. It must appear directly in the main Herdr app, with a readable route/task/role location and an interactive terminal. A supported, ready agent must also appear in the app's Agents list. A TUI bridge must not be needed to achieve either result.

The core path must work with no TUI binary, TUI server, TUI socket, or TUI pane IDs. Optional TUI failure must not break core execution or native app visibility.

### Goals

1. Select core for new installations and new config files. Leave every existing installation's backend alone on upgrade (§5).
2. Make every managed agent discoverable and operable in the native app.
3. Preserve approved-pair messaging and all existing policy and review gates.
4. Recover from daemon, app, and core connection restarts without launching duplicate agents or replaying uncertain input.
5. Distinguish an execution, its conversation, its terminal, and its views.
6. Preserve explicit legacy TUI use and existing live routes during migration.
7. Make failures diagnosable through CLI, daemon/API, and web surfaces.
8. Keep secrets out of core's state, which is plain text on disk.
9. Keep the two rules behind channel authority working on a core the user shares with other work: the launcher's attestation (ADR 0025) and the agent inventory.
10. Let a route launch agents while the app window is closed.

### Non-goals for the first release

- Defending against a process that deliberately works around the rules. Core gives its whole API to every process of the same account, and the threat model already excludes code running as the owner. The rules in §6 and §10 stop accidents and misbinding; where a rule can be bypassed on purpose, the text says so.
- Reimplementing core's PTY, rendering, terminal history, or SSH transport.
- Replacing Herdsman's daemon database or route journal with core documents.
- Automatically migrating a live TUI process into core.
- Resuming an agent merely because core restored a terminal address.
- Treating core status or a completed turn as a passed review or completed task.
- Supporting remote execution, Windows, or arbitrary unqualified harnesses in the first release. Existing Unix-platform policy remains; Linux needs its own validation.
- Installing or updating upstream software automatically.
- Requiring a custom app layout or mod for basic visibility.

## 2. Evidence and confidence

### Inspected baseline

| Component | Version or revision |
|---|---|
| Herdsman checkout, original inspection | `4e0a27d424d564451604b89afa99226717e70dd6` |
| Herdsman checkout, third revision | `3e2d44a4f0d582fb8c5bf9845be52db7b946a620` |
| herdr-core | `0.1.19`, protocol generation `1` |
| Herdr app | Installed `0.1.14` |
| Herdr TUI | `0.9.5`, socket protocol `22` |
| Codex CLI, startup probe and harness probes | `0.164.0`; the harness probes used the model `gpt-6.1-sol` at high effort |
| Claude Code, startup probe and harness probes | `2.1.322`; the harness probes used the model `claude-opus-5-5` at high effort |
| App source/documentation inspected | `0ac92e4e9d5dea2097375c26e6a0cb194a75c2f0` |

App source at that commit is newer than the installed binary. Installed behavior is established by probes, not inferred from that commit.

The first Herdsman commit is the one the original inspection read. It was a branch commit and is not in the history of `main`. The third review and the revision after it read the code at the second commit, the head of `main` on 2026-11-10. Three statements about current code rest on the second commit: the config field numbers in §5, the stop and prelude code that §10, §12 and §13 quote, and the daemon's request limit in §10. No probe is moved to it. Each probe ran against the binaries in this table, and N11 to N13 ran a build of the step 0 branch, which starts from the second commit.

Probes used a private core socket/session, private XDG directories, a private named TUI server, and a private `HERDR_APP_HOME`. No normal user server was restarted. A synthetic program emitted valid OSC 7501 status and logged its input. Native harnesses were launched without task prompts, inspected at startup, then terminated. All probe terminals and servers were stopped afterward. The review's probes (N1–N10 below) ran the same way on a separate private core session, which was stopped and removed afterward. They opened no app window. The second review's two checks, and the repeat of them behind N15 to N18, ran the same way and used no real daemon, agent or dialog. The third review ran no probe. It read this document and the code at the second commit, and ran four existing daemon tests of the request limit and the event wait.

The harness probes of 2026-11-11 (N19 to N32) ran real sessions of Claude Code and of Codex, with the models the owner chose, in a private core session of their own. A stand-in served the notice calls of §10 on a Unix socket: wait, read and acknowledge, with the notice states, the open-claim answer and the final-report order of this document. It is not the service. The Claude sessions ran with the owner's hooks, plugins and MCP servers off. The Codex sessions used the owner's Codex home, because the login lives there, with hooks off. Each step that a person would take, such as a trust dialog or a slash command, was taken by the probe's operator and is marked in the timeline. Each observation is one run or a few, with one model at one effort. They decide branches of the design. They are not a qualification suite (§17).

Durable evidence: selected RPC and CLI results, inspected native-app frame, the review's probe script, and the two ancestry probes behind N11 (process rows, the real inspection), and the launcher check probe behind N12 and N13. Behind N15 to N18: the second review's own input race probe and socket probe, and the independent repeats, the input and record probe and the sandbox socket probe. Behind N19 to N32: the harness probe record, with its scripts, its timeline and the sandbox matrix. Behind the two wait rules that answer the fourth review: a model of the rules and its result. The model is a state machine with no sockets and no clock, in which a lost answer, a reordered request and a daemon restart are written out as steps. It runs the review's cases against the text the review read and against the text of this revision. It checks the rules, not an implementation.

### Verified observations

| ID | Observation | Design consequence |
|---|---|---|
| E01 | Adding `herdr.space` and `herdr.tab` to a running core terminal caused app `ls` to show it in a space, tab, and pane. | Use native adoption; no required TUI bridge. |
| E02 | A rendered app frame showed the fixture's terminal content and its Agents-list entry. | Basic native visibility is visually verified for the fixture. |
| E03 | App adoption changed core `keep` from `false` to `true`. | Do not assume managed app terminals retain `keep=false`. |
| E04 | App status followed fixture `working` and `blocked`. Its attention state could remain `done` while raw core status was `idle`. | Scheduling reads core/runtime state, never app attention state. |
| E05 | `terminal.park` returned `pending:true`, subsequently removed placement labels and the pane, disabled keep, and preserved the process. | Command acknowledgement is not completion; parking is distinct from stopping. |
| E06 | Quitting the isolated app preserved the core process and its PID. Reopening recovered its layout. | App lifetime is independent of agent lifetime. |
| E07 | Closing the last pane through the script API requested space-close confirmation. Explicit confirmed space close ended its terminal. | Native close is destructive; never apply TUI-mirror close semantics to the app. |
| E08 | A core server restart preserved the fixture PID and core identity. The TUI's `hc attach` exited to its shell. | Reconnect transport independently of execution; optional mirrors must reattach. |
| E09 | Subscription snapshot sequence was `36`, a label event was `37`, and a new snapshot after restart was `0`. | Sequence numbers belong to a connection/server epoch. |
| E10 | After signal death with keep enabled, the same address, name, labels, and `created_ms` referred to a new shell process with another PID and no status. That probe saw `/bin/sh`; by default the replacement is the login shell, which runs the user's startup files (N2). | None of those stable fields proves execution continuity. |
| E11 | Conditional send with `if:"idle"` rejected that replacement shell as `status_mismatch`, stating that nothing was typed. | Use status guards, but do not mistake them for execution fencing. |
| E12 | Sending to a fixture reporting `blocked` without force was rejected. A send to the idle fixture was delivered and produced a completed turn. | Preserve core's input safety; no forced automated answers to dialogs. |
| E13 | Core rejected a duplicate live terminal name. The API has no launch idempotency key. | Unique names assist reconciliation but do not provide exactly-once launch. |
| E14 | Plain `hc attach` was absent from TUI `agent.list`. Reporting a custom agent made it appear, but did not provide `agent_session` or `interactive_ready`. | Display reporting cannot supply Herdsman's current identity contract. |
| E15 | TUI `agent.prompt` rejected the custom report; using a known kind still failed because that agent was not the pane's foreground process. Raw pane input reached the fixture. | Core-backed messaging must bypass TUI agent-control APIs. |
| E16 | Closing the TUI mirror preserved the core process. TUI restart restored a shell, not a verified live mirror. | Mirrors need explicit reconciliation and independent ownership. |
| E17 | Core rejected a document name containing `/`. TUI metadata rejected `hc` as a lifecycle state label. | The old draft's map name and metadata example were invalid. |
| E18 | Codex startup showed an update prompt; Claude startup showed a workspace-trust prompt. Both had `status:null` and `reported:false` at inspection. The Claude prompt was produced with `--setting-sources ""` in a temporary folder, so it shows that a dialog can come before any status, not how often a real launch meets one. | Live process is not readiness; no end-to-end native-agent certification is claimed. |

The first synthetic-fixture attempt used invalid OSC message encoding and is excluded from status conclusions. The selected fixture records use the corrected emitter. Forced event overflow, native model turns, adversarial timing, and Linux behavior remain release tests.

### Review probes

| ID | Observation | Design consequence |
|---|---|---|
| N1 | A value passed in `terminal.run` `env` is stored in plain text in core's `terminals.json` as `restart_env`. The file has mode 0644 inside a 0700 folder. The value stays there after the terminal finishes. | Nothing secret may travel by `env` (§6, §13). |
| N2 | After SIGKILL of a kept terminal's program, a login shell (`/bin/zsh -l` here) took the same address, `created_ms`, name and labels within 2.3 s. It printed the launch `env` value. | The recovery shell runs the user's startup files and inherits `restart_env` (§12). |
| N3 | A program started through core has the ancestors `hc hold`, core and launchd, and a real TTY. No agent appears, although that core was started from an agent session. `hc exec` gives core, launchd and no TTY. | Process ancestry cannot show that an agent started a core terminal (§6). |
| N4 | A send to that shell with `if:"idle"` was refused, as in E11. The same send without `if`, and with `force` false, was typed and the shell ran it. | `if` is mandatory, not "when supported" (§10). |
| N5 | One `printf` of an OSC 7501 record in the shell made core report `reported:true`, `status:idle` and `app:claude`. A send with `if:"idle"` then passed and ran. | Status is written in-band and can be forged. It is never identity (§9). |
| N6 | A send whose caller closed the connection after 1.5 s, while core was still waiting, was typed later anyway. | A send cannot be recalled (§10, §12). |
| N7 | A core terminal has the environment of whatever started the core. Here that was an agent session: its agent markers, session id, messaging socket and token, effort setting and API base URL. Core adds `HC_TERMINAL_ID`, `HC_TERMINAL_SOCKET`, `HC_SESSION` and `HC_SCOPE`. | Build the harness environment from empty (§13). |
| N8 | After `hc server stop` and `start`, the core id was unchanged. A kept terminal came back as a live login shell with a new PID and the same address, `created_ms` and labels. An unkept one finished with reason `server_stop`. | A stop and start, or a reboot, turns every kept execution into a shell at once (§12). E08's restart, where holders keep the program, is a different event. |
| N9 | A command inside Codex's `:workspace` sandbox could not connect to the core socket (`Operation not permitted`). | Sandboxed launches cannot drive core; keep that as a release gate (§13, §17). |
| N10 | Under `keep:true`, exit with a code and `terminal.kill` are both final. A finished terminal's name can be used again. | A stop through `terminal.kill` leaves no shell. Names are hints only. |
| N11 | Herdsman's own caller inspection, compiled with the step 0 fix, was run inside core terminals. As the terminal's program it saw the holder (`hc hold --socket …`) directly above itself. Under `sh -c` it saw the same, because `sh` replaces itself with a single command. Typed into the default terminal it saw `/bin/zsh -l` between itself and the holder. | The process table alone separates a command from a shell a person types in (§6). |
| N12 | `hc` and `herdr-core` are two names for one program, and a core's processes carry the name it was started with. A core started by `hc` shows `hc --hc-launchd …` and `hc hold …`. The owner's own core, started by the Herdr app, shows `herdr-core serve` and `herdr-core hold …`. The first version of the step 0 fix knew only `hc hold` and so missed the owner's core. The inspection now knows both names and passed in a private core under each. | The holder's name is not a promise. Know both names, read no arguments, and test the name against the running core (§6). |
| N13 | Herdsman's caller inspection was started apart from its parents on a terminal of its own, with two forks and a new pty and no herdr-core. It saw a TTY, no harness and no core. Started plainly by the same script, it saw the agent above it. | The process table stops the ordinary path only. A process that hides passes (§6). |
| N14 | `hc ls` and `hc server status` each start a core when none runs. `hc server status --json` gives each holder's process ID. `hc ls --json` gives no process IDs. | A read-only check asks a core only when one already runs (§6). |
| N15 | A fixture reported `idle`. A send with `if:"idle"` and `force` false pasted its text. On the paste the fixture showed a dialog and reported `blocked`. The send's Enter arrived 201 ms later, in that state, to the same process. The send returned `started:false`. The second review saw the same. | `if` is checked once, before the paste. Typed input is text plus Enter, and the Enter confirms whatever has focus by then. No wording makes a typed line inert (§10). |
| N16 | A send to a program that already reports `blocked` is refused, with and without `if`. `terminal.keys` with `enter` was delivered to it: `keys` takes no `if` and no `force`. `terminal.send` has no option to leave out Enter. | Core guards the start of a send and nothing else. Herdsman does not use `terminal.keys` (§10). |
| N17 | `terminal.get` and `terminal.list` give a terminal's `argv`, `pid`, `parent` and `foreground` whether or not it reports a status. For `/bin/sh -c "sleep 300; echo done"` the `foreground` named the shell, not `sleep`. | A terminal can be classified without a status report, by walking the process tree from `pid` (§6). |
| N18 | A command in Codex's `:workspace` sandbox could not connect to a socket shaped like the daemon's, as it could not to core's (N9). With `codex sandbox --allow-unix-socket` naming that socket or its folder it connected, and a core-shaped socket beside it stayed refused. Tested with the `codex sandbox` command only. | A sandboxed worker can be given one socket and not the other (§13). N27 gives the form that a normal Codex launch takes. |

### Harness probes

Real sessions of both harnesses, run on 2026-11-11 as the method note above describes. "Typed" means input through the terminal. A person's step was taken by the probe's operator.

| ID | Observation | Design consequence |
|---|---|---|
| N19 | **Rest states.** Claude Code reports `idle` before its first turn and after an interrupt, `done` after a turn that ended by itself, `working` in a turn and during a compaction, and `blocked` while an open panel holds back a background-task update. Codex reports `done` after a turn, and `idle` before the first turn, after an interrupt and after a person's slash command such as `/status`. Codex also reports `working` for as long as a command that it left in the background runs, after the turn has ended. | Both rest at `done`, so a typed submit with `if:"idle"` does not exist for either after its first turn (§10). On Codex, `working` does not mean "in a turn" (§9, §12). |
| N20 | **Claude Code holds the wait in the background.** The agent started the wait as a background command and ended its turn, and the program reported `done`. When a notice was issued, Claude Code started a turn by itself, with nothing typed: the turn began within 0.3 s, the read followed after about 8 s, the acknowledgement after 13 s and a new wait after 14 s. The wait was a child of the harness. It held across 30-second polls, reconnected with the same wait id after the stand-in restarted, and was gone within 80 ms of the session's exit or kill. When the wait's process was killed from outside, the agent started a new one within 12 s. | The wake of §10 works for Claude Code as designed, and a waiting agent shows as `done`, not as `working`. |
| N21 | **Claude Code's other two forms.** A wait run as an ordinary command reported `working` until the harness's two-minute command limit. Claude Code then moved the command to the background without ending it, and the turn ended. A turn-end hook that holds the wait itself handed the notice to the agent, but the program reported `working` for as long as the hook waited, and an interrupt ended the hook at once, after which nothing held a wait. | The background command is the adapter's form. The hook is not used to hold a wait (§10). |
| N22 | **Claude Code's start path.** In a folder that Claude Code had been told to trust, the line passed as the initial prompt ran with no dialog, and the wait was attached 6.9 s after the launch. In a new folder the trust dialog came first, with "No, exit" selected and no status report; after a person answered it, the same prompt ran. The session was logged in, so the harness's keychain read works inside a core terminal. | The first start path of §10 works. The trust dialog is a person's step, once for each folder, and Enter there exits (§8). |
| N23 | **Interrupts in Claude Code.** `terminal.interrupt` sends Esc once. At rest it changed nothing that matters: the background wait stayed. In a turn it ended the turn, ended the command that was running, left the claim open and the program at `idle`, and ran no turn-end hook; a wait that the agent had started in the background before its work stayed attached. Two Esc in a row at rest opened the Rewind dialog, whose Enter continues, while the program still reported `done`. With that dialog open, a wake was held back and the program reported `blocked`; a person's Esc released it, and the wake arrived 25 ms later. | An interrupt goes only to an execution that is in a turn, and only once (§12). A dialog at rest can hold a wake, and the status then says `blocked`. |
| N24 | **Wait first, then work.** The reply to a read told the agent to start its next wait in the background before the work, naming the claim it was working on. With that order: a final-report notice issued while a 40-second command ran reached the agent when the command ended, and was read and acknowledged in the same turn. An interrupt in a turn left the wait attached; when the stand-in then ordered recovery, the wait returned the open claim, and the agent read it again, started a new wait, finished the work and acknowledged `completed`, with nothing typed. That worked only after the start line said that an interrupt is a pause and that the wait's output decides how to go on. Without that sentence the model asked the person what to do. | The order of §10 changes: the wait is started before the work. Pause and resume need no person on Claude Code (§12). The start line carries the pause rule. |
| N25 | **Compaction in Claude Code.** A person's `/compact` at rest took 14.5 s at `working` and returned to `done`; the background wait stayed, and the next notice was handled. With the context window set to 100,000 tokens, Claude Code compacted by itself in the middle of a turn, 33,000 tokens below the window. That took 16 s at `working`; the turn went on and acknowledged, and the wait that was started before the compaction stayed attached and woke the agent for a checkpoint notice. | Claude Code compacts by itself, and the wait survives it (§10). |
| N26 | **Codex's start path.** A normal launch with `--no-daemon -a never`, the update check off and hooks off met no dialog in a new folder. The line passed as the initial prompt ran, and the wait was attached 7.7 to 9 s after the launch. | The first start path of §10 works for Codex. E18's update chooser is avoided by a setting, which the adapter requires or reports (§9). |
| N27 | **One socket in a normal Codex launch.** A command that the model ran in the default workspace sandbox could not reach the daemon-shaped socket. A permissions profile that sets that socket to `allow` under `network.unix_sockets` changed nothing by itself. With the feature `network_proxy` on, and a profile that has `network.enabled = true` and the socket set to `allow`, selected as `default_permissions`, the model's command connected to the notice socket and was refused both core's socket and a direct TCP connection. `codex sandbox` gave the same three answers for the same settings. A folder set to `write` under the profile's `filesystem` was writable beside it, where the built-in profile refused it. | The allowance of N18 exists in a normal launch. In Codex `0.164.0` it depends on a feature marked experimental (§13). |
| N28 | **Codex with network access.** With `network.enabled = true` in a profile and the proxy feature off, or with the older `sandbox_workspace_write.network_access = true`, a sandboxed command connected to core's socket, to the notice socket and to a TCP address. An entry that set core's socket to `deny` changed nothing. Tested with `codex sandbox`. | A Codex sandbox with plain network access is no boundary to core. Under the restricted profile, network access goes through the proxy feature or is refused (§13). |
| N29 | **Codex holds no wait across the end of a turn.** Inside a turn, the model ran the wait and kept the turn open; the program reported `working`, and the agent acted 2.4 s after a notice. A second session held one wait that way for 584 s. The model looked at the command again 12 times in that time, about every 50 s, and each look was a model call of about 21,600 input tokens, nearly all cached; it then acted 3.2 s after the notice. When the agent instead left the wait in the background and ended its turn, Codex kept the command and went on reporting `working`. When the command ended, the status became `done` and no turn started: the notice was unread 147 s later. | The return of a background command does not call the model in Codex. A wait held inside one endless turn is not a rest state (§10). |
| N30 | **Codex's own message queue.** `codex queue --thread <session id> --message <text>`, run by another process with no Codex daemon and nothing typed, started a turn in the resting session with that text as its message. In seven timed runs the turn began between 0.03 and 6.7 s after the command returned. A message queued during a turn was delivered when the turn ended, not before. A message queued after an interrupt was held, for 191 s and for 132 s in two runs, until a person typed a line; it followed that line's turn. A queued `/compact` arrived as plain text, and no compaction ran. | Codex can be woken with no terminal input, by one fixed line in its own queue. The content is still pulled (§10). After an interrupt, nothing reaches it without a person. |
| N31 | **Interrupts in Codex.** At rest, one Esc showed "esc again to edit previous message", and a second opened a view of the transcript whose Enter rewinds. In a turn, Esc ended the turn ("Conversation interrupted"), but the command that was running went on as a background command to its end, 45 s later. Until then the program reported `working`, and `terminal.interrupt` returned `still_working`; then it reported `idle`. The claim stayed open. Separately, after `terminal.kill` of a Codex terminal, the wait that Codex had run as a background command was still alive a minute later, with launchd as its parent. Claude Code's background wait was gone within 80 ms of the same kill (N20). | On Codex an interrupt stops the model and not the command, and it leaves a state that only a person's line ends (N30). Pause does not interrupt Codex by default (§12). Ending a Codex terminal does not end what Codex ran in the background, so stop works from the resource ledger (§13). |
| N32 | **Compaction and goals in Codex.** A person's `/compact` at rest took 25 s at `working` and returned to `done`; the next wake was handled. With `model_auto_compact_token_limit = 40000`, Codex compacted by itself in the middle of a turn, in 14 s, went on with the work and acknowledged with the right token. The model's catalog entry gives a window of 500,000 tokens, of which 95% is usable, and no compaction limit of its own; the default level was not measured. A goal set by a person with `/goal` was paused by an interrupt ("Goal paused"). A queued message did not resume it in 132 s. A person's `/goal resume` did, and the queued message followed the goal's turn. | Codex compacts by itself. A paused goal has no way out without a person (§10). |

Still not verified by any round:

- App behaviour beyond the original probes and the upstream guide.
- The gap between paste and Enter at a real harness dialog. N15 measured it on a fixture.
- Each harness beyond one model at one effort, and beyond one run or a few of each case. Blocked permission and authentication states, native resume, and a harness's own sandbox other than Codex's were not exercised.
- Codex with its hooks on. A turn-end hook that holds the wait was not tried in Codex, because turning hooks on also runs the owner's own hooks. Whether a Codex hook runs after an interrupt is therefore unknown.
- Codex's queue while the session shows a dialog or its transcript view, and across Codex versions.
- The level at which Codex compacts when no limit is set (N32).
- The recovery answer that §10 gives a wait when the program has stopped working with a claim open. N24 ordered it by hand.
- The Unix-socket allowance on Linux, in any harness (N27).
- `hc run -- herdsman up` end to end with a real route. §6 rests on the code, N3, N11 and N12, and on a route-level test that showed the approval before the fix and the refusal after it. The same holds for N13: the inspection was run for real, the approval that follows it was not.
- Whether `keep` can be set to false after app adoption and stay false (§12).
- Linux.

### Upstream contracts used

- The app is a core client, and its window and terminal-close lifecycles differ: [app README](https://github.com/herdrdev/herdr-app/blob/0ac92e4e9d5dea2097375c26e6a0cb194a75c2f0/README.md).
- Placement labels and adoption are documented in the installed app guide and [upstream guide](https://github.com/herdrdev/herdr-app/blob/0ac92e4e9d5dea2097375c26e6a0cb194a75c2f0/defaults/AGENTS.md).
- Explicit UI commands and their targets: [app commands](https://github.com/herdrdev/herdr-app/blob/0ac92e4e9d5dea2097375c26e6a0cb194a75c2f0/docs/commands.md).
- Seen/pinned labels are presentation state: [shared labels](https://github.com/herdrdev/herdr-app/blob/0ac92e4e9d5dea2097375c26e6a0cb194a75c2f0/docs/labels.md).
- Wire protocol and event recovery: [core protocol guide](https://github.com/herdrdev/herdr-core/blob/v0.1.19/schema/README.md), [released schema](https://github.com/herdrdev/herdr-core/blob/v0.1.19/schema/hc.openrpc.json).
- Input preparation and writes: [core input implementation](https://github.com/herdrdev/herdr-core/blob/v0.1.19/crates/herdr-core/src/input.rs).
- OSC parsing and turn semantics: [core status implementation](https://github.com/herdrdev/herdr-core/blob/v0.1.19/crates/herdr-core/src/status.rs).
- Optional TUI reporting: [socket API](https://herdr.dev/tui/docs/socket-api/), [agent integration guide](https://herdr.dev/tui/docs/add-herdr-support/).

These establish today's capabilities. Proposed Herdsman commands, data structures, and additional upstream guarantees below are requirements, not claims that they already exist.

## 3. Priority review

No **P0** issue was demonstrated by the probes or by the review. The **P1** issues block a correct release of core as a production backend. They are not cosmetic follow-ups. The first table comes from the original probes. The second was added by the first review, the third by the second review and the fourth by the third review; each of their rows names the section that now resolves it.

| Priority | Issue | Required resolution |
|---|---|---|
| P1 | Current identity and enrollment require a ready TUI agent and native session record. A reported mirror supplies neither. | Introduce execution-based runtime identity and migrate every identity consumer. |
| P1 | Core can replace an agent with a shell under the same terminal address and creation timestamp. | Fence executions and input; refuse replacement shells and unknown incarnations. |
| P1 | Launch and send are non-idempotent across uncertain replies. | Durable intents, single-writer reconciliation, and explicit uncertain outcomes. |
| P1 | Current stop closes TUI panes. Applied to a mirror, it leaves the actual core agent alive. | Stop owned executions through the runtime provider, then clean up views. |
| P1 | Visibility can silently target a different core session from the app. | Verify endpoint identity and actual app membership before first dispatch. |
| P1 | New native app close semantics conflict with the earlier bridge promise. | Respect native stop/park/quit semantics and show them accurately. |
| P1 | Core's current conditional input API is not an expected-execution guard; guarded `done` delivery is also absent from its enum. | Close the input-contract gap in §10 before enabling unattended production dispatch. |
| P1 | Native startup can block before status and readiness. | Qualify each supported harness and expose startup blockers without sending prompts into them. |
| P2 | Persisting an event sequence across restart drops new facts. | Use connection epochs and authoritative snapshots. |
| P2 | App `done` is attention state; core `turn` can advance on blocked/error transitions. | Keep presentation, runtime state, and task outcome separate. |
| P2 | Labels, renamed views, lost events, and concurrent layout edits complicate reconciliation. | Stable owned IDs, readback, bounded retries, and no direct layout-document writes. |
| P2 | The bridge draft had invalid API examples, contradictory selection, and stale-report behavior. | Replace it with the constrained optional adapter in §14. |

Added by the review:

| Priority | Issue | Resolution in this revision |
|---|---|---|
| P1 | Values in `terminal.run` `env` are stored in plain text in core's state and handed to the recovery shell (N1, N2). Today's `paneEnv` passes keychain secrets that way. | Nothing secret travels by `env`. The launch wrapper fetches secrets. A caller is identified by its process, not by a value it carries (§6, §13). |
| P1 | The rule that an agent cannot approve a pair by running `up` (ADR 0025) has no mechanism on core: a program started with `hc run` has a real TTY and no agent among its ancestors (N3). The same gap is in the current release on any machine with `hc`. | Resolve the caller to a core terminal and refuse automatic approval for one an agent started. State the limit in ADR 0025. Fix the current release first (§6, §17). |
| P1 | The agent inventory behind channel authority covers a whole Herdr server today. It has no meaning on a core the user shares with other work. | A core inventory built from registrations and parent chains, with a stated weaker guarantee and the old rule kept under the restricted profile (§6). |
| P1 | Changing a built-in default after 1.0.0 breaks ADR 0003. With no fallback it also breaks `up` on every installation without core and the app. | The default is data. New installations and new config files select core; existing ones keep theirs (§5). |
| P1 | A check in the app before every first dispatch stops unattended launches whenever the window is closed. | Verify the app-to-core binding once. Later launches gate on labels read back from that core and confirm in the app when it is reachable (§7). |
| P1 | A send without `if` is typed into a program that reports no status (N4). A send cannot be recalled (N6). Typed text carries task content. | `if` on every send and none before a first status. Outstanding sends are tracked as pending. Content is pulled and never typed. The typed notice of the first revision is withdrawn by the second review, and a wait replaces it (§10). |
| P2 | Status is in-band: anything that prints an OSC 7501 record sets it (N5). | A one-way veto rule and a per-harness gate (§9); threat-model rows (§16). |
| P2 | A core terminal inherits the environment of whatever started the core (N7). | The wrapper builds the harness environment from empty plus an allowlist (§13). |
| P2 | Kept terminals return as login shells after loss, a core stop and start, or a reboot (N2, N8). Cleanup and tab reuse were undefined. | Recovery-shell rules and one owed probe (§12). |
| P2 | §7 required a ready harness before visibility while §8 established visibility first; the diagram and the steps disagreed on enrolment order. | "Pane present" and "agent listed" are separate; one order throughout (§7, §8). |
| P2 | Seams outside the first list keep a hard TUI dependency, including three `herdr` calls on the `up` path. | Added to §15. |
| P2 | Every managed agent holds the core socket. It can start terminals or send around the pair channel (N7). | Children are counted through their parent chain; the sandbox denial is a release gate (§6, §13). |
| P2 | An uncertain send could block an execution for good, because no later send was allowed and no resend either. | An unresolved send stops typed submits to its execution only, and a person has three ways out. Notices are not typed, so they are never blocked by it. The time limit of the first revision is withdrawn by the second review (§10). |
| P2 | `pane` and `session` are `NOT NULL` in the route store, and any migration makes an older binary refuse the whole database. | New tables; rollback is a config change, not a downgrade (§5). |
| P2 | macOS privacy grants for terminal programs belong to Herdr Core, not to the app or the person's terminal. | `runtime doctor` tests the roots through core (§16). |

Added by the second review:

| Priority | Issue | Resolution in this revision |
|---|---|---|
| P1 | A typed notice ends with Enter, and core checks `if` only before the paste. A dialog that opens in between is confirmed (N15). The first revision called the notice inert and relaxed the release gate on that ground. | No typed line is inert, and the bound is withdrawn. On core `0.1.19` no automation types. An agent is woken through a wait it holds, and the daemon checks every gate when the notice is claimed (§10). |
| P1 | A notice past its time limit was sent again when its id was not on the screen. A send that core still holds has shown nothing yet, so both could be typed. | No time limit retires a send. It stays pending until core answers or the terminal ends; otherwise a person decides (§10). |
| P1 | A restricted worker cannot reach core (N9), and nothing said how it reaches the daemon to fetch a notice. A probe showed that it cannot (N18). | A worker socket that serves only the calling execution's notice calls, allowed in the sandbox by its exact path, with core still refused (§13). |
| P1 | A child terminal with no status was called a command. An agent before its first status, or one that never reports, looks the same. | Children are classified by evidence. One that cannot be classified is never called a command, and is a fault under the restricted profile. The guarantee is restated with its blind spot (§6). |
| P1 | Cleanup killed a recovery shell that had no child process and showed one prompt. A person may have worked there and cleared the screen. | Herdsman never kills a recovery shell unless a person names it. The resource ledger's evidence levels give the rule (§12). |
| P2 | A notice's first read counted as its acknowledgement. A crash between the read and the work lost the instruction, and a repeat could run it twice. | Notices have the states issued, claimed, handled and returned, with the inbox's claim token and processing record. A repeat wakes or reads again; it never repeats the action (§10). |

Added by the third review:

| Priority | Issue | Resolution in this revision |
|---|---|---|
| P1 | Stop held dispatch and then asked for final reports. The last call is a notice, and a hold refused every claim, so no agent could read the request. The current release asks before it closes anything (`applyLastCall`). | A stopping phase. Ordinary notices stay refused, and the hold is never lifted. One final-report notice per execution, which only the stop operation issues, can be claimed ahead of every other notice. Its checks are listed one by one, and an execution that cannot be asked gets that outcome by name (§10, §12). |
| P2 | A turn that was cut short after a read and before the acknowledgement left the claim open on a live execution. `Wait` had no answer for it, and the order rule kept every later notice behind it. | `Wait` returns an open claim before anything else. An acknowledgement carries an outcome, so interrupted or abandoned work is recorded as that and not as done. Pause leaves an idle waiter alone, and resume checks that each execution waits again (§10, §12). |
| P2 | The prelude ran before registration and secrets were fetched after it. A prelude that needs a granted secret works on the TUI today (`paneEnv`) and would fail on core. | The wrapper is authenticated first. That authorises its launch environment and nothing else. The prelude runs with the granted variables, and registration follows it (§6, §8, §13). |
| P2 | Every execution holds a wait, with no budget, no limit on one poll and no rule for a duplicate or a lost waiter. The daemon allows 32 requests in flight. | A wait takes a slot from its own budget and gives its request slot back, as the event wait does. One wait for each execution, a newer wait replaces an older one, and a caller that goes away frees its slot at once (§10, §13). |
| Note | §5 quoted a config field that the first inspected commit does not have. | §2 names both commits and says which statements rest on the second. |

One consequence of the second review's rule is stated plainly in this revision, after a note from the owner's side: on core `0.1.19` the compact command and `/goal resume` need a person, because neither can be pulled. §10 lists every line that Herdsman types today and what becomes of it, and §17 gains a gate for a long run with no person.

Found by the harness probes of 2026-11-11:

| Priority | Finding | Resolution in this revision |
|---|---|---|
| P1 | Codex does not call its model when a background command returns, so the wait of §10 cannot wake it after a turn has ended (N29). | The wake belongs to the adapter. Claude Code holds the wait. Codex gets one fixed line through its own message queue, and still pulls the content (§9, §10). |
| P1 | After an interrupt, nothing reaches Codex until a person types a line, and the command it was running goes on (N30, N31). Pause interrupted every agent in a turn. | Pause leaves a Codex turn to end by itself. An interrupted Codex execution is reported as `needs_attention` (§12). |
| P1 | With plain network access, a sandboxed Codex command reaches core's socket, and a `deny` entry does not stop it (N28). | Under the restricted profile, network access goes through Codex's proxy feature or the launch is refused (§13). |
| P1 | An agent that worked held no wait, so an interrupt left it with nothing to wake it, and a stop request waited for the end of its turn. | The wait is started before the work and names its claim. Pause and resume then need no person on Claude Code, and a stop request arrives at the next tool boundary (N24, §10, §12). |
| P2 | An Esc at rest is the first half of a rewind in both harnesses, and an open dialog in Claude Code holds a wake (N23, N31). | One interrupt for one pause, and none to an execution at rest. A dialog at rest is reported, and no key is sent (§12, §17). |
| P2 | Both harnesses rest at `done`, and Codex reports `working` for a command left in the background (N19). | Herdsman's state for an execution comes from the daemon's own record. A typed submit through Herdsman exists only at `idle` (§9, §10). |
| P2 | The one-socket allowance needs a permissions profile and an experimental feature in Codex, and the restricted profile builds the sandbox from older keys that cannot carry it (N27). | On core the restricted profile builds a permissions profile, and the adapter pins the Codex versions where its suite passed (§13). |
| Settled | Both harnesses compact by themselves in the middle of a turn and go on (N25, N32). A paused Codex goal has no way out with no person (N32). | The compact command is no longer a step for a person. The goal line still is (§10). |

Added by the fourth review. It read the third revision, before the harness probes. Both of its cases still held in the text after the probes, and working through them showed two more of the same kind:

| Priority | Issue | Resolution in this revision |
|---|---|---|
| P2 | An issued notice whose claim is refused woke its agent again at once. Under a hold, with a parked terminal or after a withdrawn approval, each new wait returned the same id, and each return is a model call. | The wake follows the claim. A wait announces a notice only while that notice would pass its claim, and again when a gate opens. Every gate is still checked at the read (§10). |
| P2 | A replaced wait could take the wait back. Its command tried again with its own wait id after it lost the `replaced` answer, and any other id replaced the owner. | A wait is owned by the highest generation the daemon gave out. Only a command's first call can become the owner, each wait id is recorded with its generation, and a lower generation is answered `replaced` every time, also after a daemon restart (§10). |
| P2 | Found with the first: a wake line for Codex was queued only when a notice was issued. After a refused claim nothing queued another when the gate opened, unless a new notice happened to be issued. | The same rule. The daemon looks again when a gate changes and after each notice call (§10). |
| P2 | Found with the first: an agent with an open claim that read under a hold was told to wait, and a plain wait offered it the claim again at the end of each poll. | No recovery answer is given under a hold or while the route is stopping. The reply to that read names the wait that names its claim (§10). |

## 4. Architecture and ownership

```text
Herdsman CLI / daemon API / web
             |
     orchestration and journals
     identity, policy, messages, reviews
             |
      runtime provider contract
          /             \
 herdr-core (default)   legacy Herdr TUI (explicit)
          |
    core terminal + program
          |
          +--- Herdr app: native placement, rendering, human input
          |
          +--- optional TUI view: hc attach + display reports
```

The existing Herdsman daemon owns runtime coordination and reconciliation. Do not add a required second coordination daemon. The optional TUI view worker may be a daemon component; its failure has no authority over routes or core processes.

| Authority | Owns | Does not own |
|---|---|---|
| Herdsman | Desired executions, logical roles, task/review state, approvals, dispatch, budgets, recovery policy, worktree/resource ledger | PTY rendering or app attention |
| Core | Terminal processes, terminal facts, history, status transport, input arbitration | Herdsman task completion, pair approval, sandbox policy |
| Herdr app | Its layout document, panes, focus, seen/pinned state, native human controls | Herdsman execution identity or approved relationships |
| Optional TUI adapter | Its own mirror panes and display reports | Agent launch, task dispatch, approval, or agent lifetime |

Core's parent/child and automatic notification features must not become a second orchestration tree. Herdsman agent launches set `no_parent:true` and `no_notify:true`. Herdsman messages continue through the approved-pair channel and durable outbox. A future integration of native core children requires a separate ownership and budget design.

Those two flags cover only Herdsman's own launches. Every managed agent holds the core socket and its own terminal address (N7), so it can start terminals itself. §6 says how the inventory treats them and §13 how they are counted.

## 5. Defaults, configuration, and compatibility

### Backend selection

Proposed values:

- `herdr-core`: the backend of new installations and new config files once this feature ships.
- `herdr-tui`: the legacy execution backend, and the backend every existing installation keeps until its owner changes it.

A view is configured separately. Enabling a TUI view never changes the execution backend.

Selection precedence for a new route:

1. The `--backend` option.
2. `runtime.backend` in the config file in use. The file is found as today: `-c`, then `HERDSMAN_CONFIG`, then the repository's parent folder, then the repository root. There is no separate user-level file.
3. The installation default.

For an existing route, the persisted backend and endpoint win. A conflicting override fails with `backend_migration_required`.

**The installation default is data, not a constant.** ADR 0003 says that upgrading leaves existing defaults alone. ADR 0004 applies that promise in full from 1.0.0, and its two exceptions are the daemon's early socket routes and the web API. The backend has been part of the v1.0.0 roadmap since 2026-11-10 and ships before the tag (§19). That does not free the constant. The v1 plan releases `herdsman up` in an earlier tag, `v0.9.0`, with the TUI as its only backend, and the compatibility contract binds a default from the release that ships it. Changing a built-in constant would then change the default of every installation that upgrades across the switch. With no fallback, `herdsman up` would stop working wherever core and the app are absent. Instead:

- The account-store migration that ships with this feature writes the installation default once. It writes `herdr-tui` when it runs on a store that already exists, and `herdr-core` when the store is created new.
- `herdsman config init` writes `backend = "herdr-core"` into every new file, so the choice is visible where it applies. `herdsman config update` never adds or changes it.
- An existing installation moves with one command: `herdsman config set runtime.backend herdr-core` for one project, or `herdsman runtime default herdr-core` for the installation. `herdsman doctor` mentions the option and changes nothing.
- A later major version may make core the default everywhere. That is a 2.0 decision with its own notice.

The owner brought the backend into 1.0.0 scope on 2026-11-10. Keeping the installation record is still a choice the owner can overrule. The alternative is to change the constant before the tag. That removes the record. It is free of harm only if no tag that carries `up` is released before the backend lands; the newest tag at both inspected commits is `v0.6.0`, which has no `up`. Otherwise it moves every installation that ran `v0.9.0` or `v0.10.0`, the owner's own included. The record costs one migration and holds in either order. Nothing else in this document depends on which is chosen.

**Default decision, 2026-11-13.** Herdr-core is the default for new installations and new configuration files. Existing stored choices remain intact. The old experimental marker is accepted without effect. Missing or unsupported core/app capabilities fail explicitly; they do not silently switch execution to the TUI. Harness qualification and release qualification are reported independently of backend selection.

Proposed examples. `--backend` and the `runtime` verbs are additions; the released `up` grammar does not change.

```sh
herdsman up example -p "Implement the requested change" -a claude -o codex
herdsman up legacy --backend herdr-tui -p "Continue here" -a claude -o codex
herdsman runtime doctor --route example
herdsman runtime show --route example --role orchestrator
herdsman runtime default
```

On an installation whose default is core, the first command uses core and native app visibility. `runtime show` selects the already-bound terminal in the app; it does not create another execution. `runtime default` with no value prints the installation default and where it came from.

### Proposed schema

Add a `Runtime runtime` field to `Config` in `proto/herdsman/config/v1/config.proto`, with the next unused number. That is 18 at the second commit of §2, where 17 is `Retention`. The first commit ends at 16. Generate all supported formats, documentation, and SDK types from that schema. Use optional fields to preserve omitted-vs-explicit values.

```toml
[runtime]
backend = "herdr-core"

[runtime.core]
binary = "hc"
# Optional explicit socket. Otherwise resolve the selected session through hc.
# socket = "/absolute/path/to/hc.sock"
# session = "default"
connect_timeout = "5s"
ready_timeout = "90s"

[runtime.app]
binary = "herdr-app"
# Optional nondefault app home; ordinary users use their existing main app.
# home = "/absolute/path/to/herdr-app-home"
visibility_timeout = "15s"

[runtime.tui_view]
enabled = false
# socket is required when enabled; never infer the currently focused pane.
# socket = "/absolute/path/to/herdr.sock"
```

The new section is a minor version of the config schema. An unknown key in `[runtime]` stops the command, as it does in the policy and launch sections (ADR 0011): a misspelled backend must not fall through to a default.

There is no `app.enabled=false` escape hatch in this release. Mandatory integration does not mean the app must remain open forever; see §7. On a machine the app does not support, `runtime doctor` says so and names `backend = "herdr-tui"`. Today that means an Intel Mac, which has no prebuilt app, and a Linux machine without a Vulkan driver.

Resolve the core endpoint once, then persist its canonical connection parameters and `hello.core.id`. Explicit socket and session settings must resolve consistently; contradictory settings fail. Never inherit a surrounding agent's `HC_TERMINAL_ID`, remote target, TUI pane, or scope as the new route's authority. Persist the app-home choice and verify that app membership corresponds to the selected core. Merely sharing a machine name is insufficient.

Do not launch a separate production core session per route unless the configured app actually connects to it. The normal case is the user's app and Herdsman sharing one core, with ownership scoped by route and execution IDs.

### Existing routes and public APIs

1. Existing persisted TUI routes remain TUI routes. Read old route records as legacy bindings; write an explicit backend on their next normal journal update.
2. Changing global defaults does not move, replace, or stop those agents.
3. Recreating a stopped route on core is an explicit migration: preserve files and task history, create new execution identities, and re-establish enrollment. Do not translate a pane ID into a terminal ID.
4. Existing pane-specific API fields remain meaningful only for TUI. Add typed runtime references beside them; do not fill old `pane` or native-session fields with invented values. The released SDK fields that carry panes stay as they are: `UpCaller.pane`, `PeerInfo.pane` and `session`, `InventoryAgent.pane`, `herdr_socket`. Golden files gain cases; none is rewritten.
5. The route store declares `parties.pane`, `parties.session`, `launches.pane` and `launches.session` as `NOT NULL` in strict tables. Execution records therefore go into new tables. Legacy rows are left as they are.
6. ADR 0003 is not amended; the installation default above is how this change meets it. Record that mechanism in a new ADR and in the release notes.
7. Rollback is a config change on the new binary: set `backend = "herdr-tui"` for new launches. It is not a downgrade. The store refuses a schema newer than the binary (`ErrSchemaNewer`), so once the migration has run an older binary refuses the whole database, TUI routes included. Say so in the release notes, and back the store up before the migration runs. Already-running core routes remain core routes until stopped.

No automatic fallback from core to TUI is allowed. Missing or incompatible core produces a diagnostic, not a different runtime.

## 6. Runtime contract and identity

### Provider operations

The shared orchestration layer depends on typed operations, not raw TUI RPC names:

| Operation | Required behavior |
|---|---|
| `Capabilities` | Endpoint identity, protocol, supported methods, harness/input qualifications |
| `EnsureExecution` | Reconcile one durable launch intent; return adopted, created, pending, conflict, or unknown |
| `Inspect` | Fresh execution identity, process state, readiness, native status, conversation evidence |
| `Observe` | Snapshot plus changes with epoch/resync semantics |
| `Read` | Bounded terminal text and completeness/truncation information |
| `Deliver` | Issue a notice for the execution to claim. A typed submit only as a person's action, always with `if`, with explicit pending and unresolved outcomes (§10) |
| `Interrupt` | Request interruption and report observed result; no implicit kill escalation |
| `Stop` | Terminate only the proven owned execution, verify completion |
| `EnsureVisible` / `Show` | Establish app membership / focus an existing view |

App and TUI view operations live behind separate view adapters. Most orchestration needs no knowledge of spaces, tabs, or panes.

### Persisted records

Each execution record contains at least:

```text
schema_version
route_id, logical_agent_id, role, task_id?
execution_id                  # random UUID, one actual harness launch
launch_intent_id, generation
backend, endpoint_id          # endpoint_id includes verified core identity
terminal_address              # core address, never a reusable friendly name
initial_created_ms, observed_pid, observed_process_start_identity
argv_digest, cwd, harness_kind, harness_version
policy_digest, launch_guard_registration
native_conversation           # optional typed value with provenance
desired_state, observed_state, readiness, last_verified_at
app_binding                   # core identity, space/tab/pane IDs, visibility state (§7)
tui_binding?                  # independent optional mirror identity
```

The database owns these records and their uniqueness constraints. Labels help locate resources; they do not authorize enrollment, delivery, or destruction.

**Execution identity is not `(address, created_ms)`.** E10 disproved that assumption. A launcher registration must bind the actual harness process to the launch intent and execution ID. The binding is proved by process, not by a value the process carries. It has two moments on one connection, because the prelude runs between them:

1. **Authenticate the wrapper.** The wrapper starts inside the reserved terminal and connects to the daemon's socket, naming its launch intent. The intent id is a reference, not a secret.
2. The daemon reads the peer's PID and start time from the socket, as it already does for other callers (`sys.PeerCredentials`, `sys.Identify`).
3. The daemon asks the verified core for the terminal it reserved, and requires that terminal's program PID to be the peer. The launch is now `authenticated`. That authorises one thing: the launch environment of this intent, which is the allowlist of §13 with the secrets the policy grants. It gives no enrollment, no readiness, no standing as a caller and no notice.
4. **Run the prelude.** The wrapper builds that environment and runs the prelude steps as its own children (§13). A step that fails ends the wrapper with a code, and the launch fails. Nothing was registered.
5. **Register the execution.** The wrapper reports that the prelude has passed. The daemon checks again that the peer is the same process and is still the reserved terminal's program, and records its PID and start time as the execution's process evidence. The wrapper then `exec`s the harness, which keeps the PID.

The first revisions registered once, after the prelude, and fetched secrets after that. A prelude then had no granted secret, which it has today. Authentication is split off so that the environment can be authorised before the prelude without making anything ready.

No bearer value is needed, so there is none to leak. If an implementation still wants a one-time nonce, the daemon hands it over that socket after step 3. Either way this is a defense against accidental misbinding within the existing same-user threat model, not a sandbox boundary.

**Nothing secret goes into core.** Core stores a terminal's `env` in plain text for the life of its record, and the recovery shell inherits it (N1, N2). Secrets, credentials and nonces are therefore forbidden in `terminal.run` `env`, labels, command lines, titles, core documents and app documents. `terminal.run` `env` carries nothing Herdsman would not print in a log. Secrets that the launch policy grants are read today by `paneEnv` from the keychain as the pane starts and passed as the pane's environment, so the prelude that is then typed into the pane sees them too. On core the wrapper fetches them once it is authenticated (step 3), from the keychain or from the daemon over that socket. It sets them in its own process environment, which the prelude steps and then the harness inherit, and nowhere else. A prelude therefore sees the granted variables it sees today, and a secret the policy does not grant reaches neither the prelude nor the harness. Which source each platform uses is settled by test: a prompt that appears in the terminal is acceptable, a silent denial is not.

The launcher may register and then `exec` the harness in the core-owned PTY. It must not leave a shell as a successful agent on error. Harness hooks or a qualified adapter supply native conversation evidence and readiness. Daemon recovery revalidates OS/process evidence and harness state before restoring readiness. If continuity cannot be proved, report `identity_unknown` and withhold input.

Keep native conversation identity separate from execution identity. A resumed conversation has a new execution ID even when its native conversation ID remains the same. `HC_TERMINAL_ID`, core's `identity` field, a role name, and an app pane ID are not conversation IDs.

### Pair enrollment and callers

Core routes enroll immutable `(logical_agent, role, execution_id, endpoint_id)` bindings. Native conversation identity is additional evidence when available. Approval binds the current execution pair and policy generation. Changed executions invalidate stale launch tickets, caller bindings, and delivery targets; a view move does not.

Replace the core route's dependence on `inbox.LiveIdentity(*herdr.Client, pane)` with a backend-neutral verifier. Audit all consumers, not just `up`: registration, daemon intake, courier, cardwatch, alerts, checkpoint guard, route inbox, resume, automations, and web actions.

Introduce a typed caller context for core agents. Existing `HERDR_PANE_ID`-based callers stay on the legacy path, and the wrapper never passes that variable to a core agent. A core caller is identified by its process: the daemon reads the socket peer and walks its ancestors until it meets a registered execution's PID with the recorded start time. The caller is then that execution. Execution IDs, labels and environment values that the caller presents are hints for error messages only. A replacement shell fails this check with no revocation step, because it is another process. A prelude step runs before registration, so nothing it runs is an agent's caller. This is also why Codex must run with `--no-daemon` (§9): work that runs outside the terminal's process tree cannot be attributed.

A process that leaves the tree on purpose, by a double fork or a new session, is not identified as the agent. It is then an unknown caller and gets nothing that needs an agent's or a person's standing.

A sandboxed worker cannot open the daemon's main socket (N18). It reaches the daemon through the worker socket of §13, where the same process rule identifies it and only its own notice calls are served.

A session the user is already in cannot enroll, on core as on the TUI today, because nothing registered it. The supported path is to end it and start it again through the wrapper with the harness's own resume argument. That gives a new execution with the same native conversation (§12).

### Launcher attestation on core

ADR 0025 lets a person approve a pair by running `up` and refuses an agent that tries. A user-invoked `up` remains that explicit authority. `attest` decides it today from three facts: no harness among the caller's ancestors, a TTY, and a TTY that is either a TUI pane without an agent or "a terminal outside herdr". Through core the first and third facts are blind. A program started with `hc run` has a real TTY and only `hc hold`, core and launchd above it (N3), and `hc` is not in `route.HarnessNames`. So `hc run -- herdsman up …` started by an agent meets every check. Core's own guide tells agents to run slow commands that way, so the accident is realistic. The gap is in the current release on any machine with `hc` installed, whatever backend a route uses. Fix it there first (§17, step 0).

The check on core has two parts.

From the process table alone (step 0; written and tested on the local branch `fix/attest-core-terminal`, N11):

1. A herdr-core ancestor marks a core terminal: a process whose program is named `hc` or `herdr-core`, whatever its arguments (N12). The process just below it is the terminal's program: the caller itself, or the ancestor it descends from. A holder sits below the core's server, so a herdr-core process below another is skipped.
2. When that program is not a shell waiting for a person to type, the pair is left pending and `up` prints the command a person can run. That covers `hc run -- herdsman up …`, a command string under `sh -c`, and a script. A shell counts only with nothing but flags after its name. `--pair manual` does not lift this, because a confirmation typed into such a terminal proves no more than the command does. The harness rule already works the same way.
3. A shell passes and is treated as today's "terminal outside herdr".

From the core record (needs the core client, so step 1 or later):

4. Leave the pair pending as well when the terminal has a `parent`, carries any `herdsman.*` label, or reports an agent status at that moment. Each means that an agent or Herdsman itself started the terminal or runs in it, even when its program is a shell.

The name in step 1 is what herdr-core shows today, not a promise it makes. `herdsman doctor` therefore has a `herdr-core` check, built on the same branch. When a core is already running, it asks `hc server status --json` for the holders and compares each one's process-table row with the names step 1 knows. A holder it does not know is a failed check, which also stops `up`. It starts nothing (N14), so with no running core or no live terminal it says that nothing was tested.

This stops an agent that launches `up` through core in the ordinary ways, including the one core's guide teaches. It does not stop an agent that hides, in any of three ways:

- It opens a shell terminal with no parent and types `up` into it with `terminal.send`.
- It starts `up` apart from its own parents on a terminal of its own. This needs no core at all, so the same limit is in ADR 0025 as released (N13).
- It runs a copy of herdr-core under another name.

ADR 0025 states the rule and these limits on that branch. The process table cannot close them. Two stronger checks exist, and §18 lists them as requests:

- Confirm in a channel agents do not hold, such as a dialog in the app.
- An upstream fact for "the last input came from a person". Core's input gate tracks it internally (`Gate.last_human` in `input.rs`); the terminal record does not expose it. With it the rule becomes exact: `up` is a person's when the terminal's last input was a person's and came moments ago.

### Agent inventory on a shared core

Channel authority also rests on an inventory. `BoundaryOK` and `BoundaryV2` list every live agent on the Herdr server and raise an inventory fault unless each one is the approved pair, a courier pane or a sandboxed lower-trust launch. The approval gate, message send, resume, the card watcher and the daemon's pair authority all read it. The user's core holds everything else they run, and §5 makes sharing it the normal case. The same rule there would fault for most users, for good. Dropping it silently would remove a check that five consumers depend on.

On core the inventory covers what the route created, not the whole core:

1. Read every live terminal on the verified core, without a label filter. Classify by registration and parent chain. A label alone never classifies.
2. **Member:** a registered execution of this route, including a supporting service that runs as a core terminal. Members are checked as the pair, courier and sandboxed launches are today.
3. **Child:** a terminal whose `parent` chain ends at a member. Evidence decides its kind. The lack of a status report decides nothing: a supported agent can start without one (E18), and a headless agent or a wrapper may never send one.
   - **Agent child:** it has reported an agent status at any time, or a known harness name appears in its `argv`, in its `foreground`, or in the process tree under its `pid` (N17). It is an unregistered agent the member created, and an inventory fault under every profile.
   - **Declared command:** the member registered it for its own execution with `herdsman resource add`, so the resource ledger holds a record that names the execution. It is not a fault, and it counts against the member's budget.
   - **Unclassified child:** anything else. It is never called a command. Under the default profile it is not a fault: status and `runtime doctor` list it by address, the ledger holds it as `associated`, and every inventory pass reads it again, so a first status report or a harness name turns it into an agent child. Under the restricted profile it is a fault.
4. **Impostor:** a terminal that carries this route's labels and has no registration. Always a fault.
5. **Other route's member:** a registered execution of another route on this installation. Known, and inside its own boundary.
6. **Foreign agent:** any other terminal that reports an agent status. Under the default profile it is not a fault. `runtime doctor` and status show the count.
7. **Plain terminal:** anything else. Ignored.

The guarantee is weaker than today's, and it has a stated blind spot. It is "no recognised agent the route created is outside the route's control", not "no other agent can reach the pair". Recognition rests on a status report or a known harness name. A member that starts a program which acts as an agent, reports nothing and carries no known name is seen as an unclassified child. The default profile accepts that, as it accepts that an agent can run any program inside its own terminal. The restricted profile does not. A foreign agent with full access can type into any terminal on the core, as any process of the same account can. The threat model already excludes that; record the change there as a row (§16).

Under the restricted profile the old rule stays: a foreign agent is a fault. A restricted route therefore needs a core that holds only Herdsman's work, which means a dedicated core session with the app opened on it. `runtime doctor` says which rule is in force and, on a fault, names the terminal that caused it.

A sandboxed launch could not connect to core at all in the probe (N9), so it cannot create children. That is a release gate, with and without network access (§17). The worker socket of §13 does not change it: that is another socket, and the sandbox allows it by its exact path (N18).

## 7. Required native app visibility

### Placement contract

At launch, every managed **agent** receives these labels atomically in `terminal.run`:

```text
herdsman.owner=<installation-id>
herdsman.route=<route-uuid>
herdsman.agent=<logical-agent-uuid>
herdsman.execution=<execution-uuid>
herdsman.role=<role>
herdr.space=<opaque-route-space-id>
herdr.tab=<opaque-agent-or-task-tab-id>
```

Use stable, collision-resistant opaque IDs allocated in the launch intent, not human route names. The verified app adopts unknown placement IDs into new containers. Discover the resulting pane through app `ls`, then name the space and tab through explicit-target app commands. A command response must be followed by readback.

V1 layout: one route space; one tab per coordinator or task agent; optional service/log tabs if shown. Splits and more elaborate layouts are later presentation features. Names should identify route, task, and role without exposing full prompts or secrets.

Do not write the app's `herdr-app` core document directly. The app owns its schema and concurrent layout updates. Read it for diagnostics if useful; use labels and public commands for supported mutations. Do not overwrite `herdr.seen`, `herdr.pinned`, or `where`; those belong to app presentation.

### What counts as visible

Visibility has three facts, checked at different times:

| Fact | Meaning | When it is checked |
|---|---|---|
| **Binding verified** | The configured app is connected to the selected core | Once per app home and core identity, then again whenever the app is reachable |
| **Pane present** | App `ls` shows the exact owned terminal in the intended space and tab, with a pane that explicit `agent.focus` or `pane.focus` can select | Per execution, as soon as the app is reachable |
| **Agent listed** | A qualified, ready harness reports status and is in the app's agent model | Per execution, after readiness (§8 step 7) |

"Pane present" does not wait for readiness. A starting harness that has not emitted status must still have a normal terminal pane, so the user can handle trust, authentication, or update prompts. It is shown as starting/needs-attention in Herdsman and must not be invented as a ready agent.

**Verifying the binding.** The probed app `ls` response does not expose `hello.core.id`, so endpoint verification cannot compare an imagined field. Use freshly allocated random space/tab IDs as a correlation challenge: write them only on the selected core, then require the configured app to show that exact placement and terminal as local (`machine:null`). Verify the registration and labels independently through the selected core. A pre-existing address such as `t2` or a matching display name is insufficient. Persist the result with the core identity, the app home and the app version. If a future app exposes core identity directly, require that check as well. A changed endpoint, a changed app home or a copied layout invalidates it.

The challenge needs the window, because `herdr-app ls` reads the open window. The first launch on an installation therefore needs the app open once. `up` and `runtime doctor` may open the configured app for it; they must not modify the user's app configuration or open a different core silently. Every later launch repeats the challenge with its own route's IDs whenever the app is reachable. That costs one `ls`.

**The dispatch gate.** A new execution may claim its first notice when all of these hold:

1. The binding has been verified for this app home and core identity, and nothing has invalidated it.
2. The placement and ownership labels were written on that core and read back from it.
3. The persisted record holds both.

The gate does not wait for the window. The upstream guide says that a terminal with placement labels appears when the app opens, and the layout lives in core, so the placement is durable without it. A route that runs overnight can then start a new implementer or reviewer after the app is quit, and automations (ADR 0007) do not stall.

Each execution carries one visibility state:

| State | Meaning | Dispatch |
|---|---|---|
| `unverified` | No binding has ever been verified for this app home and core | Held at `waiting_for_visibility`; tell the user which app and core to open |
| `pending` | Binding verified earlier, labels read back, app not reachable now | Allowed |
| `verified` | "Pane present" confirmed in the app | Allowed |
| `missing` | The app is reachable and does not show the placement within `visibility_timeout`, or the user parked the terminal | Held until restored or the route is stopped |

Merely writing labels, receiving `pending:true`, or observing core `state:"live"` reaches none of these states. Status shows `pending` executions plainly, so a person who opens the app knows what to expect there.

### After initial verification

Closing or hiding the app is normal. Executions continue, including their approved messaging, while the app is closed. Their persisted placement remains discoverable when it opens again. Do not kill agents or mark tasks failed simply because the window is absent.

Report `app_closed` separately from `missing` and from runtime failure. When the app becomes reachable, confirm every `pending` execution. One that the app does not show becomes `missing`; that also catches an app that was pointed at another core while it was closed. If layout membership is removed, hold new automatic dispatch to that execution until it is restored or the route is stopped. A closed window alone is not lost membership.

Respect human rearrangements: moving or renaming an owned terminal's view does not change its execution identity. Update observed placement rather than forcing the original arrangement on every reconcile.

If the user parks a managed terminal, mark it `missing` and hold future dispatch. Do not immediately fight the action by re-adopting it. `runtime show` explicitly restores placement. If a terminal is closed/killed, treat the execution as externally stopped; do not restart it automatically merely to restore visibility.

### Lifecycle meanings shown to users

| Action | Effect |
|---|---|
| Close/hide the app window | Core executions continue |
| App park | Process continues; pane removed; Herdsman holds future dispatch until shown again |
| App pane/tab/space close | Ends the associated core terminal(s), subject to app confirmations |
| Close an optional TUI mirror | Detaches that view; core execution continues |
| Herdsman pause | Interrupts agents that are in a turn and holds dispatch; an agent that only waits is left waiting. Retains execution/view where possible. A person's typed submit that core already holds may still be typed (§10) |
| Herdsman stop | Asks each agent for a final report within the grace period, then ends owned executions and retires their active enrollments (§12) |

The bridge draft's “closing a pane never kills the program” promise applies only to optional TUI mirrors. It is false for native app panes.

## 8. Launch transaction

Use the existing route operation journal and daemon database. Keep non-transactional external effects as individually recoverable steps.

```text
planned -> reserved -> spawning -> authenticated -> registered -> ready -> enrolled -> initial_dispatch
                          |             |                |
                          |             |                +-> needs_attention          (startup dialog; nothing is typed)
                          |             |                +-> waiting_for_visibility   (app binding never verified)
                          |             |
                          |             +-> failed                                    (a prelude step failed)
                          |
                          +-> outcome_unknown / conflict / failed
```

Visibility is a separate state on the execution (§7). Only `unverified` and `missing` hold the transaction.

1. **Preflight:** resolve backend and endpoints; negotiate core capabilities; inspect app commands; read the app-binding state; validate executable, harness version, cwd, policy, sandbox arguments, and visibility configuration. Resolve `argv[0]` to an absolute path as the core sees it, because the daemon's `PATH` is not the core's. Check through core (`machine.exec`) that the repository and worktree roots can be read (§16). No task-bearing prompt is embedded in startup argv.
2. **Reserve:** transactionally allocate execution ID, generation, launch intent, stable placement IDs, bounded core name, policy digest, and launch descriptor. Enforce one active launch intent per logical agent/generation.
3. **Spawn:** call `terminal.run` with argv, explicit cwd, labels, `no_parent:true`, `no_notify:true`, `rm:false`, and `keep:true` to match native app adoption. The argv starts the registration wrapper with a descriptor reference. `env` holds nothing secret (§6). Record the returned address immediately.
4. **Reconcile uncertain spawn:** query all matching live **and finished** terminals, scoped to the verified local core. Validate exact ownership labels and launch registration. Adopt exactly one verified match. Multiple or ambiguous matches become a conflict; do not kill or start another automatically.
5. **Authenticate, run the prelude, register:** the wrapper is authenticated by process (§6), builds the launch environment (§13) and fetches the granted secrets. It runs the prelude steps in that environment, with their marker files and retry rules. When they have passed it registers the execution and `exec`s the harness, with the adapter's start line where the adapter uses the first start path (§10). A wrapper that fails, in the prelude or anywhere else, exits with a code, which is final under `keep:true` (N10). It never leaves a shell behind as a successful agent, and a launch that is only authenticated is never ready.
6. **Establish visibility:** read the labels back from the core. When the app is reachable, observe native adoption, persist the pane, and name only owned containers through explicit-target app commands. When it is not, the state is `pending` and the transaction goes on.
7. **Establish readiness:** verify execution identity, harness process, a first status report since registration, and that the execution holds its notice wait (§10). Startup dialogs remain visible and block dispatch. An adapter whose start line needs a person reports `needs_attention` here. When the app is reachable, confirm "agent listed".
8. **Enroll and authorize:** bind the current execution pair under existing approval rules. Complete service registration using runtime references.
9. **Dispatch:** journal the first brief through the normal outbox after all gates pass, and issue it as a notice, which the waiting agent claims (§10). Nothing is typed. No special unjournaled startup prompt.

`up` can end in two waits that need a person: `needs_attention`, for a startup dialog or a start line that a person must submit, and `waiting_for_visibility`. It reports the wait and what to do. A second `up` on the same route picks up where the first stopped, as today. ADR 0024 says that `up` does not stop for input along the way; update it to name these two waits.

A live-name collision is a diagnostic hint, not permission to adopt a process. Use a compact generated name within core's naming limits, and compare execution labels and registration. Names may be reused after a terminal finishes.

If a launch response is lost, a missing live terminal alone does not prove nothing ran. Inspect finished records and pending-spawn states. If core state was wiped, its identity changed, or records were removed before reconciliation, preserve `outcome_unknown`; require an explicit new attempt with a new execution ID. Never replay a possibly completed launch by recycling its old identity.

Only one daemon may perform mutations for a route/backend endpoint at a time. Use the daemon's singleton mechanism plus database uniqueness/CAS on intent state. A second process may inspect; it must not reconcile the same launch concurrently. The default implementation is local-only, not a distributed lease system.

## 9. Harness adapters, status, and readiness

### Qualified adapters

Each supported harness adapter defines:

- Executable and permitted version range; launch and resume argv construction.
- Existing sandbox/profile argument translation and environment policy.
- Process and native-conversation evidence, with provenance.
- Readiness and startup-blocker recognition.
- Expected OSC states and message submission/interrupt behavior.
- The state the harness rests at between turns, `idle` or `done`, established by a real turn (§10).
- Whether tool output that contains an OSC 7501 record can change the reported status.
- How it is woken with no terminal input: by a wait it holds, or by one fixed line in the harness's own message queue. How that wake is there again when a turn ends, and which start path it uses (§10).
- Which status it reports while it only waits, and how an agent that only waits is told from one in a turn (§10, §12).
- What an interrupt stops and what it leaves running, whether the wake survives it, and whether anything reaches the agent afterwards with no person. The same for a compaction (§10, §12).
- What a second interrupt does, and what one does at rest, so that pause sends one at most and only to an execution in a turn (§12).
- Whether the harness compacts its conversation by itself, at what level, and how the checkpointer reads the context size (§10).
- Whether it is qualified for a long run with no person, or for attended use only (§10).
- The control lines it accepts. On core `0.1.19` each one is a person's action (§10).
- What Esc and Ctrl-C do at each of its dialogs, so that an interrupt confirms none (§10).
- The sandbox setting that allows the worker socket and nothing else (§13).
- Required hooks, bounded timeouts, and an end-to-end qualification suite.

### What the probes found for each harness

One model at one effort for each, and one run or a few of each case (§2). The table is what the first adapters are written against. The qualification suite of §17 still decides.

| Fact | Claude Code `2.1.322` | Codex `0.164.0` |
|---|---|---|
| Rest state after a turn | `done` | `done` |
| `idle` means | Before the first turn; after an interrupt | Before the first turn; after an interrupt; after a person's slash command |
| Wake with no terminal input | A wait held as a background command. Its return starts a turn (N20) | One fixed line through `codex queue`. The return of a background command starts nothing (N29, N30) |
| Status while it only waits | `done` | `done`; it holds nothing |
| `working` outside a turn | During a compaction | For as long as a command left in the background runs (N19) |
| Start path | The initial prompt. A new folder shows the trust dialog first, which a person answers (N22) | The initial prompt. No dialog with `-a never` and the update check off (N26) |
| An interrupt in a turn | Ends the turn and its command; a background wait stays; `idle` (N23) | Ends the turn; the command runs on to its end; `working` until then, and then `idle` (N31) |
| After that interrupt | The attached wait returns the open claim, and the agent goes on. No person (N24) | The queue is held until a person types a line (N30) |
| An interrupt at rest | The first changes nothing; a second opens the Rewind dialog (N23) | The first shows a hint; a second opens the transcript view, where Enter rewinds (N31) |
| A stop request reaches a working agent | At the next tool boundary, when the wait was started before the work (N24) | When the turn ends (N30) |
| Compacts by itself | Yes, in the middle of a turn, 33,000 tokens below the window (N25) | Yes, in the middle of a turn, at `model_auto_compact_token_limit`; the default level was not measured (N32) |
| A state that only a person ends | None found in a managed run. A dialog at rest can hold a wake (N23) | The state after any interrupt, and a paused goal (N30 to N32) |
| One socket in its sandbox | Not probed; these runs did not use the harness's own sandbox | The `network_proxy` feature and a permissions profile (N27) |
| Level by these probes (§10) | Unattended | Unattended while nothing interrupts it. A person after an interrupt, and for a goal |

Three things follow for every adapter:

- **Status needs the adapter to be read.** `done` is rest in both. `working` is a turn in Claude Code and may be a forgotten command in Codex. `blocked` in Claude Code can mean a panel that a person left open. Herdsman's own state for an execution comes from the daemon's record of its waits, wakes and claims, with the status as one input.
- **The wake belongs to the adapter.** Claude Code holds a wait. Codex is given a fixed line through its queue. The notice calls, the gates at the claim and the pulled content are the same for both (§10).
- **The Codex adapter records the session id** of the conversation that its execution runs, as part of its native-conversation evidence, and queues to that id only. The probes read the id from Codex's session file for the working folder. How the adapter learns it with provenance is part of its qualification. The queue worked with `--no-daemon` and with no Codex daemon running.

Core's installed guide requires Codex to use `--no-daemon`. The qualified Codex adapter requires it in the user's command and refuses a launch without it, naming the argument; Herdsman does not add it, because the command line is the user's (§19). It also rejects contradictory configuration. Otherwise its work can execute outside the intended terminal/runtime, and outside the process tree that identifies its callers (§6). Preserve model and permission choices from user configuration; do not introduce a model override as part of backend migration.

Native status is preferred. If a harness needs integration hooks, implement a versioned adapter that emits honest status and establishes identity; do not use a persistent optimistic `idle` report to make the Agents list look correct. Unsupported custom commands may run as explicitly unmanaged terminals in future work, but cannot silently satisfy a managed role's readiness contract.

### Normalized observations

Keep these independent fields:

| Field | Examples |
|---|---|
| Process lifecycle | starting, live, lost, replaced, finished, unknown |
| Readiness | waiting, ready, needs_attention, identity_unknown |
| Program status | idle, working, blocked, done, error, unknown |
| Visibility | unverified, pending, verified, missing; plus app open/closed/unreachable |
| Orchestration | active, paused, stopping, stopped, failed |
| Task outcome | pending, review_required, accepted, rejected |

Unknown/future OSC states map to unknown and retain the raw value for diagnostics. They do not become idle. Program `error` is not proof the process exited. Program `done` is not proof the task or review passed. A silent program is not ready merely because it has a live PID.

Readiness requires fresh identity verification, adapter evidence, and at least one status report since registration. A status timestamp measures the last status transition, not a heartbeat; an unchanged idle status is not stale by age alone. Connection health and process verification supply freshness. On disconnect, expose last-known status with its observation time and prohibit new unverified dispatch.

Use core reads for runtime output, with configured byte/line limits and truncation metadata.

**Status is written in-band.** Any program in the terminal that prints an OSC 7501 record sets the terminal's status (N5). That includes tool output that happens to contain one, and an agent that a person starts again inside a recovery shell. Three rules follow:

- Status is never evidence of identity. Identity comes from registration and process evidence (§6).
- Signs of a dialog work in one direction only. A dialog seen in the status or on the screen blocks a send. Neither an `idle` status nor a clean screen permits one alone; both must agree, on a registered execution.
- Screen heuristics may add a blocker. They never remove one that the status reports, and they never confer identity.

## 10. Safe input and delivery outcomes

### What this section governs

This section governs how an instruction from Herdsman reaches an agent in a core terminal. Pair messages are not typed. Agents pull them from the route's inbox, today and on core, and nothing here changes that channel.

Typed text today is: the first prompt (`internal/route/up.go`), the last call and the continue prompt (`internal/route/control.go`), `/goal resume` (`internal/message/resume.go`), the checkpoint prompt and the compact command (`internal/checkpoint/checkpoint.go`), the task launch prompt (`internal/daemon/task_service_wtree.go`), and raw pane input for prelude lines, interrupt keys and web actions. Herdsman writes all of it itself. On core, prelude lines are run by the launch wrapper and are not typed (§8, §13).

### No typed line is inert

`terminal.send` pastes its text and then presses Enter. Core checks `if` once, before the paste. The Enter follows about 200 ms later with no second check (N15, and `input.rs`). A program that was idle at the check can show a dialog by the time the Enter arrives, and the Enter then confirms it. The probe reproduced that in one process, so a guard on the program's incarnation alone would not prevent it. `terminal.keys` has no guard at all (N16).

The safety of typed input therefore cannot come from its wording. The first revision bounded the damage of the unguarded gap at "one inert line in the wrong program" and relaxed the release gate on that ground. That bound is withdrawn. Keeping task content out of typed text still keeps the content away from the wrong reader. It says nothing about what the Enter does.

On core `0.1.19`:

1. No automation presses Enter in a terminal. That covers dispatch, timers, the checkpointer, resume, automations and the web's queued actions.
2. A typed submit is one person's explicit action on one execution: a command or a web action that names the execution and is carried out at once, with `if` and `force:false`. Its confirmation says that the Enter follows the check by about 200 ms. It has the standing of that person typing in the app.
3. The one automated key press is `terminal.interrupt`, for pause and stop. Each adapter states what Esc and Ctrl-C do at each of its dialogs, and its qualification suite shows that neither confirms one (§9).
4. Herdsman does not call `terminal.keys`.

Rule 1 lifts for an adapter when core offers a submit that checks the permitted state and the program's incarnation at every write, the Enter included, and the adapter's suite passes on it (see "Current core gaps").

### Wake by a wait, pull the content

An instruction reaches an agent with no terminal input:

- **A notice** is a record in the daemon: an id, a kind, the target execution with its generation, and the content. The first brief, the continue prompt, the checkpoint prompt and the task launch prompt become ordinary notices. The last call becomes the final-report notice, which has a list of checks of its own (see "The final-report notice").
- **The wait.** A managed agent keeps one blocking command to the daemon open, `herdsman notice wait`. It returns when a notice for that execution can be claimed, and prints the notice id and the next command. The agent started the command, so its return is an event inside the harness. Nothing is submitted to the terminal. The pair channel already wakes a coordinator this way (`herdsman message wait`). The command runs over bounded polls (see "Wait limits"). Claude Code holds it as a background command and starts a turn when it returns (N20). Codex does not call its model when a background command returns, so its adapter is woken another way (see "The queued wake line").
- **The claim.** The agent runs `herdsman notice read`. That claims the notice and prints its content. The daemon serves it only to a caller whose process belongs to the target execution (§6).

The daemon checks every gate when an ordinary notice is claimed, not minutes earlier when it was issued: the pair approval and policy generation, the exact execution and its generation, the route's hold, and the visibility state (§7). A claim that fails a gate gets the reason, and the notice stays issued. A program that is not the agent is never handed a notice, because it never asks for one. A recovery shell holds no wait.

**The wake follows the claim.** A notice that is issued and cannot be claimed now must not wake its agent, because each wake is a model call that ends in a refusal. So the daemon looks at the same gates before it wakes:

- `Wait` announces the next notice of its execution only while that notice would pass its claim. A wake line is queued only then (see "The queued wake line"). Otherwise the wait stays attached. Its polls end empty and renew inside the command, and no model is called.
- When a gate changes, the daemon looks again. Lifting the hold, restored visibility or a renewed approval announces the same notice, once.
- The order of rule 4 is kept. The gates are facts about the execution and its route, so a notice that is held keeps the later ones behind it, and they are announced in the order they were issued.
- While the route is stopping, only the final-report notice is announced.
- This does not replace the check at the claim. `Read` checks every gate again, in the transaction that makes the claim, because a gate can close between the wake and the read. That read is refused with the reason, and its reply says to wait again. The new wait is not answered until a gate changes. One closing of a gate therefore costs one wake at most.
- An execution whose next notice is held for longer than a grace period is shown as `notice_held` with the gate's name. That is on the route's status, where a person reads it. Nothing is printed in the agent's terminal.

The text before this revision announced any issued notice. In the model of §2 that woke the agent at each of five new waits, under a hold, with a parked terminal and after a withdrawn approval.

**Notice states.**

| State | Meaning | What a repeat does |
|---|---|---|
| `issued` | Journaled for one execution generation, not read yet | A wake names the same id. After a refused claim it is announced again only when a gate has changed. Nothing else happens |
| `claimed` | The execution read it and holds a claim token. The work may be under way, or a turn was cut short and left it open | Another read by the same execution returns the same token and content, marked as claimed before, with the path of its processing record. A wait by that execution returns the open claim before anything else. The agent reads its own record before it acts again |
| `handled` | The execution acknowledged it with its token, an outcome and a processing record. The outcome is `completed`, `interrupted` or `abandoned` | A read returns the recorded result and no instruction. A second acknowledgement with the same outcome is accepted and changes nothing. One with another outcome is refused |
| `returned` | The execution ended or was replaced before the notice was handled | It is not served again. Recovery (§12) issues a new notice to the replacement with the old processing record attached, or a person abandons it |

A claim does not expire by time. It ends when the notice is handled or when its execution ends. A repeat therefore wakes the agent or reads again; it never repeats the action by itself. Each notice kind states whether its action is safe to do twice, and the processing record is what an agent or a person checks when it is not. This is the inbox's existing discipline, which already separates reading from acknowledgement (`internal/message/read.go`), applied to a queue that only the daemon writes and only one execution reads. A read is transport evidence. Only `handled` says that the agent accounted for the work, only the outcome `completed` says that it did what was asked, and neither says anything about the task's outcome.

**An open claim on a live execution.** A turn can end with a claim still open and the process alive. Pause interrupts it, a person presses Esc, the harness compacts or clears its conversation, or the agent forgets the acknowledgement. The execution has not ended, so the notice is not `returned`, and the order rule would keep every later notice behind it for good. So:

- `Wait` looks for an open claim first. When the calling execution holds one, a wait that does not name it gets it at once as a recovery answer: the notice id, when it was claimed, the path of its processing record, whether the route was paused and resumed since, and the next command. It offers no newer notice. No recovery answer is given while the work may not go on: under a hold, and while the route is stopping, the wait stays attached, and the claim is offered when the hold is lifted (§12).
- **The wait that names its claim.** The reply to a `Read` tells the agent to start its next wait before the work, as `herdsman notice wait --after <notice id>` (N24). That wait says that its caller is working on the claim, so it is not answered with the claim. It is answered by the final-report notice; by the claim itself, once recovery is due; or, after the claim is handled, by the next notice. An agent that works therefore still holds a wait. A stop request reaches it at its next tool boundary, and an interrupt leaves it something that can wake it.
- **When recovery is due.** When the route is resumed after a pause that interrupted this execution. When a person orders it. And when the program has reported a state other than `working` for longer than a grace period while the claim is open, which means that the turn ended with no acknowledgement. The last trigger reads the status, which a program can forge (§9). All that a forged one can cause is that an agent is offered its own claim again. N24 ordered recovery by hand; the trigger by status is not probed yet (§18).
- `Read` without an id returns the open claim, as a repeat read does. Neither is a new claim, and neither is permission to do the work again. The answer says whether the work may go on: not under a hold, and not while the route is stopping. When it may not, the reply names `herdsman notice wait --after <notice id>` as the next command, so the agent's wait stays quiet until recovery is due. The agent reads its processing record and the state of its work. Then it goes on from where the record stops, or it closes the claim.
- `Ack` closes a claim with an outcome. `completed` is the plain acknowledgement. `interrupted` says that the work stopped part-way, and the record says how far it went. `abandoned` says that the agent will not go on, and why. An agent that cannot tell how far it got uses `interrupted` and says so. Every outcome needs the token and a processing record, and every outcome makes the notice `handled`.
- An outcome other than `completed` goes back to the issuer as an event on the route (§19), so an agent cannot drop work quietly. Each notice kind states what follows: a new notice to the same execution with the old record attached, a report to the coordinator, or nothing, as for a checkpoint prompt that a later one replaces. The old notice is not served again.
- `Ack` is accepted in every route state, under a hold and while stopping. It records what happened and passes no gate.
- An agent that lost its context learns of its claim the same way, from its next `Wait`. When it finds no processing record, the kind's rule for a second run decides: where a second run is safe the agent does the work again, and where it is not the agent acknowledges `interrupted`.
- A daemon restart changes none of this. Claims, tokens and outcomes are journaled, and the first `Wait` after the restart gives the same recovery answer.
- A harness that ignores the recovery answer must not spin. Only the first answer for a claim is immediate. Later ones come at the end of a poll, and after three with no read and no acknowledgement in between, the execution is reported as `claim_stuck`.

**The surface.** `NoticeService` in `proto/herdsman/daemon/v1/`, with `Wait`, `Read` and `Ack`, and the thin verbs `herdsman notice wait`, `read` and `ack` over it. `Wait` is a unary long poll, like the SDK's event wait. It answers with a notice id, an open claim, `replaced`, or nothing yet. A command's first call asks for the wait and is given a wait generation, and each later poll carries it (see "Wait limits"). `Ack` carries the outcome. The reply to each of the three names the next command. A notice document carries its schema version, id, kind, execution, generation, issue time and content digest. A sandboxed worker reaches the service through the worker socket (§13).

**Wait limits.** Every managed execution holds a wait, so the service states its limits. The daemon allows 32 requests in flight and answers `busy` after two seconds when they are taken (`limit` in `internal/daemon/server.go`). The event wait shows the pattern to follow (`EventService.Wait`): it takes a slot from a separate budget, gives its request slot back once it is attached, holds for 30 seconds at most, and ends when its caller goes away. A notice wait does the same, under these rules:

- **Slots.** An attached wait holds a wait slot and no request slot. Notice waits have a budget of their own, apart from the one that event streams and party streams share, so that neither can starve the other. The defaults are 128 waits for the daemon and one for each execution.
- **Admission at launch.** Preflight counts the live managed executions of every route on the daemon. A launch that would pass the budget fails there with a diagnostic. No agent is started that could not wait.
- **Poll length.** The daemon holds one poll for 30 seconds at most. `herdsman notice wait` is one command over many polls. It polls again at once when a poll ends empty, and it carries its wait id and its generation, so the daemon sees a renewal and not a second waiter. On `busy` or a lost connection it tries again with the backoff of §11: it renews when it has a generation, and it repeats its first call when it has none. It ends when it has something to print, when it was replaced, or when the route has stopped.
- **Ownership.** One command owns an execution's wait, and the daemon decides which.
  - The first call of a `herdsman notice wait` command asks for the wait, with a wait id of its own. In one store transaction the daemon gives it the next wait generation of that execution, records the id with that generation, and answers every poll of an older generation with `replaced`.
  - Every later poll of the command renews. It carries the id and the generation. A renewal never makes an owner.
  - The owner is the highest generation the daemon has given out. A request with a lower one, or with a wait id whose record has a lower one, is answered `replaced` every time it arrives. A command that lost that answer gets it again when it tries again, and cannot take the wait back.
  - A command that lost the answer to its first call repeats that call with the same id. The daemon finds the id's record and answers from it, and gives out no new generation.
  - A wait id that the daemon has no record of can ask for the wait only as a first call. As a renewal it is answered `replaced`.
  - The records are journaled with the execution. A daemon restart changes no owner: the connections are gone, each command connects again and renews, and the one with the highest generation still owns the wait.
  - A replaced command exits without polling again. New generations for one execution are at least a second apart; a faster request waits out the rest of that second.

  The text before this revision let any other wait id replace the owner. In the model of §2, two commands that had each lost an answer then took the wait from each other at every retry, and after a daemon restart the owner was whichever connected last.
- **A caller that goes away.** A poll whose caller closed the connection ends at once and frees its slot. The execution holds no wait from that moment. A poll that ended empty keeps its place for a short renewal window, five seconds by default.
- **`not_waiting`.** The count starts when the execution last held a wait: at the lost connection, or at the end of the renewal window. An execution that holds no wait while its program reports `working` is in a turn, and the count starts when that turn ends.
- **Short calls.** `Read`, `Ack` and the start of a `Wait` are ordinary requests on the worker socket, which has its own limit of 32 in flight (§13). One execution may have two in flight. Agent traffic therefore takes no request slot on the main socket, where route control and lifecycle calls are served.
- **Daemon stop.** Every wait ends with an answer that tells the command to connect again, as the event wait does.

The numbers are implementation defaults, as in §11.

**The start line.** A new harness must be told once how it is managed. That is one fixed line per adapter with no task content. For Claude Code it says three things: start `herdsman notice wait` in the background and end the turn; when it returns, do what it prints; and an interrupt is a pause by the operator, not a request to stop, so after one the agent does not ask what to do and follows its wait's output. The third sentence is needed. Without it the model answered a resumed claim by asking the person how to continue (N24). For Codex the line says that each later message names a command to run. Every wake prints the next command again, so the protocol survives a compacted or cleared conversation (N25, N32). The line is versioned with its adapter. It reaches the harness in one of three ways, and the adapter declares which it is qualified for:

1. **As the harness's initial prompt.** The wrapper passes the line as the prompt argument that the harness accepts at launch. The harness takes it after its own startup dialogs, which a person answers in the app, so no terminal input is submitted. This adds one argument to the user's launch command. The compatibility contract says that Herdsman never adds one, so this path needs a stated exception for the core backend: the argument is recorded in the launch spec, printed by `--dry-run`, and refused when the user's command already carries a prompt. The exception is the owner's decision (§18). Both harnesses took the line this way in the probes (N22, N26).
2. **By a person.** `up` reports `needs_attention` with the line. A person submits it in the pane, or triggers it under rule 2.
3. **By a guarded submit**, once rule 1 lifts for that adapter.

An agent of a wait-holding adapter that ends a turn without a wait cannot be woken. The daemon knows which executions hold a wait. One that holds none for longer than a grace period is reported as `not_waiting`, its notices stay issued, and the remedy is the start line again. With the wait started before the work, an agent holds one in a turn too, so the count needs no reading of the status. When the wait's process was killed from outside, Claude Code told the agent, and the agent started a new one (N20). A turn-end hook may check that a wait is held and send the agent back to it when none is. It ran at each turn end in the probes and found the wait there; its refusal was not exercised, and no turn-end hook runs after an interrupt (N23).

A harness can hold the wait in the foreground, as a command it runs and waits for, or in the background, where the harness itself calls the agent again when the command ends. The probes settle it for both harnesses. Claude Code holds it in the background and reports `done` meanwhile; in the foreground its command limit ends the attempt after two minutes, and a hook that holds the wait reports `working` and dies with an interrupt (N21). Codex can keep a wait only inside a turn that never ends, where the model looks at the command about every 50 seconds, at the price of a model call each time, and reports `working` throughout (N29). That is not a rest state, and the Codex adapter does not use it. Herdsman's own status says `waiting`, from the daemon's record, and no rule here reads the program's status to decide whether an agent waits.

**The queued wake line.** Codex holds no wait across the end of a turn, and it has a message queue of its own (N30). For an adapter of that kind the wake is one fixed line, such as "A notice is waiting for you. Run `herdsman notice read` and do what it prints", which the daemon puts in the harness's queue for the conversation of that execution. The harness starts the turn itself, at rest at once and otherwise when the current turn ends. Nothing is typed in a terminal, so rules 1 to 4 are not touched. The rules for it:

- The line is fixed text for each adapter. It carries no task content, no notice id and nothing from another agent. The content is pulled by `Read`, and every gate is checked at the claim, as for a wait.
- The daemon queues a line only for a registered, verified execution, to the conversation id that the adapter recorded for it (§9). It queues one when that execution's next notice can be claimed and no line of its own is outstanding. It looks when a notice is issued, when a gate changes, and after each notice call of that execution. A line is outstanding from the moment it is queued until that execution's next notice call. One line serves every notice issued meanwhile, because each reply names the next command.
- It is queued only while the claim would pass, by the same gates as a wait's announcement (see "The wake follows the claim"). Under a hold, with a parked terminal or after a withdrawn approval the daemon queues none, and it queues one when the gate opens. While the route is stopping it queues one for the final-report notice and none for any other. A read that is refused because a gate closed after the line was queued ends that turn, and no new line follows until a gate changes.
- A queued line cannot be recalled. That is why it is fixed: a stale one makes the agent run a read that returns nothing, or a refusal with its reason.
- A line that is followed by no notice call within a grace period is reported as `wake_unanswered`. That is this adapter's form of `not_waiting`. The count does not run while the execution is in a turn, which the daemon knows from an open claim and not from the status (N19).
- After an interrupt, Codex holds its queue until a person types a line (N30, N31). The execution is then reported as `needs_attention` with the reason `interrupted`, and the daemon queues nothing more for it until it calls again.
- The queue belongs to the harness. Any process of the account that can write the harness's home can queue text for any of its sessions. That is the standing the core API already has (§16). A sandboxed worker cannot write there.

An adapter with neither a qualified wait nor a qualified queue is not qualified for unattended use on core `0.1.19`. Using Codex's queue is a decision the owner can overrule (§18); without it Codex is an attended harness.

### Dispatch contract

Every ordinary notice must pass, when it is issued and again when it is claimed:

1. Current route/pair approval and policy generation.
2. Exact target execution, generation and endpoint identity.
3. A visibility state that allows dispatch (§7), and a route that is neither held nor stopping.
4. Per-execution order: an execution claims its ordinary notices one at a time, in the order they were issued. A claim it left open comes before any later notice (above).

The final-report notice passes its own list, at both moments (see "The final-report notice").

Every typed submit, which rule 2 above limits to a person's action, must pass those four and also:

5. `if` on the call, naming the state the adapter permits. There is no send without `if`.
6. At least one status report from the program since registration.
7. At most one send outstanding per execution.

Rules 5 and 6 come from N4 and E18. Core types a send without `if` into a program that reports no status. Codex's startup chooser reports no status and opens with "1. Update now" selected, so Enter there runs the installer; `skills/herdsman/references/codex.md` already records that lesson. Both harnesses rest at `done` (N19), and `if` cannot express that on core `0.1.19`. A typed submit through Herdsman therefore exists for them only while they report `idle`: before the first turn and after an interrupt. At any other time a person types in the app's pane. A send without `if` is not an acceptable workaround.

Human input through the native app remains human input. Herdsman should respect core's arbitration rather than assembling paste/key sequences through a UI or bypassing its quiet period.

Use `terminal.send` with `force:false`. Do not answer a permission, trust, authentication, or update dialog automatically. `terminal.interrupt` is a separate operation; it does not authorize a general prompt while blocked.

**Control lines.** A slash command, or any other line that only the harness's own interface accepts, cannot be pulled. It is a typed submit, so on core `0.1.19` it is a person's action and never an automatic one. `/goal resume` and the compaction step of a checkpoint are in that class. The text that goes with them, the continue prompt and the checkpoint prompt, is a notice. Where a harness offers the same effect with no input, such as its own automatic compaction or a hook, the adapter uses that. A harness's own queue does not carry a control line: a `/compact` queued for Codex arrived as plain text (N30).

### What stops being automatic

Herdsman types these lines today. This is what becomes of each on core `0.1.19`:

| Typed today | On core `0.1.19` | Needs a person |
|---|---|---|
| First prompt (`up`) | Ordinary notice, after the start line | For the start line only, unless the first start path is allowed |
| Task launch prompt | Ordinary notice | No |
| Continue prompt (resume) | Ordinary notice, or the recovery answer for an open claim | No, when the execution waits (§12) |
| Last call (stop) | Final-report notice | No |
| Checkpoint prompt | Ordinary notice, without its leading slash command. A Codex target gets that form today | No |
| Compact command, which the checkpointer sends after the checkpoint when the context is over its limit | Not sent. Both harnesses compact by themselves (N25, N32) | No. Yes where the user's configuration turns automatic compaction off |
| `/goal resume`, which `herdsman message resume-goal` sends to a Codex target whose goal is paused | Control line | Yes. No way out with no input was found (N32) |
| Prelude lines | Run by the launch wrapper (§13) | No |
| Interrupt keys | `terminal.interrupt` | No |
| Web actions that type | A person's typed submit | It is a person's action already |

Compaction and a paused goal matter most to a coordinator that runs for hours, so they are designed here and not left as a footnote. The probes took the first off the list of steps that need a person and confirmed the second.

**Compaction with no input.** The checkpointer on core sends no compact command. It issues the checkpoint notice on its interval, as today. It also issues one when the context passes a level set below the harness's own automatic compaction, so that the memory is written before the harness compacts by itself, when the agent comes back to its wait in time. A turn that fills the context without a break is compacted by the harness first, as it is today. The adapter states that level and how the context size is read (§9). Claude Code compacts 33,000 tokens below its window (N25). Codex compacts at `model_auto_compact_token_limit`; its level when none is set was not measured, so until it is, the Codex adapter takes the level from the launch's own setting and otherwise issues the checkpoint notice on the interval only (N32). Both compact in the middle of a turn and go on with the work, and in both the claim's acknowledgement survived. After a compaction the agent must be reachable again: Claude Code's background wait stays attached through it, and Codex needs no wait. Where the harness runs a hook after a compaction or at the end of a turn, the adapter uses it, with no input; every wake also prints the protocol again. A harness that does not compact by itself, or where the user's configuration turns that off, gets `needs_attention` with the reason `compaction_due` when the context passes the checkpointer's limit. Herdsman does not change that configuration, and it does not type.

**A paused goal.** On core, `herdsman message resume-goal` types nothing. It reports `needs_attention` with the reason `goal_paused` and the line, and returns its held result. A person submits the line. The probe found no other way: an interrupt paused the goal, a queued message was held, and only the person's line resumed it (N32). A goal is paused by an interrupt. Pause does not interrupt Codex by default (§12), so in a managed run that leaves a person's own Esc, and whatever limits Codex applies to a goal by itself, which were not probed.

**Attended and unattended.** An adapter is qualified for unattended use on core `0.1.19` when all of these hold: it can be woken with no terminal input, by a wait or by its own queue; that wake is there again with no input after a turn and after a compaction; the harness compacts by itself after a checkpoint; and a managed run has no state that only a person can end. Any other adapter is qualified for attended use. `up` and `runtime doctor` then say, before the route starts, which of its steps will need a person. Each such step raises `needs_attention` on the event stream when it comes due, and the route's other agents go on. A route never waits for a person without saying so.

By the probes (§9):

- **Claude Code reaches the unattended level.** It holds the wait in the background, acts on its return and waits again, keeps the wait through a compaction, compacts by itself, and resumes after a pause with no person. What can still need a person: the trust dialog of a new folder, once, and a dialog that opens at rest and holds a wake, which the status reports as `blocked` (N22, N23).
- **Codex reaches it under two conditions.** Its wake is the queued line, which is the owner's decision (§18). And nothing interrupts it: after any interrupt, and for a paused goal, only a person's line gets it going again. Pause therefore leaves a Codex turn to end by itself (§12). A route that needs to cut a Codex turn short, or that runs Codex goals, has attended steps and says so.

These are single probes with one model each. The gate "Long run with no person" (§17) decides unattended qualification for a release. The release notes give the level of each harness. Core remains the default for new installations; qualification and attended steps do not change that decision (§5).

### The final-report notice

Stop asks each agent for its report before it ends it. The current release types a last call and waits a bounded time for the agent to finish (`applyLastCall` in `internal/route/control.go`). On core the last call is a notice, and stop also holds dispatch. A hold that refused every claim would refuse the last call too. The hold is not lifted to let it through. The route has a state of its own for this.

**The stopping phase.** A route that has taken a stop is `stopping` until its executions have ended. In that state:

- Every ordinary notice is refused, at issue and at claim, with the reason `route_stopping`. A notice that was issued earlier stays `issued`, and becomes `returned` when its execution ends.
- A person's typed submit through Herdsman is refused, as under a hold.
- The pair channel is not changed by the phase. It stays as it was, open or held, until the pair is retired, so an agent can still send its report.
- One kind of notice can be issued and claimed: `final_report`.

**Who issues it.** Only the stop operation, as one journaled step for each execution of the route, bound to that operation's id. `NoticeService` has no call that issues a notice, and no other operation issues this kind. Its content is fixed text with no task content: the route is stopping, write your report now, start no new work. When the execution holds an open claim, the text names it, so that the report says where that work stands.

**Its checks**, when it is issued and again when it is claimed:

1. *Identity.* The exact execution, generation and endpoint, as for every notice. A recovery shell, an older generation and another execution get nothing.
2. *Approval.* The stop operation is journaled, and it was accepted from a caller that may stop this route under the rules that govern `stop` today. The execution is a registered member of the route. Pair approval is not required, because the notice carries nothing from one agent to another. The current release sends its last call on the same terms.
3. *Visibility.* The states that allow an ordinary notice: `pending` or `verified` (§7). An execution that a person could never see, or one that a person parked, gets no new instruction, and this is one.
4. *Route state.* `stopping`, with the deadline not yet passed.
5. *Order.* None. It can be claimed ahead of every ordinary notice, issued or claimed. `Wait` returns it before an open claim, and that claim stays open beside it.

**Reaching the agent.** Nothing is typed (rule 1), so the request reaches an agent when the agent next calls the daemon:

- An agent that holds its wait gets the notice id at once.
- An agent in a turn gets it sooner or later, by its adapter. Claude Code holds a wait while it works, so it gets the notice at its next tool boundary: the probe's agent read and acknowledged it in the same turn, when the command that was running ended (N24). It cannot cut into a running command. Codex gets the queued line when its turn ends (N30). Either gets it sooner in the reply to a notice call or a pair-message call that it makes meanwhile; while the route is stopping, each such reply names the final-report notice and the command that reads it.
- An agent that makes no call before the deadline is not reached. The current release could type into a working harness. On core `0.1.19` only a person can (rule 2).

**The deadline.** It is the stop request's grace period, counted from the moment the notice is issued, and journaled as a time. Each execution ends with one of these outcomes, and stop reports each by name:

| Outcome | Meaning |
|---|---|
| `reported` | The final-report notice was acknowledged before the deadline. The row holds the acknowledgement's outcome and the path of its record |
| `report_timeout` | The notice was issued. At the deadline it was unclaimed, or claimed and not acknowledged; the row says which |
| `report_unavailable` | The notice could not be issued. The row gives the reason: `not_waiting`, visibility `unverified` or `missing`, `identity_unknown`, or an adapter with no qualified wait |
| `not_reached` | The execution was already gone |

Termination follows the report or the deadline, whichever comes first (§12). No outcome is reported as another. A stop that could ask nobody says so.

**A daemon restart while stopping.** The state, the notice and the deadline are journaled. The restarted daemon goes on with the same stop operation. It issues no second notice, and it does not leave the stopping phase. No agent could reach the daemon while it was down, so the deadline is set once more, to one grace period from the restart. The journal records that it was moved, and a second restart does not move it again.

### A send cannot be recalled

Core types a send even when its caller has gone (N6), and its wait for a person to stop typing has no upper bound (`type_in` in `input.rs`). So, for the typed submits that remain:

- A send that has not returned is **pending in core**, not merely uncertain. The daemon keeps the call open. No time limit retires it, and nothing is sent in its place.
- A timeout on the caller's side, or a lost connection, is not the end of the operation in core. After a daemon restart, a journaled send with no recorded outcome is still pending.
- A pending send is resolved by core's response to it, or by the terminal finishing or being removed, which proves that it can no longer write. Nothing else resolves it. What the screen shows is not a receipt: a program can consume, erase or scroll away what it was sent, and a send that core still holds has shown nothing yet.
- A send with neither resolution is `unresolved`. Its execution takes no further typed submit. A person chooses: keep waiting, stop the execution, or send again knowing that both may be typed.
- Pause and hold report `dispatch_draining` while a send is pending and `unresolved` when it cannot be resolved (§12). They do not report that dispatch has stopped.

Notices do not have this problem. An issued notice that nobody claims has done nothing, and a hold or a stop refuses the claim of an ordinary one.

Persist states for typed submits such as `queued`, `pending_in_core`, `typed`, `rejected_before_write` and `unresolved`. A successful send is transport evidence, not task acceptance. `started:false` or `started:null` does not prove that nothing was typed; N15 returned `started:false` after its Enter had confirmed a dialog. A wait timeout is a check-in, not failure or completion. Send again at once only when the runtime explicitly proves rejection before any write.

### Current core gaps

Core `0.1.19` exposes conditional `if` values `idle` and `working`, but no expected execution/incarnation token. Preparation and the later paste and submit writes are separate steps: one status check, the paste, a 200 ms pause, then Enter, with no second check. Source inspection showed that, and N15 measured it. The shell-rejection probe (E11) establishes one guard behavior; it does **not** establish protection during delivery, against a state change or against loss and replacement. Nor does `if:"idle"` support an agent that remains at raw `done`. A send has no id, so it can be neither queried nor cancelled.

Requested from upstream:

- A per-program incarnation that changes on replacement, even if address and `created_ms` do not.
- A submit that checks that incarnation and the permitted state at every write, the delayed Enter included. No later step may reach a replacement program or a state the caller did not permit.
- Conditional delivery to the adapter's permitted states, including `done` where needed, while rejecting unknown/blocked states.
- An explicit distinction between rejection before write and partial/uncertain delivery.
- Equivalent incarnation protection for destructive stop or other queued control actions. `terminal.kill` takes an address only today.
- An id on each send, with a way to query and cancel it, and an optional deadline on the wait for human quiet that ends in rejection before write.
- The time of the last human input on the terminal record (§6, §12).

These are **proposed upstream capability requirements**, not existing RPC parameters. Prefer a small core API extension and regression tests because core owns the input queue. Do not invent undocumented arguments or claim read-before-write checks are atomic.

What gates an unattended release on `0.1.19`:

- No automated path presses Enter or sends keys (rules 1 to 4), and the §17 gates "Input race" and "No automated submit" pass.
- Every qualified harness is woken with no terminal input, acts on the wake and can be woken again, and the release notes say which wake and which start path each one uses ("Wake without input").
- No interrupt is sent to an execution that is not in a turn, and never two in a row ("Interrupt discipline").
- Notice states and the checks at claim pass their tests ("Notice lifecycle").
- The stopping phase, the open-claim rules and the wait limits pass theirs ("Stop reports", "Claim recovery", "Wait capacity").
- At least one harness in each coordinator role passes "Long run with no person". A harness that does not is released as attended, and the release notes say so.
- Stop checks, immediately before `terminal.kill`, that the address still holds the recorded process. The window that remains is a recovery shell that is seconds old; the release notes state it.

This replaces both earlier positions: that an unattended release waits for the whole upstream contract, and that notices alone make typed input safe enough. The upstream contract stays required before any automated typed submit, before typed text may carry task content again, before any automated answer to a dialog, and before any core version is called a certified minimum. Manual input and read-only inspection can be exercised at any time.

An idempotency key would improve delivery recovery but is not required to pretend exactly-once execution. Even with one, retain application-level message receipts and uncertain-outcome handling.

## 11. Observation and reconciliation

Use separate control and subscription connections. Plain NDJSON is sufficient for control/events; negotiate framing only for operations that require it. Bound message sizes and validate responses against the supported protocol contract. Unknown optional fields are tolerated; missing mandatory capabilities fail clearly.

On every connection:

1. Call `hello`; compare core identity with the persisted endpoint.
2. Subscribe and install the returned snapshot plus its sequence as a new local observation epoch.
3. Apply later events only within that epoch.
4. On `missed`, replace the cache with the supplied fresh snapshot, invalidate dependent assumptions, and reconcile.
5. On disconnect, discard ordering assumptions; resubscribe from a fresh snapshot.

Do not carry a high-water sequence across a core restart. Do not infer failure from gaps in a filtered stream; unrelated events may be filtered out. Handle label additions/removals as membership changes and use full records when provided. A label-filtered list can include remote addresses; V1 explicitly rejects nonlocal targets.

Maintain these separate reconcilers:

- **Execution:** desired launch/stop intents against core facts, process evidence, and registrations.
- **Identity/enrollment:** whether callers, pairs, and pending sends still bind current executions.
- **Inventory:** classifies every live terminal on the core as §6 describes, and raises a fault for a status-reporting child or an impostor.
- **Native visibility:** labels and persisted membership against app observations, respecting user moves/park/close.
- **Optional TUI view:** independently verifies mirror ownership and attachment.

Perform a full reconciliation on startup/reconnect and a periodic 30-second safety pass. Coalesce ordinary updates for up to 100 ms; process loss/finish/identity changes immediately. Retry unavailable endpoints with jittered exponential backoff from 250 ms to 10 seconds. A 45-second lack of subscription heartbeat or traffic makes the connection unverified; do not apply that timeout to unchanged agent status.

These are implementation defaults, subject to measured validation. They are not claims about upstream guarantees.

## 12. Pause, stop, resume, and crash recovery

### Pause

Persist the route hold before requesting interruptions. Stop new dispatch immediately: the hold refuses every new claim of a notice from that moment. A notice that is issued and not claimed is not announced under the hold; a wait stays attached and is not answered, and no wake line is queued (§10). A repeat read of a notice that was claimed before the hold still answers, so that an agent can find its record; it says that the route is held and that the work must wait. An acknowledgement is always accepted (§10). A person's typed submit that core already holds cannot be recalled (§10); report `dispatch_draining` until it has resolved, and `unresolved` when it cannot be.

What pause does to a turn that is under way depends on the adapter, because the two harnesses differ in what an interrupt leaves behind (§9):

- **Where an interrupt can be resumed with no person**, as in Claude Code, pause interrupts each verified execution that is in a turn, then observes the result. The turn ends, the claim stays open, and the wait that the agent started before its work stays attached (N23, N24).
- **Where it cannot**, as in Codex, pause sends no interrupt by default. The hold already refuses every new claim. The turn under way ends by itself, and pause reports `pause_pending` for that execution until it does. A person can ask for the interrupt, with the stated cost that a person must then type a line before the agent can go on (N30, N31). On Codex an interrupt also stops the model and not the command that is running, so the program reports `working` until that command ends.

Three rules hold for every adapter. An execution that only waits is left alone: there is nothing to stop, and an Esc at rest is the first half of a rewind in both harnesses (N23, N31). One interrupt at most goes to an execution for one pause; a second is never sent to make the first take effect. And the adapter says how an agent in a turn is told from one at rest, which is not the status alone (§9): the daemon uses the open claim.

Report `pause_pending` or `pause_partial` if an agent does not stop working; do not claim it paused from a successful RPC alone. Pause records, for each execution, whether it was interrupted, whether it holds an open claim and whether it can still be woken, so that resume knows what to restore. Preserve terminals and views. Do not escalate from interrupt to kill without the existing stop policy.

### Stop

Persist the stop intent with its grace period, and put the route in the stopping phase (§10). The phase refuses ordinary notices and typed submits, and it is not lifted for anything. Then:

1. Issue the final-report notice to each execution that can be asked. Record `report_unavailable` with its reason for each that cannot.
2. Wait until every asked execution has reported or the deadline has passed. Nothing is typed meanwhile.
3. Interrupt what is still in a turn, once. Stop each proven owned execution through the provider. Check, immediately before `terminal.kill`, that the address still holds the recorded process (§10). In Codex the interrupt stops the model and leaves its command running, and `terminal.kill` left a background command of Codex's alive with launchd as its parent (N31). Stop therefore ends the processes that the ledger records under the execution, not the terminal alone (§13), and the adapter's suite checks that nothing of the execution is left.
4. Verify termination before retiring active enrollment. Notices that were not handled become `returned`.
5. Record each execution's report outcome beside its termination result.

A stop of a paused route goes the same way. A Claude Code execution that the pause interrupted still holds its wait and gets the final-report notice. A Codex execution that a person had interrupted cannot be reached. It ends as `report_unavailable` with the reason `interrupted`, unless a person types a line there first.

Stop owned services using their recorded runtime/process references. Uncertain ownership is reported and left intact, following ADR 0016.

Native app panes generally disappear as terminals finish. Clean up only owned, empty containers after reconciliation. Never close a shared space or tab to stop one agent. Optional TUI mirrors are closed only after execution handling; closing them is not the stop operation.

### Resume

- From pause: verify the same live execution, readiness, approval, and visibility; then lift the hold. Then, for each execution:
  - If it holds an open claim, issue no continue prompt. Recovery is now due for that claim (§10). The wait that the agent holds returns it, with the note that the route was paused and resumed, and the agent goes on from its record or closes the claim with an outcome. The probe's Claude Code agent did so with nothing typed (N24). A Codex execution that was not interrupted has ended its turn by now, so its claim is handled or the queued line reaches it.
  - If one of its ordinary notices is issued and not claimed, issue no continue prompt either. Lifting the hold makes that notice claimable, and the daemon then announces it to the wait or queues the wake line (§10).
  - Otherwise issue the continue prompt as a notice.
  - If it cannot be woken, resume says so. A wait-holding execution with no wait is reported as `not_waiting` with the start line. A Codex execution that was interrupted is reported as `needs_attention` with the reason `interrupted`. In both cases a person types a line in the pane (§10). Resume reports `resume_partial` until every execution can be woken again.
- From stop/loss: allocate a new execution ID/generation and launch through the same transaction. Preserve files and task history.
- Use native resume only when the adapter has a verified conversation ID and compatible resume contract. If no such evidence exists, show an explicit fresh-conversation recovery plan rather than claiming conversation continuity.
- Re-enroll new executions and invalidate old caller bindings. User-authorized route recovery may authorize the new pair under the established launcher rules; an agent caller cannot self-approve it (§6).

### Recovery shells

When a kept terminal loses its program, core starts the login shell at the same address, with the same labels and the launch `env` (N2). That happens on signal death and holder loss, and to every kept terminal at once after a core stop and start or a reboot (N8). The shell runs the user's startup files. It is never resumed work.

1. Mark the old execution lost and revoke its readiness. Keep the shell visible as recovery evidence. Type nothing into it; a launch command typed there as an automatic repair is forbidden.
2. A replacement execution gets a new terminal and a new `herdr.tab` ID in the route's space. It never reuses the tab that holds its predecessor's shell.
3. When many executions are lost together, offer one recovery plan for the route, not one prompt per agent.
4. Herdsman never kills a recovery shell by its own decision. The first revision removed one that had no child process and showed only its first prompt. Neither fact shows that a person has not adopted it: a person can change directory, set variables and define functions there, do work, and clear the screen. Core does not say whether anyone has typed in a terminal.
5. The resource ledger already has the rule. A recovery shell is another process than the one its execution recorded, so its evidence is never `exclusive`. It is `associated` at most. `stop`, route close and `teardown` list each one with its address and the lost execution it replaced, and leave it, following ADR 0016. A person closes it in the app, or names it to Herdsman as any other associated resource is named: `--include associated`, or a yes for that one resource. No automation gives that answer for a recovery shell.
6. A stopped route can therefore leave shells in its space, and status says how many. Automatic cleanup can return when core reports the time of the last human input on a terminal (§10). The rule is then: the shell started after the loss, no human input since its start, ownership proven by the lost execution's record and label, and the incarnation unchanged at the kill.

One probe is owed before step 2, because it needs the app: whether `keep` can be set to false after adoption and stay false. If it can, a loss leaves a finished record and no shell. The address-reuse hazard of E10, the inherited `env` of N2 and most of this subsection then go away at the root, and step 3 of §8 changes to match.

### Failure matrix

| Failure or external action | Required behavior |
|---|---|
| Herdsman daemon restarts | Reconcile durable intents, identities, approvals, and outbox before dispatch; adopt verified live executions |
| Core connection/server restart, process survives | New event epoch; revalidate continuity; do not relaunch |
| Core/holder loss replaces program with shell | Mark execution lost/replaced; revoke readiness and old callers; no automatic prompt |
| Core stops and starts, or the machine reboots | Every kept execution is now a shell (N8): mark all lost, type nothing, offer one route recovery plan |
| Something in a terminal prints a status record | No effect on identity, enrollment or readiness; a send still needs a registered execution |
| Core identity changes | Endpoint mismatch; freeze mutations and require explicit recovery |
| App closes after verified membership | Keep running; show app closed separately; preserve membership |
| App closed during a launch, binding verified earlier | Launch and dispatch go on with visibility `pending`; confirm when the app is reachable |
| App binding never verified | Waiting for visibility; no first notice; retain recoverable intent |
| App reachable but placement absent | Visibility `missing`; hold dispatch to that execution |
| App moves/renames view | Update observed binding without changing execution identity |
| User parks terminal | Hold future dispatch; restore only on explicit show/resume |
| User closes native terminal | Execution externally stopped; preserve task history; no resurrection loop |
| Optional TUI goes down | View degraded only; core and app continue |
| Spawn reply lost | Inspect live/finished/pending records; adopt or mark unknown; no blind duplicate |
| Reply to a person's typed submit lost | Still pending in core; no replay. Resolved by core's response or the terminal's end; otherwise `unresolved`, and a person decides |
| Typed submit still pending in core at pause, stop or daemon restart | Report draining; no second send, whatever time has passed |
| A dialog opens between a typed submit's check and its Enter | Possible on core `0.1.19` (N15). No automation types, so only a person's own action can meet it |
| Notice issued and not claimed | Nothing has happened. It stays issued; an execution that holds no wait is reported as `not_waiting` |
| A notice is issued, and a gate closes before it is read | The read is refused with the reason, once. The next wait stays attached and is not answered, and no wake line is queued, until a gate changes. Then the same notice is announced. `notice_held` with the gate's name after the grace period |
| A replaced wait command tries again after it lost the `replaced` answer | `replaced` again, every time. The owner is the highest wait generation, also after a daemon restart, and a renewal never makes an owner |
| Notice claimed, then its execution is lost | The notice is `returned`. Recovery issues a new one with the old processing record, or a person abandons it |
| An agent reads a notice it has already claimed | Same token and content, marked as claimed before; the action is not repeated blindly |
| A turn is cut short after a read and before the acknowledgement, and the process lives | The claim stays open. The next `Wait` returns it before any newer notice. The agent goes on from its record, or acknowledges `interrupted` or `abandoned`; later notices follow |
| An agent ignores an open claim and keeps waiting | The same recovery answer at the end of each poll; `claim_stuck` after three |
| A wait-holding execution holds no wait | `not_waiting` after the grace period. The agent starts one again when its harness tells it that the wait ended (N20), or a person submits the start line; resume says `resume_partial` until then |
| A Codex execution is interrupted, by pause at a person's request or by a person's Esc | Its queue is held. `needs_attention` with `interrupted`; nothing is queued until it calls again. A person types a line |
| A queued wake line gets no answer | `wake_unanswered` after the grace period; its notices stay issued |
| A dialog opens in a harness at rest and holds its wake | The status says `blocked`. `needs_attention` with the dialog's name; a person closes it in the app. Herdsman sends no key (§10) |
| A second interrupt would be sent | It is not. One interrupt for one pause, and none to an execution at rest |
| Stop, and an execution holds its wait | It gets the final-report notice at once. Termination follows its acknowledgement or the deadline |
| Stop, and an execution is in a turn | Claude Code gets the notice at its next tool boundary, Codex when its turn ends (§10). With no call before the deadline: `report_timeout`, then interrupt and stop. Nothing is typed |
| Stop, and an execution cannot be asked | `report_unavailable` with the reason; stop goes on |
| The daemon restarts in the stopping phase | Same stop operation, no second notice, ordinary notices still refused; the deadline moves once |
| A prelude step fails | The wrapper exits with a code. The launch fails at `authenticated`, never registered or ready. The granted secrets were in that process only |
| More executions than wait slots | The launch is refused in preflight. Running agents keep their waits |
| Two waits from one execution | The newer one replaces the older, whose command exits |
| A waiter's connection is lost | Its slot is freed at once; the execution counts as `not_waiting` after the grace period |
| The context passes the checkpointer's limit in a harness that does not compact by itself, or whose automatic compaction the user turned off | `needs_attention` with `compaction_due`; nothing is typed, and the other agents go on |
| A Codex goal pauses | `needs_attention` with `goal_paused`; a person submits `/goal resume` |
| A member starts an agent of its own through core | Inventory fault as soon as it is recognised by a status report or a harness name; channel authority returns to the person until it is resolved |
| A member starts a terminal that cannot be classified | Listed as unclassified; no fault under the default profile, a fault under the restricted profile |
| A recovery shell is left after stop or route close | Listed with its address; never killed unless a person names it |
| Labels removed or copied | Reconcile using stored execution evidence; labels alone neither adopt nor destroy; route labels without a registration are an inventory fault |
| Two controllers claim one intent | Unique constraint/single-writer conflict; one proceeds, other observes |
| Protocol or harness unsupported | Explicit capability error before first task dispatch |

## 13. Services, policies, and resource accounting

Courier, cardwatch, checkpoint, and other supporting services must stop depending on shell commands typed into TUI panes. Register them as daemon-managed services or core executions according to their existing lifecycle needs. Choose one owner per service; do not run duplicate daemon and terminal copies.

Where a service requires an agent target, pass a typed execution reference rather than `TARGET=<pane-id>`. Checkpoint guards and late reports must reject stale execution generations. Supporting services may be ordinary terminals without OSC status; do not invent agent entries for them. Their health remains available through Herdsman diagnostics.

Apply existing `confinedArgs`/profile logic before launching a core agent. Preserve roots, grants, protected paths, secrets policy, prelude ordering, budgets, and reviewed-head constraints. Core is not a sandbox. Do not use a default shell or `hc` template that quietly drops restrictions.

### The worker socket

Under the restricted profile a worker's commands run inside the harness's sandbox. They cannot connect to core (N9), which is wanted. They cannot connect to the daemon's main socket either (N18), so they could not fetch a notice. Opening the sandbox to the network or to core to make that work is not allowed. The path is a second daemon socket:

- The daemon listens on a worker socket in its private run folder, beside the main one. It serves `NoticeService` (§10) and nothing else: no pair approval, no route control, no configuration, and no way through to core.
- A caller is identified as on the main socket: the socket peer's process, walked up to a registered execution (§6). The service answers only for that execution's own notices. A caller that belongs to no execution gets nothing.
- The wrapper gives the harness the socket's path in a variable that is not a secret, and the thin client uses it. Every managed agent uses this socket for its notice calls, restricted or not, so one path is tested. `Wait`, `Read` and `Ack` are served here only. Without the variable the notice verbs fail and say that they serve managed executions.
- The socket has its own request limit, apart from the main socket's, and an attached wait holds no request slot on it. Agent traffic therefore cannot take the slots that route control and lifecycle calls need, and a crowd of waits cannot take the slots that `Read` and `Ack` need. §10 gives the numbers and the rules for a duplicate or a lost waiter.
- The profile allows that one socket in the sandbox by its exact path. `codex sandbox --allow-unix-socket` showed that one socket can be allowed while core's stays refused (N18). A normal Codex launch takes the same allowance through three settings together (N27): the feature `network_proxy`, a permissions profile with `network.enabled = true` and the socket's path set to `allow` under `network.unix_sockets`, and that profile as `default_permissions`. A model-run command then reached the worker-shaped socket and was refused core's socket and a direct TCP connection. The `unix_sockets` entry does nothing without the feature. The restricted profile builds Codex's sandbox today from the older keys, `sandbox_mode` and `sandbox_workspace_write.writable_roots` (`SandboxConfig` in `internal/profile/restricted.go`), and those keys have no place for a socket. On core it builds a fresh, randomly named permissions profile per launch so merged lower configuration layers cannot retain grants from an earlier profile: the writable folders as `write` entries under `filesystem`, which the probe showed to work beside the socket entry, and the network block above. `CheckHarnessArgs` then also refuses a user argument that sets `permissions`, `default_permissions` or the `network_proxy` feature. The TUI backend keeps the older keys, so no existing launch changes. The feature is marked experimental in Codex `0.164.0`, so the Codex adapter pins the versions where its suite passed. Linux is still owed (§18). Each adapter records its setting (§9).
- **Network access is not allowed to open core.** With plain network access, by a profile without the proxy feature or by the older `network_access` setting, a sandboxed Codex command reached core's socket, and a `deny` entry for that socket changed nothing (N28). Under the restricted profile the Codex adapter therefore refuses a launch whose sandbox has plain network access, naming the setting. A worker that needs the network gets it through the proxy feature, where a direct connection and core's socket stay refused.
- A harness whose sandbox cannot allow one socket by path is not qualified for the restricted profile on core. The sandbox is not widened for it. The restricted profile sets a sandbox for Codex only today, and refuses every other kind, so the question does not arise for Claude Code until that changes.

The release gate is one combined test with a real restricted worker (§17, "Restricted worker").

### Launch commands and environment

Construct argv arrays, never shell strings assembled from prompts or titles. The released `-a` and `-o` values are command lines that may carry a prelude, such as `claude doctor && claude --model opus`. The wrapper runs them through the existing launch grammar, and the string is never joined with a prompt, a title or a label. Prelude steps keep their `.start` and `.rc` marker files and their retry rules. The wrapper runs them in the core terminal after it is authenticated and before it registers the execution (§6). They run in the launch environment below, so a prelude step sees the variables that the harness sees, granted secrets included. That is what a prelude sees today, where the pane's environment is loaded before the prelude is typed into it (`paneEnv`). A launch such as `private-auth-check && codex …`, whose first command needs a granted token, therefore works on both backends. The one argument Herdsman may add is the adapter's start line, as a separate argument and only under the exception that §10 describes.

A core terminal starts with the environment of whatever started the core, plus core's own `HC_*` variables (N7). That environment can hold another agent's markers, session id, tokens and API base URL, and it outlives core updates and restarts. Core's `env` map can add to it and cannot remove from it. The wrapper therefore starts from an empty environment and adds from an allowlist:

- The user's login values and `PATH`. The adapter decides whether they come from the launching command or from a login shell that the wrapper runs, and the descriptor records which.
- The harness's documented variables, under the existing environment policy.
- Core's own `HC_*` variables, unchanged. With them, a terminal that the agent starts through `hc` is recorded with its parent and can be counted. Removing them would not remove the agent's access to core, only the record.
- Secrets that the policy grants, fetched once the wrapper is authenticated (§6). The grant belongs to the launch, as it does today. There is no narrower grant for one step, and a secret the policy denies is in neither the prelude's environment nor the harness's.

The wrapper never passes `HERDR_PANE_ID`, or another agent's parent, caller or scope. `runtime doctor` shows where the environment came from and names any agent markers it finds in the core's own environment. Preserve required authentication through existing policy mechanisms without copying secrets into diagnostics.

### Resource accounting

Terminals that a managed agent starts through core are counted through their `parent` chain: against the member's budget, and in the resource ledger for teardown. Their class decides whether they are a fault (§6): an agent child always, an unclassified child under the restricted profile, a declared command never. Only a declared command is `exclusive` in the ledger; the others are `associated` and are not stopped without a person's yes.

Record core endpoint/address, execution ID, OS process evidence, services, and detached resources in the resource ledger. Killing a terminal alone is not proof that every detached process it started is gone. Existing teardown ownership rules remain authoritative.

V1 keeps terminal records with `rm:false` for recovery. After final outcome, acknowledged messages, and journal evidence are durable, garbage collection may remove proven owned finished records under the existing retention policy. Do not remove active, uncertain, foreign, or unresolved-recovery records. Clearing terminal history is distinct from deleting a route or worktree. Core keeps each record's launch `env` in plain text for as long as the record exists (N1). Nothing sensitive is in it under §6, and removing the record removes that copy too.

## 14. Optional Herdr TUI support

### Legacy execution backend

`backend="herdr-tui"` retains native TUI agent creation, readiness, session identity, and pane-based behavior behind the provider. A new route gets it from the option, the config file, or the installation default of an existing installation (§5). It is implicit when decoding existing legacy route state. It does not claim automatic visibility in the new desktop app. Users who require the new app's visibility must use core; no reciprocal mirror is part of V1.

### Optional core-to-TUI view

This is a convenience adapter, not a default dependency or an identity provider. Its narrow contract is:

- Mirror only executions owned by the configured Herdsman installation/route on one verified core.
- Create dedicated, discoverable TUI containers; never type into an arbitrary existing root pane.
- Attach by immutable core address with an explicitly scoped endpoint, not a reusable friendly name.
- Use a controlled attach helper that registers its binding and can reattach after transport loss. An unbounded shell loop is not a recovery policy.
- Validate TUI session identity, internal terminal identity, and helper registration before input/report/close operations. Public pane IDs may change after moves.
- Use custom display labels if needed, but keep harness kind separately. Do not call TUI `agent.prompt` for core agents or treat report acknowledgements as native readiness/session evidence.
- Persist creation intent before creating a view. Use a unique creation label on a dedicated tab/workspace so a crash before mapping commit can be reconciled. An ambiguous orphan is not permission to reuse a user's shell.
- Serialize reports through one owner per binding with durable increasing sequence numbers. Core event sequence is not the TUI report sequence.
- On status-source loss, expire the display metadata. The implemented adapter installs no persistent agent lifecycle override and publishes no native session identity. Its helper has a fifteen-second daemon lease, reports metadata with a three-second TTL and durably increments its own sequence before each report. If a future adapter adds persistent lifecycle overrides, it must also implement an independent cleanup watchdog; metadata TTL would not expire those overrides.
- User mirror close/detach does not kill the execution and is not immediately undone. An explicit show/reattach action recreates it. App visibility remains the primary contract.

Keep mapping, helper and lease state in Herdsman's private durable state directory, with atomic writes and serialized ownership. A separate `hc doc` map is unnecessary. If an independent bridge later uses core documents, it must use a valid name such as `hc-bridge-map`, proper version CAS, singleton ownership, and its own recovery specification.

The optional adapter must not delay the core backend's release once core and native app gates pass. Its own tests gate enabling that option.

## 15. Codebase changes

These are the principal verified seams, not a complete file-edit checklist.

| Area | Current coupling | Required change |
|---|---|---|
| `internal/route/up.go` — `UpRequest`, `Deps`, `applyAgent`, `probeAgent` | TUI client, pane creation, agent-kind/name lookup, `AgentStart` | Persist backend; inject runtime provider; launch/reconcile executions |
| `internal/route/control.go` — stop/resume, `applyClosePane`, `applyLastCall` | Pane closure stands in for execution termination; the last call is typed and its wait reads the agent's status | Provider stop/interrupt/resume and separate view cleanup; the stopping phase with a final-report step for each execution (§10, §12) |
| `internal/route/workspace.go` — `ensurePane`, `applyService` | Services and target agents are pane-bound | Runtime service records and native app placement |
| `internal/route/restricted.go` — `confinedArgs` | Existing sandbox/profile conversion | Reuse for both providers; no policy downgrade |
| `internal/herdr` | TUI RPC transport/types/read/prompt | Retain as legacy adapter; add separate core client and app adapter |
| `internal/inbox/registry.go` — `LiveIdentity` | Requires TUI `interactive_ready`, pane fields, and native session | Typed execution identity and verifier, with explicit legacy decoding |
| `internal/daemon/store/registry.go`, `internal/daemon/pair.go` | Enrollment bound to pane/session | Execution-aware enrollment and pair generation |
| `internal/daemon/server.go`, intake and route inbox | Caller identity resolved through TUI | Callers identified by process, then checked against the persisted binding (§6) |
| `internal/message`, `internal/checkpoint`, `internal/watch` | TUI status/events/readiness/input assumptions | Shared runtime reads, events, delivery, and stale-generation rejection |
| `internal/status`, `internal/web` | Pane-centric status and controls | Runtime identity, visibility and uncertainty surfaced distinctly |
| `internal/ledger` | Process/resource ownership and revalidation | Core references plus execution/process evidence; preserve conservative teardown |
| `proto/herdsman/config/v1/config.proto`, generated SDK/config docs | No runtime selection | Add schema fields and regenerate all derived surfaces |
| Launch commands, setup/doctor, automations, task-worktree launch paths | Potential implicit TUI dependency | Same backend resolver and capability checks as `up` |
| `cmd/herdsman/cmd_up.go`, `internal/catalog/pipeline.go` | Harness kinds are read from `herdr agent start --help`; `up` also runs `herdr server` and `herdr session attach` | Kinds come from the adapter registry; no `herdr` call on the core path |
| `internal/route/attest.go`, `internal/daemon/route_service.go` — `attest`, `inspectCaller` | Attestation reads TUI panes and harness ancestors | Core terminal resolution (§6), shipped first for the current release |
| `internal/inbox/registry.go`, `internal/inbox/boundary_v2.go` — `BoundaryOK`, `BoundaryV2` | Inventory of every agent on the Herdr server | The core inventory of §6, with the restricted-profile rule kept |
| `internal/route/restricted.go` — `paneEnv` | Keychain secrets passed as the pane's environment, which the prelude also sees | Fetched by the authenticated wrapper before the prelude; nothing secret in `terminal.run` |
| `internal/daemon/server.go`, `internal/daemon/event_service.go` — `limit`, `streamSlot`, `EventService.Wait` | 32 requests in flight; stream slots for events and parties; the event wait gives its request slot back and holds 30 seconds at most | Notice waits follow the event wait's pattern with a budget of their own; the worker socket has its own request limit (§10, §13) |
| `internal/checkpoint/checkpoint.go` — `Cycle` | Types the checkpoint prompt, then the compact command when the context is over the limit | The prompt is a notice; no compact command on core; `compaction_due` where the harness does not compact by itself (§10) |
| `internal/message/resume.go` — `resume-goal` | Types `/goal resume` into a Codex target whose goal is paused | On core it types nothing and reports `goal_paused` (§10) |
| `AgentPrompt` call sites: `internal/route/up.go`, `internal/route/control.go`, `internal/message/resume.go`, `internal/checkpoint/checkpoint.go`, `internal/daemon/task_service_wtree.go` | Task content typed into the pane | Notices and adapter control lines (§10) |
| `internal/checkpoint/checkpoint.go` | The transcript is found through Herdr's `agent_session.value` | Conversation evidence from the adapter; the checkpointer refuses to guess when there is none |
| `internal/route/check.go` (C3.2) and the route file | A required Pane column | A runtime reference column beside it; Pane stays for TUI routes, and the check accepts either by backend |
| `internal/watch/common.go`, `internal/watch/wait.go`, checkpoint `TARGET` | Pane IDs in `TARGET`, `HERDSMAN_PANE_<name>` and `HERDSMAN_REVIEWER_PANE`, with default panes built in | Typed execution references; the old variables keep working for TUI routes |
| `internal/web/actions.go` | Keyed by pane ID; forwards Herdr methods generically | Runtime references and an explicit action list per backend |
| Released SDK messages and golden files | `UpCaller.pane`, `PeerInfo.pane` and `session`, `InventoryAgent.pane`, `herdr_socket` | Fields stay; runtime references are added beside them (ADR 0003) |
| `internal/daemon/store/schema` | `pane` and `session` are `NOT NULL` in the route store; no installation default in the account store | New execution tables; one account-store migration for the default (§5) |
| `skills/herdsman` | Works through the `herdr` CLI and requires `HERDR_ENV=1` | A core path in the skill, or a stated TUI-only scope until it has one |
| `docs/threat-model.md`, ADR 0024, ADR 0025 | Written for the TUI | The rows and limits named in §6, §8 and §16; the new ADRs of §18 |
| `internal/ops` — the operation engine and its outbox | `up` steps and outbox rows know panes and typed prompts | Launch steps whose reconciliation verifies process identity; notices as outbox rows; a person's typed submit as a `never` step (§19) |
| `internal/launch` — the launch spec, `wrapper.go`, the markers | The wrapper is submitted with `pane.run`, and the agent goes through `herdr agent start` | A core wrapper that is authenticated by process, runs the prelude in the launch environment, registers the execution and runs the harness, with the same marker files (§6, §8, §13) |
| `internal/message/read.go` and the inbox's claim discipline | Pair messages only | Reused for notices: claim token, processing record, acknowledgement (§10) |
| `internal/profile/restricted.go` — `SandboxConfig` | Filesystem sandbox settings only | The worker socket allowed by its exact path, and nothing wider (§13) |
| `internal/ledger` — evidence levels and revalidation | Processes, ports and containers of a task | Core terminals with their process identity; a recovery shell and an unclassified child as `associated` (§12, §13) |
| `internal/guard/imports_test.go` | A server list without runtime packages | The new packages join the server list (§19) |
| `acceptance/`, `acceptance/requirements.md` | No core or app capability | The gates of §17, and `herdr-core` and `herdr-app` as declared capabilities (§19) |
| `internal/tui/forms/up.go`, the web's route page | No backend choice; pane-centric state | A backend field in the form; runtime state, visibility and unresolved items on the page (§19) |

Suggested package boundaries: `internal/runtime` for the provider/domain contract, `internal/hc` for core protocol, `internal/herdrapp` for native visibility, and `internal/runtime/legacytui` for the existing execution adapter. Avoid putting routing policy in protocol clients. The core client is Herdsman's own code over newline-delimited JSON; it adds no third-party dependency.

Audit graph callers of `LiveIdentity`, `AgentGet`, `AgentPrompt`, `ReadPane`, and pane lifecycle operations before declaring migration complete. A new backend that works only for `up` is incomplete.

## 16. Diagnostics and security

`runtime doctor` should report resolved backend, endpoint/core identity, negotiated features, app endpoint/membership, harness qualification, sandbox compatibility, and specific failed gates. It must be useful with no TUI installed. It also reports:

- Where the backend choice came from: the option, the config file, or the installation default.
- Whether the app-to-core binding has been verified, and when.
- Where a launch's environment would come from, and any agent markers in the core's own environment (§13).
- Which inventory rule is in force, how many foreign agents the core holds, and any fault with the terminal that caused it (§6).
- Whether core can read the repository and worktree roots. The upstream manual says that macOS privacy requests from terminal programs belong to Herdr Core, not to Herdr.app, and that existing grants do not transfer. A repository under Documents, Desktop, Downloads or an external volume can therefore meet a system prompt or a denial that no status reports. The check runs through core (`machine.exec`).
- Whether the app supports this machine (§5).

Status output should include logical agent, role, execution generation, runtime address, process/readiness state, last verified time, app binding, view health, and uncertain operation IDs. An app's attention badge should not replace runtime state.

Use structured events for launch transitions, identity changes, resyncs, visibility changes, blocked dispatch, uncertain input, and teardown outcomes. Redact prompts, environment secrets, credentials, and terminal content by default. Diagnostic bundles should be explicit and bounded.

Use private same-user sockets and state permissions consistent with Herdsman's threat model. Validate endpoint type/ownership and avoid unexpected symlink or path changes. Core control is broad same-user authority; it does not provide per-route authentication. Labels and documents are not trust boundaries. Existing sandbox restrictions and daemon peer/caller validation remain necessary. See [core security](https://github.com/herdrdev/herdr-core/blob/v0.1.19/docs/security.md) and [Herdsman's threat model](../docs/threat-model.md).

The threat model gains these rows when the backend ships:

| Input | From | Handling |
|---|---|---|
| Terminal status (OSC 7501) | Any program in a core terminal | It gates dispatch, so it is no longer only displayed. It can block a send and can never permit one alone or prove identity (§9) |
| Core API | Any process of the same account | Not a boundary. Labels, names and a label-scoped list prove nothing; registration and process evidence do (§6) |
| Core state on disk | Core, in plain text | Holds each terminal's launch `env`, screens and history. Herdsman puts no secret there (§6) |
| Other agents on the shared core | The user's other work | Outside the route's inventory under the default profile, a fault under the restricted profile (§6) |
| Typed input into a core terminal | Herdsman, through core's input queue | Text plus Enter, with one check before the paste. Not safe by its wording. No automation uses it on core `0.1.19` (§10) |
| Worker socket | Commands of a managed agent, sandboxed or not | Serves the notice calls of the calling execution only, identified by process. Not a way to core or to any other daemon service. Its waits and requests have their own limits, so an agent cannot use up the main socket (§10, §13) |
| A harness's own message queue | The daemon, for Codex; and any process of the account that can write the harness's home | The daemon queues one fixed line with no content, to a registered execution's own conversation. It is not a boundary: another process can queue text for a session, as it can type through core. Content is pulled and every gate is checked at the claim, so a queued line gives no access to a notice (§10) |
| Final-report notice | The daemon's stop operation | Fixed text, one for each execution, only while the route is stopping. No agent and no API call can issue one, and it carries nothing from one agent to another (§10) |
| Launch environment before registration | An authenticated launch wrapper | Granted secrets reach the prelude and the harness, as today. Authentication gives no standing as an agent (§6) |
| Terminals a member starts | A managed agent, through core | Classified by evidence. An agent that reports nothing and carries no known name is not recognised (§6) |
| Recovery shells | Core, after a loss | May be a person's shell by now. Never killed unless a person names it (§12) |

Do not auto-update core, the app, or harnesses. Diagnose missing capabilities with the required contract/version evidence. Do not silently install app mods, rewrite shipped layouts, or modify global core settings such as notifications merely for a route.

## 17. Acceptance tests and release gates

Tests must exercise behavior, not just mirror adapter internals. Use private core/app/TUI homes and sessions; fixtures must never stop a normal user's server. At least one visual app inspection and real harness run is required for each supported release combination.

| Gate | Required evidence |
|---|---|
| Core path | A launch on a new installation uses core, succeeds with TUI binary/server absent, and persists backend |
| Native visibility | Real coordinator and worker appear in correct app space/tab/pane and Agents list; selected terminal is interactive |
| First-dispatch gate | No notice can be claimed before registration, a first status report, readiness, approval, a verified app binding, and labels read back from core. Nothing is typed at any point |
| Endpoint mismatch | App on another core/session cannot falsely verify a same-named/same-address terminal |
| Real harness qualification | Codex and Claude: startup, idle, work, blocked permission/auth, completion, interruption, stop, and supported resume; policy limits retained. Each fact in the table of §9 is checked again for the harness version and the model in use, more than once |
| Codex process ownership | `--no-daemon` enforced and actual work remains under the intended runtime/process ledger |
| Identity replacement | Kill a kept harness; same-address shell does not receive prompts, inherit approval, or pass checkpoint/caller verification |
| Input race | Two fixtures: a program that turns from idle to a dialog when the paste arrives, in one process (N15), and one that is lost or replaced during the human-quiet wait, the paste and the delayed Enter. On core `0.1.19` no automated path sends to either. The fixtures stay in the suite and record what core does. An adapter may turn automated submit on only for a core version where both show the Enter withheld |
| State guards | Unknown, blocked, error, starting, and unsupported `done` paths cannot bypass delivery policy |
| Launch crash points | Kill daemon before/after run request, reply, record commit, adoption, enrollment, first dispatch; no duplicate or hidden successful launch |
| Send uncertainty | Drop the reply to a person's typed submit, during the human-quiet wait and after typing. No replay at any age, and none because the screen lacks the text. It resolves on core's response or the terminal's end; otherwise it is reported as unresolved with the three choices |
| Reconnect | Daemon/app/core restart preserves correct surviving execution, rebuilds event state, and does not replay task text |
| Events | Snapshot/event race, sequence reset, forced missed events, label membership changes, finished records, and remote records |
| User UI actions | App quit preserves execution; park holds dispatch; move/rename preserved; close ends execution without resurrection |
| Pair boundary | An agent that runs `up` as an ordinary command, directly or through core, cannot self-approve; doctor fails on a holder it does not know; stale caller bindings and late reports fail after execution replacement; view changes preserve valid pair |
| Lifecycle | Pause reports partial results accurately and leaves an idle waiter's wait in place; it interrupts a Claude Code turn and leaves a Codex turn to end; stop ends core executions and owned services, a command that Codex left running among them; resume rebinds correct identities and reports an execution that cannot be woken again |
| Interrupt discipline | Against a core that records its calls: pause and stop send no interrupt to an execution at rest, and never a second one to the same execution. In each qualified harness, one interrupt at rest opens no dialog. A harness that shows its rewind view, or any panel, at rest is reported as `needs_attention`, and no key is sent to it |
| Teardown | Foreign, uncertain, moved, or user-taken-over resources are left intact; detached owned services handled by ledger |
| Migration | Old TUI routes continue; explicit TUI works; an older binary refuses the migrated store safely; rollback by config works on the new binary |
| Upgrade | An existing installation upgraded across the switch keeps `herdr-tui` with no config change. A new installation and a new `config init` file select core. `config update` adds no backend |
| Public surfaces | CLI, daemon API, web, SDKs, automations, task launches, and checkpoint workflows use the same runtime contract |
| Optional TUI | Mirror create/attach/report/reattach/close, lost events, stale report expiry, moved panes, and collision/crash recovery |
| Platform | macOS and Linux isolated suites pass before claiming support for each; no Windows claim |
| Secrets at rest | A marker secret granted to a launch is absent from core's state folder after launch, after loss of the program, and after finish. The recovery shell's environment does not hold it |
| Launcher attestation | `hc run -- herdsman up …` from a managed agent does not approve a pair. The same command typed by a person in a shell pane of the app does |
| Caller identity | A command run by the harness is identified as its execution. The same command from a recovery shell at the same address, or from a process outside the tree, is not |
| Inventory | A status-reporting child of a member, a child that runs a known harness and has reported nothing yet, a headless agent under a known harness name, and a terminal with copied route labels each raise a fault. A child registered with `resource add` does not. A child that is neither is listed as unclassified: no fault under the default profile, a fault under the restricted profile. An unrelated agent on the same core does not raise one under the default profile and does under the restricted profile |
| Unattended launch | With the app closed and the binding verified earlier, a new execution launches and claims its first notice. With no verification ever, it waits |
| `if` and first status | No typed submit is issued without `if`. A harness waiting at its update or trust dialog receives nothing, Enter included |
| No automated submit | Against a core that records its calls, dispatch, resume, checkpoint, automations and queued web actions are each exercised, and none calls `terminal.send` or `terminal.keys`. `terminal.interrupt` at each qualified harness's dialogs confirms none of them |
| Wake without input | For each qualified harness: it is woken with no terminal input, acts on the wake, and can be woken again after its turn. Claude Code holds its wait in the background, also while it works. Codex gets one fixed line through its queue, at most one outstanding, and none under a hold. One that cannot be woken is reported as `not_waiting` or `wake_unanswered`. The start path it declares works with its startup dialogs present |
| Notice lifecycle | A notice is claimed once. A second read by the same execution returns the same token, marked as claimed before. A read after handling returns the result and no instruction. A new claim of an ordinary notice by another execution, by an older generation, during a hold or while the route is stopping is refused and leaves the notice issued. An execution killed after its claim leaves the notice returned, and its replacement gets a new one with the old record |
| Stop reports | Stop a route with four agents: one that holds its wait, one with an ordinary notice issued and unclaimed, one that waits with a claim open, and one in a turn longer than the grace period. Ordinary notices and typed submits are refused throughout, and the hold is never lifted. The first three handle the final-report notice inside the grace period and end after it. The fourth ends as `report_timeout`. An execution with visibility `missing` ends as `report_unavailable`. Nothing is typed. Repeat with a daemon restart in the stopping phase: no second notice, no ordinary claim, and the deadline moved once |
| Claim recovery | Interrupt a Claude Code agent after its read and before its acknowledgement, and resume. The wait it started before its work returns the open claim, with nothing typed. The agent goes on from its record or acknowledges `interrupted`; the action is not repeated blindly, and the next notice then becomes claimable. Repeat with a daemon restart in that state, with the agent's context cleared, and with a turn that a person ended, where the claim comes back after the grace period. For Codex: the same interrupt leaves `needs_attention` with `interrupted`, and after a person's line the queued wake brings the open claim back |
| Wait capacity | More than 32 executions across routes hold their waits. Notice reads, acknowledgements and main-socket control calls keep their normal answer times. A second wait replaces the first, whose command exits. A hundred reconnects in a row leave one slot in use. A waiter that disconnects frees its slot and counts as `not_waiting` after the grace period. A launch beyond the wait budget is refused in preflight |
| Held notices | Issue a notice, pause before its read, and start a new wait again and again. The read is refused once. No later wait is answered and no wake line is queued while the hold stands, and the daemon sees no tight loop of requests. Resume announces the same notice once, and it is claimed once. Repeat with visibility `missing` and with a withdrawn approval; with two notices, to show the order; with an open claim under a hold; and while stopping, where only the final-report notice is announced. For Codex: one wake line when the gate opens, with no new notice issued |
| Wait ownership | Start wait A, replace it with B, discard the `replaced` answer to A, and let A connect again: B stays the owner and A exits. Repeat with the answer to A's first call lost, with late and repeated copies of A's requests, with both commands losing their connections at once, and with a daemon restart after which the two connect again in either order. Exactly one command owns the wait throughout, and the owner changes only when a new command asks for the wait |
| Prelude environment | A prelude step reads a marker secret that the policy grants, and a secret it denies is absent from the prelude and from the harness. The marker is absent from core's launch record and from the recovery shell. A launch whose prelude fails is never registered, ready or enrolled, and leaves no shell |
| Long run with no person | For each harness qualified as unattended: a coordinator on core handles a checkpoint notice, the harness compacts by itself in the middle of a turn, the agent finishes that turn, is woken again and claims its next notice, with no terminal input and no person. A pause and a resume in that run need no person either, by an interrupt in Claude Code and with none in Codex. For a harness qualified as attended, and for the attended steps of Codex: `up` names the steps that will need a person, `compaction_due`, `goal_paused` and `interrupted` are raised when they come due, and nothing is typed |
| Outstanding send | A person's typed submit still pending in core at pause is reported as draining. A daemon restart during one does not issue a second, at any age |
| Status forgery | A shell that prints an OSC 7501 record does not become ready, enrolled or a delivery target. For each qualified harness, tool output that contains such a record does not change the reported status |
| Environment | With a core started under another agent's environment, none of those values reaches a managed harness. `argv[0]` resolves to the same file on the core |
| Restricted worker | A real restricted worker of each qualified harness, on macOS and on Linux, with and without network access: it claims its own notice and acknowledges it through the worker socket, is refused another execution's notice, reaches no other daemon service, and cannot connect to the core socket. For Codex, network access means the proxy feature, and a launch whose sandbox has plain network access is refused |
| Recovery shells | After a core stop and start with several kept executions: all are marked lost, nothing is typed, one recovery plan is offered, and no shell is killed unless a person names it. A shell where a person worked and then cleared the screen survives `stop`, route close and `teardown` |
| Privacy grants | `runtime doctor` reports a repository root that core cannot read |

### Release sequencing

These steps are this document's own order. §19 maps them to the tasks and the stage of the v1 implementation plan.

0. **Attestation fix for the current release:** Add core-terminal resolution to `attest` (§6). It does not depend on this feature, because the gap exists today on any machine with `hc` installed.
1. **Identity and provider foundation:** Introduce typed execution records and provider boundaries; keep the legacy suite passing. Add core protocol fixtures and capability diagnostics.
2. **Core plus native app slice:** One managed agent, durable launch intent, registration by process, native placement, fresh identity/readiness, the notice channel with its worker socket and the input rules of §10, stop, and reconnect. The probes that decide how each harness is woken are recorded (N19 to N32); run the ones §18 still lists first, `keep` after adoption above all.
3. **Full route slice:** Coordinators, workers, approved-pair messages, the core inventory, services, checkpoint, watch, budgets, task/worktree launches, web/API controls, and crash recovery.
4. **Switch for new installations:** Pass the required gates, among them "Long run with no person" for at least one harness in each coordinator role (§10). Document compatible versions and each harness's level, migrate old-state decoding, write the installation default, update ADRs/generated docs and release notes. Existing installations keep their backend.
5. **Optional TUI view:** Ship independently when its adapter gates pass. It must not become a dependency of step 4.

A development configuration may select the new backend before step 4. That is rollout mechanics; the intended backend for new installations remains core. Do not advertise `0.1.19` as a certified production minimum solely because these fixture probes passed.

## 18. Remaining work before implementation can be called complete

The architecture is decided. These are concrete verification/dependency gates, not choices to defer to an implementer without criteria:

1. Release the launcher attestation fix (step 0). Its process-table part and the doctor check are written and tested on the local branch `fix/attest-core-terminal`, not yet committed or merged. Confirm it once with `hc run -- herdsman up` in a throwaway route before the release. Decide separately whether approval needs proof that a person is present, because a process that hides passes the check with or without core (§6, N13).
2. Run the owed probes. The harness probes of 2026-11-11 answered these, with one model for each harness (N19 to N32): the rest state of both; how each is woken, with which status and by which start path; what an interrupt and a compaction do to that; whether each compacts by itself; whether a paused Codex goal has a way out; the socket allowance in a normal Codex launch on macOS; the Codex sandbox with network access; and the keychain read from inside a core terminal, for Claude Code. Still owed:
   - `keep` set to false after adoption (§12). It needs the app.
   - Whether herdr-core and the app run on the Linux runner that the v1 plan uses (§19), and the socket allowance on Linux (§13).
   - The recovery answer by status: a turn that a person ended leaves a claim open, and the wait that names it must return it after the grace period (§10). N24 ordered recovery by hand.
   - Codex's queue while the session shows a dialog or its transcript view, and the queue in the Codex versions the adapter will allow (§10).
   - Codex with its hooks on: whether a turn-end hook runs after an interrupt, and whether one can hold or restore a wake. It was left out because it also runs the owner's own hooks.
   - The level at which Codex compacts when no limit is set, and what else pauses a Codex goal (§10).
   - How the Codex adapter learns its session id with provenance (§9).
   - That a stop leaves nothing of a Codex execution running (§12, N31).
   - The held-notice case with a real harness: an agent whose read is refused under a hold starts its wait again and stays quiet until resume (§10). The rule was checked in a model only.
   - Each row of the table in §9 again, more than once, for the harness versions and models of the release.
3. Send the upstream requests of §10: a submit guarded at every write by incarnation and state, `if` for `done`, a send id with query and cancel, a deadline on the wait for human quiet, and the time of the last human input. Ask the app for a confirmation dialog that agents cannot answer (§6).
4. Implement and qualify native harness identity/readiness/resume adapters. Startup-only probes do not establish complete support.
5. Complete the runtime-neutral caller/enrollment migration across all graph-discovered consumers, including the inventory.
6. Validate native app membership, closure, parking, restoration, and endpoint matching with actual managed routes.
7. Run the crash, concurrency, policy, and Linux suites; publish the tested compatibility matrix.
8. Write the ADR changes: a new ADR for the backend and its installation default, one for delivery by notice with no automated typing, one for the worker socket, ADR 0024 for the two waits of `up`, ADR 0025 for what attestation proves on core, and the threat-model rows.
9. Add the backend to the roadmap and the v1 implementation plan as §19 describes, and refresh the pinned copies that a route's contracts quote before any of its tasks is dispatched.

The revisions made these decisions, which the owner can overrule without reopening the rest:

- The default is recorded per installation, so existing installations keep the TUI (§5). The owner has since brought the backend into 1.0.0 scope; the alternative that remains is to change the constant before the tag.
- Under the default profile, other agents on the shared core are outside the route's inventory. Under the restricted profile they remain a fault (§6).
- The app-to-core binding is verified once per app home and core, not before every first dispatch (§7).
- On core `0.1.19` no automation types into a terminal. Agents are woken through a wait they hold, and a typed submit is a person's action (§10). This replaces the first revision's decision that notices and `if` were gate enough. The alternative is to hold unattended use until upstream guards the submit.
- The start line may be passed as the harness's initial prompt. That adds one argument to the user's launch command and needs an exception in the compatibility contract (§10). The alternative is a person's action at every start. The same row of the contract says that harness kinds come only from herdr's own list; on this backend a kind needs a Herdsman adapter, which the owner accepts with the backend (§19).
- Under the default profile an unclassified child is listed and is not a fault (§6). The stricter choice is a fault under every profile, which stops any member that opens a plain terminal through core.
- Recovery shells are never removed automatically (§12). A stopped route can leave shells behind until a person closes them.
- Stop asks for final reports by notice only. An agent that is in a turn and makes no call to the daemon before the deadline is stopped without a report, and stop says so (§10). The current release types its last call into a working harness. The alternative on core `0.1.19` is a person's typed last call.
- A final report is not asked of an execution whose visibility is `unverified` or `missing` (§10). The looser choice is to ask a parked agent too, on the ground that the request starts no work.
- A prelude gets the secrets granted to its launch, as it does today (§6). The stricter choice gives a prelude none, which breaks a launch line whose first command needs one.
- On core `0.1.19` `/goal resume` needs a person, and so does the compact command where a harness does not compact by itself. Core is selected for new installations. Harness qualification and attended steps are stated explicitly in the release notes; they do not change the selected backend (owner decision, 2026-11-13).
- Codex is woken through its own message queue, with one fixed line and no content (§10). The alternative is to treat Codex as an attended harness on core `0.1.19`, where a person types a line for every notice, because Codex has no other wake that survives the end of a turn.
- Pause does not interrupt a Codex turn; it holds new work and lets the turn end (§12). The alternative is to interrupt it as today, after which a person must type a line before that agent can go on.
- The wait is started before the work and names its claim (§10). An agent that works therefore holds a wait, and one execution holds one wait slot for its whole life. The alternative is the earlier order, where an interrupted agent holds no wait and a person brings it back.
- On core the restricted profile builds Codex's sandbox as a permissions profile and turns on Codex's `network_proxy` feature, which Codex `0.164.0` marks experimental (§13). The alternative is no restricted Codex worker on core until that feature is stable.
- The backend is roadmap pull request 18, built in stage 5b of the v1 plan with the tag `v0.11.0`. Release qualification remains a separate gate; an open gate does not silently change the selected backend (§5, §19).

The original bridge draft is useful as an optional-view starting point after correction. It is insufficient as the design of Herdsman's core backend because it conflates displayed agent state with execution identity, does not replace pane-based lifecycle control, and predates the native app adoption path verified here.

## 19. Place in the v1.0.0 roadmap

The owner added this backend to the v1.0.0 roadmap on 2026-11-10. It is pull request 18 of the roadmap and stage 5b of the v1 implementation plan, between the remaining product (stage 5) and the release (stage 6). The plan holds the task rows, their owned files and their gates. This section says how the design meets the rules that both documents already set, so that neither has to bend.

### How the design meets the v1 rules

| Rule or part of v1 | What it means for this backend |
|---|---|
| Contracts first | Every new surface is a proto message or service before it is code: `RuntimeService` and `NoticeService` in `proto/herdsman/daemon/v1/`, runtime references beside the pane fields in `core/v1`, and `Runtime` in `config/v1`. The `runtime` and `notice` verbs are thin clients of the SDK, `spec cli` and the descriptor goldens gain entries, the MCP tools follow from the descriptors, and the web reads runtime state through `@herdsman/sdk` |
| Import guard | `internal/runtime`, `internal/hc`, `internal/herdrapp` and `internal/notice` open sockets, signal processes or write state, so the tasks that create them add them to the server list. No verb file imports them |
| Dependencies | The core client is Herdsman's own code over newline-delimited JSON, and the app adapter runs the app's own command. No module is added, and the budget test does not change |
| Operations | The launch transaction of §8 is a set of steps of the `up` operation on the engine: intended before the effect, done after it is confirmed. The idempotency key of a spawn is the terminal's generated name with the operation's label. Reconciliation verifies identity, not existence: the process `(pid, start time)` and the registration. A recovery shell under the same name is "found but different", so the operation stops as a conflict and a person decides. A person's typed submit is a `never` step: an uncertain one stops with its choices and is not replayed. Notices are rows in the engine's outbox. The final-report request is one step of the stop operation for each execution, with its deadline in the journal, so a restart goes on with the same step |
| Launch spec | Prelude steps, their retry rules and their marker files are unchanged; the wrapper runs them in the core terminal once it is authenticated, with the variables granted to the launch. The wrapper runs the agent command where the TUI path uses `herdr agent start`. Harness kinds come from the adapter registry on this backend |
| Ownership ledger | An execution is an `exclusive` resource whose identity is `(pid, start time)`, with the core address beside it. Revalidation before every signal is the check before `terminal.kill`. A recovery shell and an unclassified child are `associated`, so nothing stops them without a person's yes (§12, §13) |
| Policy and profiles | `max_agents` counts members and agent children. Budgets count children through the parent chain. The restricted profile keeps the whole-core inventory rule, needs a dedicated core, and reaches the daemon only through the worker socket (§6, §13) |
| Configuration | `[runtime]` is a section, like every setting. It is strict, as `[policy]` and `[launch]` are, and it is a minor version of the config schema. All four formats come from the one message |
| Compatibility contract | Two new rows: the herdr-core coupling (one pinned protocol generation per release, every method and event Herdsman uses listed in one file, a capability check against core's schema, a fake core at the pinned generation and the previous one in CI) and the runtime references (stored, never derived from a pane or an address). Existing rows are kept: pane fields stay, defaults do not move for existing installations (§5), and identifiers are stored. Two points touch the launch-commands row, and both are the owner's to accept. The start line as an added argument needs an exception (§10). And a harness kind on this backend needs an adapter, which is Herdsman's code, so a new kind arrives with a Herdsman release, where the row says kinds come only from herdr's own list (§9). An adapter may refuse a command; it never rewrites one |
| Events and transports | The notice wait is a unary long poll on the RPC binding, bounded as the event wait is and with a budget of its own (§10). Execution loss, `needs_attention` with its reason (`compaction_due` and `goal_paused` among them), `not_waiting`, `claim_stuck`, a claim closed as `interrupted` or `abandoned`, the report outcomes of a stop, visibility changes and unresolved sends are event kinds on the stream, so automations and the web see them without a new channel |
| TUI forms and web | The `up` form gains a backend field, prefilled from the config and `doctor`. The route page shows process state, readiness, visibility and unresolved items as separate fields (§9), and its controls call the provider through the SDK |
| Traces | Launch steps, the issue, claim and handling of a notice, and each typed submit are spans of the route's trace |
| Acceptance | The gates of §17 are scripted cases under `acceptance/`, run on macOS and on the Linux runner. `acceptance/requirements.md` declares `herdr-core` and `herdr-app` as capabilities per platform. A gate whose capability a platform declares absent is skipped by name; one that is declared present and missing fails the run |
| Performance | The `up` budget case of the benchmark harness gains a core variant, with the A/A control and the result as a confidence interval |
| Documentation | The ADRs of §18, the threat-model rows of §16, the generated command and config pages, and the skill's references for the core path, updated in the same stage |
| Dogfooding | The tag `v0.11.0` goes through the promotion drill. The store migration is listed in `MIGRATIONS.md` with its fixture. The release route then runs its own coordinators on core where their harness is qualified for unattended use, with the TUI one config line away. By the probes that is Claude Code, and Codex while nothing interrupts it and it runs no goal (§9, §10). A coordinator whose harness has attended steps stays on the TUI, and a second, attended route exercises core. Rollback is the drill's recovery plus `backend = "herdr-tui"` (§5) |

### Tasks

The steps of §17 become these tasks. The plan's table is the authority for owned files and gates.

| Step of §17 | Task in the plan | Lands as |
|---|---|---|
| 0 | `attest-core-terminal` | Its own pull request, at any time; it is written on a local branch |
| 1 | `runtime-records`, with the queue tasks `hc-client` and `herdrapp-adapter` | Group `runtime`, and two library pull requests |
| 2 | `core-launch`, `notice-channel` | Group `runtime` |
| 3 and 4 | `core-surfaces` | Group `runtime`, last before the stage's docs |
| 5 | `tui-view` | Included in PR #121 as an optional attachment; native app remains required |

Stage 5b starts when stage 5 is merged. `core-launch` and `notice-channel` also waited on three probes that each decide a branch of the design. Two are recorded: how each harness is woken (N20, N29, N30) and the socket allowance in a normal launch (N27). `notice-channel` can be written against them. `core-launch` still waits for the third, `keep` after adoption, which needs the app. The other tasks do not depend on the probes and start from the plan's queue.

### What the roadmap and the plan change

- The roadmap gains pull request 18, a decision row, the two compatibility rows above, `[runtime]` among the strict sections, and the new packages in its architecture. Pull request 17, the release, still comes last.
- The plan gains stage 5b with its rows, the tag `v0.11.0`, and decisions for the stage, for the unchanged numbering, for the experiment fallback, for the rule that nothing is typed and for the steps that need a person. Stages 0 to 5 do not change. In stage 6, `acceptance-suite` also covers the gates of stage 5b, and it and `docs-final` start after that stage.
- Stage and pull-request numbers are not renumbered. Contracts quote rows by name and number, and a route's pinned copies must stay readable.

### Platforms

The roadmap supports macOS and Linux. The app has no prebuilt version for an Intel Mac and needs a Vulkan driver on Linux (§5), and neither product has been run on the plan's Linux runner. The harness probes ran on macOS on Apple silicon only. Until the Linux probes of §18 say otherwise, the backend is claimed for macOS on Apple silicon only. On another machine `runtime doctor` says why and names `backend = "herdr-tui"`, and the Linux acceptance run skips the core gates by name.
