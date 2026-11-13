# herdr-core backend: implementation and review evidence

Date: 2026-11-13 (Asia/Dhaka). Pull request: [#121](https://github.com/arafathusayn/herdsman/pull/121).

## Result

Herdr-core is the default for fresh installations and newly generated configuration files. Existing installations and routes keep their recorded backend. Main Herdr app visibility is mandatory. The TUI is an explicit legacy backend or an optional view of an existing core execution.

The implementation covers phases 0–7 of the backend plan. Release publication, Linux core/app qualification and repeated long-running model qualification are separate from implementation. The original plan's unchecked release gates must not be interpreted as passed by this review.

## P0 and P1 review

No P0 issue was demonstrated. The following P1 failures or required-path gaps were found and fixed. This is evidence from source review, adversarial fixtures and isolated runtime probes, not a claim that no other defect exists.

| Finding | Failure before the fix | Resolution and regression evidence |
| --- | --- | --- |
| Public task launch bypassed backend selection | `new --agent` constructed a local service with no runtime callbacks. A fresh installation reported core as default but launched through the legacy path. | `LedgerService.NewWorktree` uses the daemon-owned runtime service. `TestNewAgentUsesDefaultCoreThroughTheCLI` verifies the production CLI path, durable execution, native visibility and notice without a backend opt-in. A real restricted worker also passed this path. |
| Restricted launch could inherit broader permissions | A reused named Codex profile can merge with user configuration. An older wrapper could ignore a new sandbox descriptor. | Fresh random permission profile per launch, exact resolved-profile verification before registration, and wrapper protocol 2. Old wrappers and missing verifiers are refused. Real sandbox acceptance tests cover inherited grants, filesystem access, Unix sockets and direct TCP. |
| Task acknowledgements could lose continuation work | A hold or failure between acknowledgement and continuation issuance could leave no next task notice. | Acknowledgement and continuation are one transaction. Tests cover held work, maximum payloads and recovery. |
| Open claims could defeat stop priority or authorization changes | An ordinary open claim could hide a final report; repeated reads could return content without checking current gates. | Final reports preempt ordinary claims. Every read checks generation, endpoint, policy, visibility and deadline; acknowledgement remains available during a hold. |
| Persisted registration could lose identity evidence | Execution updates omitted creation time, launch guard and effective command/policy digests; stale writers could overwrite process evidence. | Account and in-memory records preserve and seal registration evidence. `TestExecutionSealsRegistrationEvidence` and launch transaction regressions verify it. |
| Standalone tasks could reconnect to the wrong endpoint | Restart and queue paths could resolve current configuration instead of the execution's original endpoint. | Executions persist the core socket and binary. Watch, queue and show resolve that stored endpoint and verify core/process identity. |
| Task command parsing changed arguments | Splitting on whitespace broke quoted arguments and dropped environment/prelude structure. | Task launches use the launch grammar; malformed or unsupported commands fail before the worktree is made. Quoting and prelude regressions cover the full task launch data. |
| Restarting a stopped task could reuse an obsolete identity | A stopped execution's intent, markers or old task notice could be reused. | A confirmed stopped-and-dead execution receives a fresh execution, attempt intent, step directory and notice. A still-live or uncertain process is not replaced. |
| Core resume could lose policy or approval requirements | Restricted sandbox data and manual pair approval did not survive every resume path, and core startup waits could not advance. | Resume carries the original sandbox and manual pairing mode. Pending pairs require the exact recorded checksum. Core resume advances its own startup state. |
| Codex lacked the specified native wake path | Notices required repeated manual reads despite the queue capability in the plan. | The pinned adapter uses a fixed queue line only after process, endpoint, version and notice-backed conversation evidence. Durable reservations prohibit automatic replay of uncertain writes. Queue tests and two real Codex queue rounds passed. |
| Restricted Codex arguments disappeared from process evidence | The shared `ps -E` parser treated `-c key=value` arguments as the beginning of an environment and dropped the following `--no-daemon`. The real queue path consequently refused valid workers. | Core now parses an environment-free process command column without that heuristic. A regression preserves all sandbox arguments; a real restricted worker completed two notices across the daemon's native queue wake. |
| Optional TUI view was absent | Core executions could not be mirrored with the planned ownership and recovery rules. | The attachment manager/helper use durable intents, immutable identities, bounded reconnects, leases and expiring display metadata. Real private-TUI tests cover lost replies, collisions, moves, close, explicit reattach, restart and helper death. |

An additional event-parser defect was fixed: global document events have no terminal field. Requiring one repeatedly disconnected the reconciler while the native app was active. Known terminal events still require a terminal; global and unknown event kinds preserve stream continuity.

## Implementation coverage

| Plan phase / consumer | Implementation | Evidence |
| --- | --- | --- |
| 0: launcher attestation | Core ancestry is inspected before pair approval; process names/status labels cannot grant agent authority. | Attestation, daemon caller and core pair-boundary tests. |
| 1a: protocol client | Capability negotiation, bounded frames, independent subscription epochs, snapshot/missed recovery and local-only identity. | `internal/hc` unit/fuzz seeds and live core event, remote-record and input-race gates. Unsupported protocol generations are refused. |
| 1b: native app | App-to-core binding challenge, placement readback, local membership and explicit-target focus. | Fake endpoint mismatch tests and real private native-app visibility/mismatch tests; both role screenshots inspected. |
| 2: records and selection | Installation migration, persisted route backend, strict runtime configuration and execution records. | Existing/new account, conflicting override, migration, config-format and SDK conformance tests. |
| 3: launch and identity | Wrapper handshake, sealed registration, readiness/visibility steps, process ancestry, inventory and recovery-shell refusal. | Core launch transaction, crash/reconnect, identity replacement, environment and privacy tests plus real core gates. |
| 4: delivery | Notice outbox, read/ack/wait, priority, holds, claim recovery, native queue and execution-scoped worker socket. | Notice/daemon regressions, real queue rounds and real restricted task notice read/ack. |
| 5: routes and task launches | `up`, task launch, pause, resume, stop and delete use the stored backend and execution identity. | Route/daemon lifecycle and public CLI tests; the task launch bypass found by this audit is fixed. |
| Pair messages, courier, cardwatch and alerts | Core channels and daemon-owned services use execution references; the file/pane implementation remains for TUI routes. | `core_channel_test.go`, `core_services_test.go`, `core_cardwatch_test.go` and forged-status tests. |
| Checkpoint and goal resume | `checkpoint/oncore` emits notices and uses adapter conversation evidence; core goal resume raises attention rather than typing. | Checkpoint core tests and `TestCoreResumeGoalRaisesAttentionAndTypesNothing`. |
| Status, watch, web and forms | Runtime SDK observations expose independent process/readiness/visibility states; `exec:` watch targets use runtime reads. Legacy status specs and pane endpoints retain their explicit TUI semantics. | CLI runtime/route-status tests, watch core tests, web unit tests and 301 browser checks. |
| Automation, MCP and traces | Runtime events reach automations, core pause goes through the daemon, descriptors generate MCP surfaces, traces omit prompt content. | Automation runtime, MCP descriptor/conformance and trace-span tests. |
| 6: documentation | Generated config/CLI references, SDK contracts, ADRs 0032–0035, threat model and release notes. | Generation/currentness checks; release operations remain separately identified below. |
| 7: optional TUI | Dedicated attachment panes display existing core processes; they are not identities or dispatch targets. | Real private-core/TUI lifecycle gates, including stale metadata expiry after a hard helper kill. |

The consumer audit used graph symbol discovery. Graph tracing returned an inconsistent “project not indexed” result, so direct call-site searches were used for `LiveIdentity`, `AgentGet`, `AgentPrompt`, `ReadPane` and pane lifecycle consumers. Remaining TUI calls belong to the legacy provider, explicit pane surfaces or branches selected only for TUI routes. The optional mirror never calls `agent.prompt`.

## Validation record

All live probes used private core sessions and app/TUI homes. They did not stop the user's servers or change the user's app layout.

| Check | Result |
| --- | --- |
| `make proto docs` | Passed; regenerated SDK, descriptor/CLI goldens and references. |
| `make check` | Passed across all modules; includes race tests, static analysis, SDK/conformance, web and vulnerability checks. |
| `make web-e2e` | Passed: 301 checks, zero failures. |
| `make live-core` | Passed on the settled code, including optional TUI attachment gates. |
| `make live-app` | Passed; implementer and orchestrator screenshots visually inspected. |
| Restricted sandbox acceptance | Passed with actual Codex 0.164.0, including broader inherited profile grants. |
| Real restricted worker through public `new --agent` | Passed: core default without opt-in; main-app placement verified; own notice read and acknowledged; core/control Unix sockets denied. |
| Real Codex native queue | Passed twice in one private interactive session, then through the actual daemon: a restricted worker acknowledged two notices across a queued continuation. Its transcript contains the fixed queue wake as a new user message. |
| `make bench-up` | Passed, 20 rounds against the fake core world: 95% CI 258.96–270.14 ms against a 2,000 ms budget; A/A control 95% CI −13.26–9.94 ms. |
| `make release-check` | Passed with the repository-pinned GoReleaser 2.19.0. |
| `make acceptance` | Core-launch gate passed. Whole release suite: 12 passed, 1 declared macOS skip, 6 failed because catalog targets are missing or stale; details below. |

The successful real-worker probe used an absolute path to the newly built Herdsman executable because the separately installed CLI was an older build without `notice`. The probe operator supplied the initial harness prompt explicitly; subsequent continuation came through the daemon's native queue. This does not claim an automatic initial start. The complete notice race suite also passed ten consecutive runs after the capacity fixture was changed to join canceled reconnects before checking the occupied-slot limit.

## Release qualification still open

The broad acceptance catalog already contained these failures before this review's changes:

- `clean-undo-ignored-data` and `clean-failed-snapshot` name nonexistent tests in `acceptance/recovery`; related tests live in `acceptance/clean` under different names.
- `merge-moved-target` and `merge-stale-review` name tests not yet present in `acceptance/landing`.
- `restricted-maintenance` names a nonexistent test; the related test is `TestTaskSurvivesSourceCleanup`.
- `drill-export-restore` names an `acceptance/drills` package that is not present.

These results do not satisfy the plan's whole-release promotion gate. They are reported rather than hidden by changing the gate list. The PR also does not claim Linux core/app support, an unattended initial harness start, automatic Codex goal/interrupt recovery, repeated long-run model qualification, an upstream guarded-kill capability, or a published release tag. Merging, tagging and sending upstream feature requests are separate release/communication actions.

The current supported combination remains macOS on Apple silicon, herdr-core 0.1.20 at protocol generation 1, the native Herdr app, and the harness versions in the release notes. Core remains the new-installation default regardless of these explicit qualification limits.
