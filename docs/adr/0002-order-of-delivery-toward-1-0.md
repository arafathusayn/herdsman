# 0002: Order of delivery toward 1.0

Status: accepted

## Context

Later work depends on earlier work, so the sequence has to be settled up front.

## Decision

Everything else waits on the daemon, which ships first; contracts come next. The 1.0.0 tag is cut only after the final item merges, and candidates tagged `-rc.N` may go out earlier.

## Consequences

People can try a candidate through the normal install channels while work continues.
