# UI responsiveness probe

Run the production bundle against the same mock load before and after a change:

```sh
make web
cd web/e2e
bun performance.ts > /tmp/herdsman-performance.json
```

The runner uses headless Chrome with fourfold CPU slowdown (`CPU=1` disables
it), a 1300×800 viewport, 40 archived routes containing 16,000 log lines, and
400 terminal lines with ten colored runs each. It records click handlers,
two-frame completion, composer input, a 30-event activity burst, long tasks,
sampled CPU hotspots and uncaught errors. The burst measurement includes
waiting for its last visible entry and navigating back to the checklist.

Measured on macOS arm64, comparing the initial migration at `ad48eb20` with
the optimized implementation using the same runner and fixture:

| Work                                              |   Before |  After |
| ------------------------------------------------- | -------: | -----: |
| Tab handler, median of 12                         |  34.5 ms | 2.6 ms |
| Theme handler, median of 12                       |  32.2 ms | 0.8 ms |
| Composer input handler, median of 23              |  57.6 ms | 0.7 ms |
| Terminal DOM mutations while typing 23 characters |  184,046 |      0 |
| Activity burst through visible completion         | 1,167 ms | 111 ms |
| Longest observed task in the complete probe       | 1,019 ms | 173 ms |

These are diagnostic samples, not promises for every device or a comparison
with main before the state migration. Initial loading of the deliberately
large snapshot still produces long tasks; there is no claim of zero jank.
Browser startup and initial load vary substantially, so they are reported in
the raw output but are not used to claim an improvement.

The fixes keep state in Reactre actors. Application domains have independent
actors, preserving the decoded server projection across navigation, activity
and theme changes. Terminal controls, output text and palette also have
independent actors. Unchanged palettes keep their identity during polling.
ANSI output uses one reactive effect to reconcile native text/span nodes;
the node handles are resources, and the rendered values derive from actors.
This avoids thousands of reactive owners when closing a populated terminal.

`make web-e2e` includes the same large fixture with deterministic checks for
zero output mutations during typing, retained spans across output refreshes,
preserved drafts, completed activity bursts and subsequent navigation. Unit
tests check server identity and reader counts across unrelated updates,
atomic multi-domain updates, ANSI escaping, colors, type changes and removal.
CI deliberately checks work and correctness rather than wall-clock thresholds.
