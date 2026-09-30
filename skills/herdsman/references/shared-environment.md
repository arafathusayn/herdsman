# Shared machine and test stack

All agents of a route run on one machine and usually on one test database container. Their gates compete for CPU, memory and database state, so a route must share these on purpose.

## Contracts name the environment exactly

- Before you write test database names into contracts, read the project's test-reset guard (for example a required "test" in the name) and the settings its suites need. Otherwise each implementer invents its own names and values.
- Give each agent its own databases, named in its contract or prompt. Never let one agent touch another's database.
- Probe a new harness before a route gives it database-backed tests: sandbox network and approval modes decide whether it can reach the container.

## Share the load

- Run one single-threaded compiler per agent at a time. Parallel type checks can hang the machine.
- Agents that run full gates on one container at the same time can exhaust its shared memory. Run one full gate at a time, or let the reviewer run only the touched packages while the integrator gates. Every brief says: wait and run again after a recovery, never restart containers.
- Database tests that skip for no clear reason can come from a full System V shared-memory table (orphaned segments of crashed embedded databases): check `ipcs -m`, and give the cleanup command to the user.
- Starting a container runtime can also start other projects' containers with a restart policy. Report them; do not stop them.

## Tests that prove the product

- A suite that skips environment validation everywhere hides boot failures: require one test that starts the real entry point with only the documented settings.
- Opt-in live tests may skip in the normal suites, but a route whose goal needs the live path must run it ([`contracts.md`](contracts.md), Live tests).

## Facts, not guesses

- Timestamps come from `date`; route-state lines come from [`scripts/route-log.sh`](../scripts/route-log.sh).
- Test logs written outside the worktree make a working agent look idle; check that folder before calling it hung.
