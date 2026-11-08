# 0025: The launcher vouches for the agent pair

Status: accepted

## Context

Pausing for an approval stalls a launch a person just asked for.

## Decision

Because a person ran `up`, it signs off on its two agents and logs who, where and when. If it finds itself under an agent or harness, it holds back the approval and prints the command a person can run. `--pair manual` brings back the inspection step.

## Consequences

Launches do not stall, and an agent cannot approve a pair by running `up`.
