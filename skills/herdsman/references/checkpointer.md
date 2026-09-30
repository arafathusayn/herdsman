# Checkpointer

The checkpointer bounds the orchestrator's own context. It runs `scripts/checkpointer.sh` in a pane labelled `checkpointer`, next to the orchestrator pane (on its right).

## Reading the requests

- `start checkpointer every <N>m` means `checkpoint start --every <N>`.
- `checkpoint now and compact` means one checkpoint, then a compact whatever the size: run `TARGET=<pane> LIMIT=1 /bin/bash <skill>/scripts/checkpointer.sh --once` in the background and end the turn. The memory prompt lands first, then the compact command.

## Reading the numbers

- The context number comes from the usage of the last main-thread turn in the session transcript. Right after a compact no turn has run yet, so the script prints the same number as before the compact. An unchanged or high number there is not a failed compact; the next turn's reading is the real one.

## Living with it

- A background event that wakes the orchestrator between the idle check and the prompt only delays the prompt; the harness queues typed input.
- One checkpointer per orchestrator. Checkpointer panes in other workspaces belong to other sessions; find yours by the pane's `label` field, not by a text search of the pane list.
