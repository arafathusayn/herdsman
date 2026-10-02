# Shared machine and test stack

All agents of a route run on one machine and usually on one test database container. Their gates compete for CPU, memory and database state, so a route must share these on purpose.

## Contracts name the environment exactly

- Before you write test database names into contracts, read the project's test-reset guard (for example a required "test" in the name) and the settings its suites need. Otherwise each implementer invents its own names and values.
- Give each agent its own databases, named in its contract or prompt. Never let one agent touch another's database.
- Probe a new harness before a route gives it database-backed tests: sandbox network and approval modes decide whether it can reach the container.

## Share the load

- Heavy checks (type check, lint, tests, formatter, build) run one at a time on the whole machine, not one per agent, single-threaded where the tool allows, under one machine-wide lock (`lockf -k -t 5400 <lock file> <command>` on macOS). Reason: parallel checks from several agents can hang the user's machine. Write the exact single-thread forms into the shared rules and the reviewer contract (one test worker and no file parallelism, ESLint `--concurrency=off`, turbo `--concurrency=1`, a native compiler's own single-thread flags: read its `--help` first). Forbid ad hoc formatters when the repository has no formatter script, and forbid handing checks to subagents.
- Put a wrapper in the route folder, `run-check.sh <command>`, and name it in every contract. It takes the lock, then, inside the lock, waits while the 1-minute load average is at or over a limit or free memory is low, gives up with a distinct exit code after a long wait, and then `exec`s the command. Wait inside the lock: a reading taken before the lock is out of date by the time the lock is free.
- Set the load limit from the machine's idle load, read before the route starts. Operating-system background work (media indexing, the window server, a container runtime's virtual machine) can hold the load over a fixed limit while no check runs, and every gate then waits for nothing. Every prompt caps that wait: a check that waits on the load gate for more than about ten minutes is cancelled and reported as a gap (implementer) or under NOT CHECKED (reviewer). A re-review reuses an earlier result for packages and files that the new commits do not touch.
- A load gate must work inside every harness's sandbox. The Codex sandbox denies `sysctl` and `memory_pressure`, so a wrapper that waits on an empty reading hangs forever and the reviewer runs no gates. Run a publisher outside the sandbox (a background shell of the orchestrator) that writes the reading to a file under /private/tmp every 15 s and stops by itself after a set number of hours. The wrapper uses that file while it is fresh; with no reading at all, it runs under the lock alone and prints that. Write the publisher's stop time in the route file and restart it before the next job: after it stops, sandboxed agents run with no load gate. Test the wrapper with a stub PATH that denies the probes before any agent gets it.
- Agents that run full gates on one container at the same time can exhaust its shared memory. Run one full gate at a time, or let the reviewer run only the touched packages while the integrator gates. Every brief says: wait and run again after a recovery, never restart containers. The lock prevents parallel runs but not this limit: one full gate can still crash a container with a small shared-memory size that holds thousands of leftover test databases. Suites drop the databases they create; a cleanup or a bigger shared-memory size means a container restart, which is the user's go.
- Database tests that skip for no clear reason can come from a full System V shared-memory table (orphaned segments of crashed embedded databases): check `ipcs -m`, and give the cleanup command to the user.
- Starting a container runtime can also start other projects' containers with a restart policy. Report them; do not stop them.
- Every contract says: when your browser tests end, close your pages, stop the test browser if no other agent is in a browser test, stop your local servers, and list what you closed in the report. On each wake, look for test-browser processes of finished agents and ask their owners to close them. Reason: idle browsers and servers load the shared machine and the user's desktop.

## Tests that prove the product

- A suite that skips environment validation everywhere hides boot failures: require one test that starts the real entry point with only the documented settings.
- A test harness must not set up anything that the real job does not (a trusted-directory setting, an exported variable, a loaded `.env` file). Such a setup masks exactly the failure the job will hit. Reproduce the job's conditions instead: its user, the owner of its workspace, its empty environment.
- The local gate is not the CI gate: CI usually has no `.env` files and keeps the runner's default per-test and per-hook timeouts. Tests that start child processes pass locally only because the parent loaded a `.env` file. Before a push, run the touched suites again with a clean environment (`env -i PATH="$PATH" HOME="$HOME"` plus the runner's flag that skips `.env` files).
- Opt-in live tests may skip in the normal suites, but a route whose goal needs the live path must run it ([`contracts.md`](contracts.md), Live tests).

## Facts, not guesses

- Timestamps come from `date`; route-state lines come from [`scripts/route-log.sh`](../scripts/route-log.sh).
- Test logs written outside the worktree make a working agent look idle; check that folder before calling it hung.
- Run the gates on the base before the route starts. Keep a short list of known flaky tests and failures that already exist on the default branch in the shared rules, with the command that reruns them, so that agents report them as unrelated instead of chasing them. Later, before you call a failure new, run it on the base. A failure from a stale build cache needs a forced rerun (turbo `--force`).
- Compare the local toolchain versions with the versions CI pins, at launch. A lock file is changed only by the pinned package manager, never by hand and never by another version; lock-file drift in a report goes to the user as a question ("upgrade the pins?").
- When an implementer's test results and the reviewer's differ, look for an environment difference first (exported variables, a loaded `.env` file, a different database). The accept decision rests on the reviewer's run.
- A gate that exits 0 without running anything is a false green: a flag in the wrong position can print the tool's usage text and exit 0, a misspelled config key is ignored, tests can skip without a word, and a pipe into `tail` reports `tail`'s exit code. A report's test lines name what ran and what skipped (files, counts) and the command's own exit code; a line without them is a gap.
- A report that finishes much faster than the work should take is checked in the agent's own session transcript (the commands and their output) before you call it real or fake. Build caches can make full gates fast.
