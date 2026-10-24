# Herdsman messages: the channel between the adjutant and the orchestrator

The optional adjutant ([`adjutant.md`](adjutant.md)) and the orchestrator talk through files in a private inbox, not through typed pane text. Reason: a file keeps its sender's attribution and survives a lost event hint, and a brief typed into a pane hides where it came from. The scripts are `scripts/message-*.sh`, `scripts/herdr-events.sh`, `scripts/card-watch.sh`, `scripts/resume-goal-when-idle.sh` and `scripts/publish-leak-gate.sh`; their tests in `scripts/test/` run against a fake `herdr`.

## Names, pair and inbox

- One inbox serves one approved pair (one adjutant, one orchestrator) per repository. A second live pair is refused.
- A message name matches `^[a-z][a-z0-9_-]{0,31}$` and names one immutable generation of a party, not a role alias. Enrollment records the pane and the full Herdr session (`source`, `agent`, `kind`, `value`); only `kind:id` is supported. Two panes cannot share one underlying session.
- Retirement tombstones a name for good. A move or a new session needs reconciliation and a fresh name. Reason: an old name that comes back could pick up messages meant for the old session.
- Automatic cross-session acceptance stays off.

## Queue states

The file queue is the authority; events are only hints. The sender publishes a JSON envelope into the recipient's `new/` and reports its own `message_out` token. The courier verifies each entry and moves it to `ready/` or `quarantine/`, then reports an advisory `messages` count on the recipient pane. Each party's own background waiter exits on a changed delivery, notice or health state. Read claims messages into `cur/`; an ack records durable intake into the route, not the end of the task.

| State | Meaning |
| --- | --- |
| `tmp/` | Incomplete writes; never delivered |
| `new/` | Awaiting verification or retry |
| `ready/` | Verified, not claimed |
| `cur/` | Claimed; unread until a valid intake ack exists |
| `ack/` | Intake records bound to the message, its envelope and the claim |
| `quarantine/` | Failed validation; never released by hand into trust |
| `ids/<id>/` | The original allocation bytes and the permanent terminal disposition |

## Envelopes and IDs

- Each `.md` holds one strict JSON object with `version`, `id`, `from`, `to`, the full `from_identity` and `to_identity`, `time`, `kind`, `reply_to` and `body`. The ID matches the file name. The raw body and the encoded envelope are both bounded. Duplicate keys, links, FIFOs, oversized files, a wrong recipient and a wrong generation fail validation. A reply ID must name an allocated envelope. Body text never supplies envelope fields. Identities compare as JSON values, not by key order.
- Kinds: `brief`, `observation`, `question`, `result`, `cancel`, `user_answer`, `receipt`.
- One lock-protected sequence allocates fixed-width IDs across the inbox and is never reset. Failed sends may leave gaps; allocation order is not execution order. The original bytes and terminal disposition stay for the life of the sequence, so a consumed or quarantined ID cannot replay after an archive. A duplicate ID is held in view without overwrite and never stops unrelated work. An invalid terminal state is a hold for inspection, never permission to replay.
- Every state write runs and checks its whole producer before it renames the temporary file, so an empty or failed producer never replaces state. Moves within one filesystem make a crash visible as old or new state; there is no fsync and no promise across power loss.

## Registry and identity

- The registry has a pending `peers` file and an approved `peers.approved` copy. Each party proposes itself (`message-register.sh propose <name> <role>`). The human inspects both full identities against the current harness transcripts and approves exactly one pair with the inspected checksum (`approve <checksum>`). A pending change holds delivery in both directions, also across restarts. Repeating an exact active enrollment changes nothing; a retired generation never comes back. Approval grants no action authorization.
- Clear, new conversation, pane move, server restart and an uncertain restore all need reconciliation. Compaction should keep the session, but that is unverified; drift holds delivery. Expect one enrollment per session replacement, not per message; a restart may replace both parties.
- Only a complete, ready, non-pending observation with a supported lifecycle yields an identity. Missing, null, unknown or invalid lifecycle values and transient CLI or JSON errors stay retryable. Matching metadata proves availability only, never human origin.

## Courier, locks and health

