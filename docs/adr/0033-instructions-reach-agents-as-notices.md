# 0033: Instructions reach agents as notices

Status: accepted

## Context

herdr-core checks a program's state once, before it pastes text, and presses Enter about 200 ms later with no second check. A dialog that opens in between is confirmed by that Enter, whatever the text said.

## Decision

On herdr-core no automation presses Enter or sends keys in a terminal. An instruction is a notice that the daemon records for one execution and generation; the agent claims it with `herdsman notice read` and closes it with `herdsman notice ack`, an outcome and a processing record. Every gate is checked when the notice is issued and again when it is claimed, and a notice that cannot be claimed wakes no one. An agent is woken with nothing typed: Claude Code holds a background wait, and Codex receives a fixed line through its proven native conversation queue. Harness steps that require a person are reported explicitly. A typed submit is one person's explicit action on one execution. Stopping a route asks each agent for its report with one final-report notice that only the stop operation can issue.

## Consequences

Slash commands such as `/goal resume` need a person on core. A route reports each step that needs a person before it starts and as an event when it comes due. Automated typing returns only for a herdr-core version that checks the program at every write.

## Notes

The initial fixed start line remains an operator step after startup dialogs. Codex 0.164.0 requires `--no-daemon`; its process, version, endpoint and conversation provenance are reverified before each queued wake. A durable reservation precedes the queue call, and an uncertain result is never replayed. Holds are rechecked immediately before writing. Interruptions and paused goals remain attended. `keep` stays enabled, and recovery shells receive no agent authority.

Task acknowledgements that request continuation create it in the same transaction. Holds defer its claim instead of losing work. Final-report notices take precedence over an ordinary open claim, and repeated reads recheck authorization and report deadlines.

The card watch on core reads screens through the runtime and types nothing: it relays side-agent cards to the adjutant through the route's channel and raises alerts for what needs a person. Reading another agent's screen is the daemon's and a person's, never an MCP tool.
