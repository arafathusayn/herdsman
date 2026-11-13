# Reactre core + built-in signals

One benchmark application using `@reactre/core` and its built-in `signal`.
Source comes from Herdsman's Reactre submodule at
`f0cb6cb9f7d9c9a96c25fb25ece2d54adb702c27`, the same revision as
`../reactre-state`. No `@reactre/state` runtime or machine is used here.

Each fresh driver snapshot is written to the signal. Both Reactre entries
import the exact same [view components and keyed projections](../../shared/reactre/)
and use the same upstream core compiler. They render identical tabs, retain
equal keyed rows, dispose replaced tabs and invoke card selection handlers.
The difference is ownership: this entry uses a built-in signal; the combined
entry sends snapshots through a compiled state actor and its subscription
bridge. This is a benchmark-only alternative; Herdsman's application state
continues to belong to `@reactre/state`.

The shared source adapter resolves vendored imports during whole-program
analysis as well as bundling. The [mounting and cleanup profile](../../PROFILING.md)
records the compiler fallback fix, direct scalar text holes and deferred
projection indexes used by both entries.

No whole-snapshot comparisons or operation-aware shortcuts are used. Tests
exercise both compiled factories with changed non-key fields, optional bodies,
event shapes, selection and disposal. The unchanged browser runner applies
its canonical DOM/card gate, workloads, throttling and A/A control.

From `bench/web/herdsman-ui-cases`, after installing the existing pinned
benchmark dependencies:

```sh
bun build.ts
bun run.ts --iterations 16
```

Sixteen iterations rotate each of the 16 entries through every timing
position. The benchmark README reports measured ranks and the control gap;
first or second place is an expectation to test, not an assigned rank.
