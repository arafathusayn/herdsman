# 0025: The launcher vouches for the agent pair

Status: accepted

## Context

Pausing for an approval stalls a launch a person just asked for.

## Decision

Because a person ran `up`, it signs off on its two agents and logs who, where and when. If it finds itself under an agent or harness, it holds back the approval and prints the command a person can run. `--pair manual` brings back the inspection step.

It holds back in the same way in a herdr-core terminal that was started with a command, such as `hc run -- herdsman up`. herdr-core starts such a command with no agent above it in the process table, whoever asked for it, so the command is no sign of a person. A confirmation typed there counts for no more, so `--pair manual` does not lift this. A shell that a person types in passes as before, in a herdr-core terminal as anywhere else.

## Consequences

Launches do not stall. An agent that runs `up` as an ordinary command, directly or through `hc run`, does not approve a pair.

The check stops that ordinary path and no more. It reads the process table only, which leaves three ways past it:

- A process started apart from its parents, on a terminal of its own, has no agent above it and passes. This needs no herdr-core.
- herdr-core is known by the name of its program, `hc` or `herdr-core`. A copy that runs under another name is not seen. The name is what herdr-core shows today, not a promise it makes, so `herdsman doctor` compares it with the holder processes a running herdr-core reports and fails when they differ. A new herdr-core release then cannot reopen the gap unseen.
- A person's typing looks the same as text that an agent sends into a shell through a terminal's API.

Closing these needs proof that a person is present, which the process table cannot give: a fact only the terminal's owner has, such as whether the last input came from a person, or a confirmation given outside any terminal an agent can reach.