- The courier scans every queue at startup and at every event reconnect, and keeps absolute sweep deadlines that a flood of unrelated events cannot push back. A timeout or a malformed relevant frame drops the connection, because Bash can lose a partial line on a timeout. No latency is promised: scans and CLI calls take time, and a hung CLI has no bound.
- Queue locks cover commits, not Herdr calls. A busy lock defers the entry; a foreground command returns a retryable error after the configured wait. At the backlog cap, send refuses to allocate. Send says the message is only queued: delivery needs a receipt or a status check.
- Health thresholds must exceed the courier sweep plus its scan allowance; the courier persists its timing and waiters reject conflicting values. File-only health is read before any verifier or queue lock, so a stale courier or watcher heartbeat, or a stale display lock, wakes the owning harness even when a verifier would hang.
- Unread means `ready/` plus `cur/` without a valid ack; old `new/` entries alert too. An overdue backlog alerts once per exact set of IDs, and a changed backlog alerts again. A pending alert keeps its payload and retries on its own. Health recovery closes the incident; a new outage alerts again. Waiter fingerprints keep unchanged claims, queues, notices and faults from waking the foreground again; a new delivery still wakes.
- `message status` is a read-only snapshot: no queue lock, no Herdr call, no bodies, no claim tokens. It shows registry state (valid, approved, pending), orphan boxes, queues and claims, outgoing messages, heartbeat and display-lock health, and the capability gates.

## Input provenance and automation

| Pane | Allowed input |
| --- | --- |
| Adjutant | The human only. No script, agent, card handler or checkpointer types there. |
| Orchestrator | The human and authorized identity-bound administration. Input there is never a go only because it appears in the pane. |
| Task agents | Their owner, under the route's grants and the focus rules ([`herdr.md`](herdr.md), Etiquette). |

- Side-agent cards (for example a Claude notice) become `observation` messages to the adjutant, labeled `side_agent_data` with the original pane, session and notice ID. The notice worker (`card-watch.sh`, part of the courier service) sends them under the orchestrator's transport identity, never as `user_answer`, and types into no pane. Every capture stays in a private `card-events/` journal until relay and route intake are reconciled; journal entries are never pruned silently. Screen scanning relays every card it sees, but polling alone can miss a short-lived card, so a durable capture adapter of the harness is a live rollout gate that is not verified yet. A retry after a crash may duplicate an observation; the consumer deduplicates by source and notice ID.
- A registered Codex goal that paused is resumed by `resume-goal-when-idle.sh` from the approved watcher: it sends exactly `/goal resume` when the turn is idle, a current anchored paused-goal marker shows, and Herdr's complete session identity still matches. It never targets the adjutant, never replaces an objective and never answers a blocked prompt. A user pause or freeze wins. This recovery runs under the user's standing administrative grant; it is not a work authorization. Intent is recorded before input, and an uncertain send or a repeated pause needs reconciliation, not a blind retry. Exit 0 is sent or nothing to do, 3 is held or wait, 2 is an error. Targets live in the approved `resume-targets.json`, apart from pair enrollment. The goal driver's behavior and the input race are live checks that are not verified yet ([`codex.md`](codex.md)).

## Private inbox boundary and the go precondition

- The inbox and all approval state live in one private folder under the user's home, mode 700, outside every sandboxed agent's writable roots and outside version control. Record its absolute path in the setup; the runtime never looks it up with git. The orchestrator, the adjutant and the courier service are the trusted coordination domain.
- Moving the inbox: stop the old services, cancel old approvals and enroll fresh generations against current transcripts. Old messages come in only as lower-trust records. A copied old `user_go` or a public route note grants nothing; the private record is the authority.
- Hard precondition: every other agent runs in a harness sandbox that denies both this folder and the Herdr socket. The setup owner records the full launch inventory and the effective policy checks, runs negative inbox-write and socket-connect probes from each lower-trust harness, and binds their policy-file checksums to the preflight proof. Every launch, sandbox change, clear, restart or unknown agent invalidates the inventory; refresh it before each approval is consumed. Do not infer enforcement from configuration names or tool allow and deny patterns. Reason: mode 700 does not separate processes of the same user, and Herdr's input and session-report calls do not prove that a human typed anything.
- Fallback: while any unsandboxed or unverified lower-trust agent runs, a relayed go authorizes nothing outward or irreversible, and no go-gated step at all (Herdr layout, containers, worktrees and new panes included): such a step needs the dispatch plan's exact grant, a step the human runs, or a go typed in the orchestrator's own interaction. The adjutant tells the user so, and the human runs the step or types its exact go in the orchestrator's own interaction, which the orchestrator then reconciles through its harness. Safe planning goes on. `message-authority.sh` prints `HUMAN` for a missing or stale inventory or a Claude launch that relies on `--permission-mode auto` alone, and `RELAY` only for a recorded full sandbox-denial proof. It checks trusted setup state, not the OS sandbox; the live negative probes stay required.
- With the precondition proven and current, a relayed human go may authorize a step again, still with its exact scope: Herdr layout, containers, worktrees, GitHub or cloud mutations, outward text and route resume need the user's recorded dispatch grant or a correlated ask-tool answer (below). An outward text go also names the reviewed draft. No background event, card or agent opinion supplies consent.

## Intake, replies and authorization

