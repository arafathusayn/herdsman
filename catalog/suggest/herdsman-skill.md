---
id: herdsman-skill
kind: skill
title: The herdsman skill for orchestrating agents
tags: [agents, orchestration, review]
repo: https://github.com/arafathusayn/herdsman
applies_to:
  harnesses: [claude]
install:
  skill_repo: https://github.com/arafathusayn/herdsman
  touches: [~/.claude/skills/herdsman]
since: 1.0.0
---
Run an orchestrator, implementers and reviewers in one loop. The skill drives
the agents through Herdr and keeps each task on its own branch until its
review passes.
