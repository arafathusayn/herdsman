# 0033: Instructions reach agents as notices

Status: accepted

## Context

herdr-core checks a program's state once, before it pastes text, and presses Enter about 200 ms later with no second check. A dialog that opens in between is confirmed by that Enter, whatever the text said.

## Decision

On herdr-core no automation presses Enter or sends keys in a terminal. An instruction is a notice that the daemon records for one execution and generation; the agent claims it with `herdsman notice read` and closes it with `herdsman notice ack`, an outcome and a processing record. Every gate is checked when the notice is issued and again when it is claimed, and a notice that cannot be claimed wakes no one. An agent is woken with nothing typed, by a wait it holds; a harness that cannot be woken that way is qualified for attended use only. A typed submit is one person's explicit action on one execution. Stopping a route asks each agent for its report with one final-report notice that only the stop operation can issue.

## Consequences

Slash commands such as `/goal resume` need a person on core. A route reports each step that needs a person before it starts and as an event when it comes due. Automated typing returns only for a herdr-core version that checks the program at every write.
