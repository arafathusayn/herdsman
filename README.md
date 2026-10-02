# herdsman

A coding-agent skill that runs a team of coding agents inside [herdr](https://github.com/herdrdev/herdr): an orchestrator, implementers, an integrator and reviewers. The loop of fixes and reviews continues until each task passes.

## Agent types

- **Orchestrator:** the agent session that the user talks to. It runs the interview, writes the contracts, launches and watches the other agents, forwards review findings and records the results. It does not write application code and does not push.
- **Implementer:** one agent per task. It writes the code and the tests for its task on its own branch.
- **Integrator:** when several tasks land as one branch or one pull request, one agent combines the accepted task branches, fixes what breaks between them, and is the only agent that pushes and answers review threads.
- **Reviewer:** reviews each task branch and, before a push, the combined result. It reports only P0 and P1 findings and changes nothing.

Each agent can use a different harness and a different model. Any coding agent that herdr can start and recognize in a pane can take any agent type.

## Features

- **Parallel implementers:** one implementer agent per task, each in its own herdr pane, optionally in its own git worktree and branch.
- **One pull request or many:** each implementer publishes its own pull request, or an integrator combines the tasks into one branch and publishes it.
- **Strict review:** reviewer agents, usually on a stronger model than the implementers, report only P0 and P1 findings. A task gets at most three fix rounds; then the user decides. In an integration route a final review of the combined head runs before the push.
- **Event-driven waits:** a background waiter wakes the orchestrator on a new report, a new review, a blocked agent or a stalled agent.
- **Health checks:** on every wake, a status script shows each agent's state, model, background jobs with their ages, recent file writes, and the machine's load and free memory. It flags jobs that run too long, trees with no writes, and a loaded machine. Between wakes the waiter runs the same check every ten minutes and wakes the orchestrator only for a new flag.
- **One heavy check at a time:** agents run type checks, lint, tests and builds through `run-check.sh`: a machine-wide lock with a load gate inside it, at a lower CPU priority, so checks queue instead of starting together.
- **Checkpointer:** a pane next to the orchestrator asks the orchestrator to save its memory every 15 minutes, and to compact its context when the context passes a token limit.
- **Context hygiene:** each agent is compacted or cleared as soon as it finishes.
- **No-commit routes:** when commits are not allowed, reviews use tree snapshots from a temporary git index.

## How it works

1. **Interview:** the orchestrator asks once for the tasks, branches, harnesses, models, integration (one pull request per task or one combined branch), merge policy and test setup. Then it shows a dispatch plan and waits for the user's go.
2. **Contracts:** the orchestrator writes a route folder with shared rules, one contract per task, an integrator contract when tasks are combined, and a reviewer contract.
3. **Launch:** the orchestrator creates the herdr tabs and panes, starts the agents, and sends each implementer a full brief. A brief has the goal, numbered steps with a check for each step, a leave-alone list, time limits and the report format.
4. **Wait:** the waiter script exits on the first event. The orchestrator handles the event, runs the health check, and starts the waiter again.
5. **Review:** each finished task goes to a reviewer. A `fix` verdict goes back to the implementer as a fix round. A `pass` verdict closes the task. In an integration route, the integrator then combines the accepted branches in phases, a final review checks the combined head, and only a passing final review lets the integrator push.
6. **Close:** the orchestrator records the results and writes the lessons into memory. General lessons become rules in the skill's concept files ([`references/flow-and-time.md`](skills/herdsman/references/flow-and-time.md), [`context-hygiene.md`](skills/herdsman/references/context-hygiene.md), [`ownership-and-integration.md`](skills/herdsman/references/ownership-and-integration.md), [`review-discipline.md`](skills/herdsman/references/review-discipline.md), [`shared-environment.md`](skills/herdsman/references/shared-environment.md), [`checkpointer.md`](skills/herdsman/references/checkpointer.md)) or in its tool notes.

The orchestrator does not write application code and does not push. The implementers push their own pull requests, or, in an integration route, only the integrator pushes.

## Install

1. Install [herdr](https://github.com/herdrdev/herdr).
2. Install the coding-agent harnesses you want for the orchestrator, the implementers, the integrator and the reviewers.
3. Install the skill for the orchestrator's harness with the [GitHub CLI](https://cli.github.com/manual/gh_skill_install):

   ```
   gh skill install arafathusayn/herdsman herdsman --agent <agent> --scope user
   ```

   `<agent>` is the orchestrator's harness, as `gh skill install --help` names it. Add `--pin <tag or commit>` to pin a version. Use `gh skill update` to update later.

   To edit the skill while you use it, clone the repository and link its skill folder into the harness's skills folder instead:

   ```
   git clone https://github.com/arafathusayn/herdsman.git
   ln -s "$PWD/herdsman/skills/herdsman" <harness skills folder>/herdsman
   ```

4. Start the orchestrator inside a herdr pane.

## Invoke

In the orchestrator session, inside herdr:

```
/herdsman [subcommand] [options]
```

| Subcommand | What it does |
| --- | --- |
| none, or `run <route request>` | Runs the full route. The request names the tasks, harnesses, models and rules. |
| `checkpoint [start\|now\|probe\|status\|stop]` | Manages the checkpointer. The default action is `start`. |
| `status` | Shows the health of every agent in the current route. Changes nothing. |
| `help` | Shows the subcommands, options and examples. |

Options for `checkpoint`:

| Option | Default | Meaning |
| --- | --- | --- |
| `--pane <id>` | this session's pane | The orchestrator pane to watch |
| `--every <minutes>` | 15 | Time between checkpoints (`start` only) |
| `--limit <tokens>` | 250k | Compact at or over this context size |
| `--prompt "<text>"` | the script's default | The memory checkpoint prompt |
| `--compact "<command>"` | `/compact` | The harness's compact command |
| `--side right\|down` | right | Where the new pane goes (`start` only) |

Examples:

```
/herdsman checkpoint start --every 10 --limit 300k
/herdsman checkpoint now
/herdsman status
/herdsman run "tasks: issues 12 and 14; implementers: <harness> <model>; reviewer: <harness> <model>"
/herdsman run "tasks: four parts of one change, one pull request; implementers: <harness> <model>; integrator: the first implementer that is free; reviewers: two <harness> <model>"
```

The skill stops when `HERDR_ENV` is not `1`.

## Requirements

- `/bin/bash` 3.2 or later. The scripts use no associative arrays, so they run on the macOS default shell. They work with the BSD tools of macOS and the GNU tools of Linux, and start the same way from bash or zsh.
- `jq`, for [`checkpointer.sh`](skills/herdsman/scripts/checkpointer.sh).
- The GitHub CLI (`gh`): a version with `gh skill` (tested with 2.101.0), to install the skill; any recent version, when the route opens pull requests.
- A memory checkpoint command in the orchestrator's harness. The checkpointer's prompt is set at the top of [`checkpointer.sh`](skills/herdsman/scripts/checkpointer.sh).

## Develop

Run the tests after each change to the scripts, from zsh or bash:

```
zsh <skill folder>/scripts/test/run-tests.sh
zsh <skill folder>/scripts/test/checkpointer-tests.sh
```

Started from zsh or sh, each test script runs itself again under `/bin/bash`. CI runs both on macOS (bash 3.2, BSD tools) and Linux (GNU tools).

Check the checkpointer against a live orchestrator pane. The probe sends nothing:

```
TARGET=<orchestrator pane id> /bin/bash <skill folder>/scripts/checkpointer.sh --probe
```

Start the checkpointer in its own pane, to the right of the orchestrator:

```
herdr pane split <orchestrator pane id> --direction right --ratio 0.7 --no-focus
herdr pane rename <new pane id> checkpointer
herdr pane run <new pane id> "TARGET=<orchestrator pane id> /bin/bash <skill folder>/scripts/checkpointer.sh"
```

The script reads `INTERVAL` (seconds, default 900), `LIMIT` (tokens, default 250000), `PROMPT` and `COMPACT` from the environment. `--once` runs one checkpoint now and exits.

## Design notes

- **Background waits, not monitors:** an in-session monitor event does not wake an idle orchestrator. A background command that exits does.
- **Few wakes:** every wake is a whole orchestrator turn. The waiter runs up to 25 minutes, under the 30-minute limit for background commands, and wakes early only for an event.
- **A lock, not a rule:** a sentence in each contract does not keep agents from starting heavy gates at the same moment. A lock shared by every agent on the machine does, and the kernel frees it when its holder exits.
- **One herdr change per call:** two changes in one shell call have failed without output.
- **Deadlines belong to the orchestrator:** agents ignored stop times written in their own briefs. A queued instruction runs only when the agent's turn ends, so the orchestrator interrupts when the deadline passes.
- **Context size from the transcript:** the checkpointer reads the input and cache token counts of the orchestrator's last turn from its session transcript. The included reader expects JSONL transcripts with a usage block per turn. For a harness with another format, replace `transcript()` and `context_tokens()` in [`checkpointer.sh`](skills/herdsman/scripts/checkpointer.sh).

## License

GNU General Public License v3.0. See [LICENSE](LICENSE).
