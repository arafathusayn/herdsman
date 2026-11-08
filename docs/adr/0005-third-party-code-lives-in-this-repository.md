# 0005: Third-party code lives in this repository

Status: accepted

## Context

Every outside package is a future maintenance burden and a supply-chain risk.

## Decision

Neither building nor running reaches outside the checkout. For a narrow format with a settled specification we write our own implementation. Where code is big or handles security, we keep a copy that records its origin, and we monitor that copy for vulnerabilities.

## Consequences

Offline builds work, and taking an upstream security fix is a deliberate step.
