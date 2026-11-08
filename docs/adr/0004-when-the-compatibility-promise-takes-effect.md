# 0004: When the compatibility promise takes effect

Status: accepted

## Context

Early interfaces were experimental, and freezing them would leave two APIs for good.

## Decision

The promise applies in full from 1.0.0. Interfaces carried into that release count from the 0.x version that introduced them. The daemon's early socket routes and the web API are exceptions: they get replaced before the tag, as SemVer permits below 1.0.

## Consequences

Nobody has to maintain a legacy API beside its replacement.
