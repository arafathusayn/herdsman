---
id: worktree-per-task
kind: tip
title: Give every agent its own git worktree
tags: [agents, git, worktrees]
since: 1.0.0
---
Agents that share a checkout overwrite each other's changes. One worktree and
one branch per task keeps their work apart until it is reviewed.
