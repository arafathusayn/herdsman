# herdsman

A coding-agent skill that runs a team of coding agents inside [herdr](https://github.com/herdrdev/herdr). One agent session is the orchestrator. Worker agents write the code. A reviewer agent reviews each result. The loop of fixes and reviews continues until each task passes.

Each role can use a different harness and a different model. Any coding agent that herdr can start and recognize in a pane can be a worker or the reviewer.

## Features

- **Parallel workers:** one worker agent per task, each in its own herdr pane, optionally in its own git worktree and branch.
- **Strict review:** one reviewer agent, usually on a stronger model than the workers, reports only P0 and P1 findings. A task gets at most three fix rounds; then the user decides.
- **Event-driven waits:** a background waiter wakes the orchestrator on a new report, a new review, a blocked worker or a stalled worker.
- **Health checks:** on every wake, a status script shows each agent's state, model, background jobs with their ages, and recent file writes. It flags jobs that run too long and trees with no writes.
- **Checkpointer:** a pane next to the orchestrator asks the orchestrator to save its memory every 15 minutes, and to compact its context when the context passes a token limit.
- **Context hygiene:** each agent is compacted or cleared as soon as it finishes.
- **No-commit routes:** when commits are not allowed, reviews use tree snapshots from a temporary git index.

## How it works

1. **Interview:** the orchestrator asks once for the tasks, branches, harnesses, models, merge policy and test setup. Then it shows a dispatch plan and waits for the user's go.
2. **Contracts:** the orchestrator writes a route folder with shared rules, one contract per task and a reviewer contract.
3. **Launch:** the orchestrator creates the herdr tabs and panes, starts the agents, and sends each worker a full brief. A brief has the goal, numbered steps with a check for each step, a leave-alone list, time limits and the report format.
4. **Wait:** the waiter script exits on the first event. The orchestrator handles the event, runs the health check, and starts the waiter again.
5. **Review:** each finished task goes to the reviewer. A `fix` verdict goes back to the worker as a fix round. A `pass` verdict closes the task.
6. **Close:** the orchestrator records the results and writes the lessons into memory. General lessons become rules in the skill's concept files (`references/flow-and-time.md`, `context-hygiene.md`, `ownership-and-integration.md`, `review-discipline.md`, `shared-environment.md`, `checkpointer.md`) or in its tool notes.

The orchestrator does not write application code and does not push. The workers do.

## Install

1. Install [herdr](https://github.com/herdrdev/herdr).
2. Install the coding-agent harnesses you want for the orchestrator, the workers and the reviewer.
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
/herdsman run "tasks: issues 12 and 14; workers: <harness> <model>; reviewer: <harness> <model>"
```

The skill stops when `HERDR_ENV` is not `1`.

## Requirements

- `/bin/bash` 3.2 or later. The scripts use no associative arrays, so they run on the macOS default shell.
- `jq`, for `checkpointer.sh`.
- The GitHub CLI (`gh`): a version with `gh skill` (tested with 2.101.0), to install the skill; any recent version, when the route opens pull requests.
- A memory checkpoint command in the orchestrator's harness. The checkpointer's prompt is set at the top of `checkpointer.sh`.

## Develop

Run the tests after each change to the scripts:

```
/bin/bash <skill folder>/scripts/test/run-tests.sh
/bin/bash <skill folder>/scripts/test/checkpointer-tests.sh
```

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
- **One herdr change per call:** two changes in one shell call have failed without output.
- **Deadlines belong to the orchestrator:** workers ignored stop times written in their own briefs. A queued instruction runs only when the worker's turn ends, so the orchestrator interrupts when the deadline passes.
- **Context size from the transcript:** the checkpointer reads the input and cache token counts of the orchestrator's last turn from its session transcript. The included reader expects JSONL transcripts with a usage block per turn. For a harness with another format, replace `transcript()` and `context_tokens()` in `checkpointer.sh`.

## License

GNU General Public License v3.0. See [LICENSE](LICENSE).