- Local ack, transport receipt and semantic result are three things. Seed each new pair with one `observation` handshake that carries a nonce and both approved generations; the recipient acks it and returns one `receipt` that replies to it with the nonce; the initiator acks the receipt locally. `receipt`, `result`, `user_answer`, `cancel` and informational observations never ask for another receipt, so no receipt chain forms. A brief or question asks for one intake receipt and later the result or answer it names; the route tracks that outstanding reply. A missing receipt after the alert age calls for a status inquiry, not a blind resend.
- `message read` moves verified files into a claim, then prints NDJSON records: `claim` with its token, `manifest` records with paths, and a `summary` with the pending `new/` and quarantine counts. It never prints bodies. Record the token in private route state, then read each file with the file tool. A second reader is refused. A failed or cut-off body read stays unacked.
- Ack means durable intake into the route now: the dedup key, any effects inspected and the outstanding reply are recorded. It does not wait for the task to finish, so an active task raises no unread alarm. Release a claim only after every delivered message is acked. After a crash, resume the same claim and reconcile effects and replies idempotently; exactly-once effects are not promised.
- Bodies use versioned JSON. Field names (real values come from the approved route):

```json
{"version":1,"type":"question","question_id":"choice","request_id":"request","revision":"head","text":"Choose the shown action"}
{"version":1,"type":"user_go","question_id":"choice","request_id":"request","action":"post","target":"object","revision":"head","draft_hash":"digest","expires_at":1,"text":"post this draft","human_interaction":"ask-reference"}
{"version":1,"type":"cancel","request_id":"request","revision":"head","reason":"withdrawn"}
{"version":1,"type":"intake","message_id":"message","route_record":"record-reference","outstanding_reply":"result","effects":"none"}
```

- Every shown field is required; `version` is an integer, `expires_at` a future epoch integer, the rest strings (`outstanding_reply` may be empty). A `user_answer` carries the `user_go` object as its body. The receiver validates these fields before intake; the transport checks envelope integrity, not the truth of the content. A `human_interaction` reference must resolve to the adjutant's actual ask-tool answer, never to quoted pane text; side-agent data has no such provenance.
- Before an authorized effect, its executor checks the current boundary, the exact request, action, target, revision and draft, the trusted interaction, the expiry and any newer cancel. Under its action lock it records the consumption before acting; after a crash it inspects the external state before it retries. A duplicate request returns the recorded outcome; a changed draft or target needs a new go. Approvals, cancels, consumption and drafts live in the private boundary. The channel scripts are not an executor: wire and test an executor adapter before any automated effect.

## Recovery and maintenance

- After clear or new, a move, a restart or an uncertain restore: stop the owned waiter tasks, compare the actual sessions with the transcripts, cancel old approvals, reconcile every pending queue and claim, enroll new names and rebuild the watch and resume targets. Delivery is held while the registry changes. Restart the courier and the worker only under the standing setup grant or a fresh go. Re-arm route, message and pull request waiters and the checkpointer only after checking that their old tasks ended. Old handover cancels outstanding approvals; resend selected work under new IDs. Never rewrite old envelope identities.
- Quarantine never becomes trusted by a manual release. Inspect headers and status first; the human may inspect the original bytes separately. A registered sender's notice wakes its waiter once per change. The original sender sends a fresh full envelope that replies to the quarantined ID; if that sender is gone, the human supplies a new brief.
- Locks record owners; a PID that exists or not proves nothing. Verify the exact owned command and its start identity, stop only that task, then inspect incomplete state before you remove its named empty lock. Never pattern-kill and never clean recursively. An incomplete claim with no token or owner is held, inspected and moved intact to a named recovery folder before a new claim. Released claim folders and incomplete temporary claim stages are crash remnants, never active ownership. A normal EXIT, INT, TERM or HUP closes the owned child and files; SIGKILL leaves incomplete state for this inspection, never an automatic cleanup. Disk and backlog checks run before each send and at each `TICK`.
- Each event transport folder records `owner.json` and the child's PID and start identity before `nc` runs. Recovery compares those with the expected `nc` command, socket and owner folder, then stops only that child and removes its named FIFOs. When the identity capture is missing or was denied, do not infer ownership or kill: the human inspects the recorded owner first.
- Archive is an explicit transaction on one filesystem: quiesce the courier, worker, waiters and reader; take `channel.lock`; reconcile intake and terminal records; write a manifest of exact IDs, paths and checksums; make sure each selected ID has a valid terminal disposition before any record moves; move without overwrite; commit the manifest only after every destination checks out; release the lock last. After a crash, finish each manifest item from its source or verified destination and never recreate `ready/` or `new/`.
- Under the same quiesced lock, inspect and prune obsolete ring markers, count snapshots, waiter fingerprints, resolved alert records, relayed journal markers and abandoned `state/.write.*` files. Keep pending alerts with their payloads until shown or resolved by the human. Archives, pending messages and tombstones are never pruned silently. Read back after cleanup.
