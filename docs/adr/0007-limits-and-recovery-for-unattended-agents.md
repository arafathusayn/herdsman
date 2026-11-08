# 0007: Limits and recovery for unattended agents

Status: accepted

## Context

Agents left alone need bounds and a way back.

## Decision

The daemon journals operations so a person can reverse a destructive command. Spending and time limits live in policy, and each says how strictly it is enforced. Agents can drive herdsman over MCP, automations fire on events only after approval, runs emit traces that allow harnesses to be compared, and the security model is written down and enforced.

## Consequences

An agent working overnight can misstep only within bounds, and its mistakes can be walked back.
