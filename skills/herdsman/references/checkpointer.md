# Checkpointer

The checkpointer bounds the orchestrator's own context. It runs [`scripts/checkpointer.sh`](../scripts/checkpointer.sh) in a pane labelled `checkpointer`, next to the orchestrator pane (on its right).

## Reading the requests

- `start checkpointer every <N>m` means `checkpoint start --every <N>`.
- `checkpoint now and compact` means one checkpoint, then a compact whatever the size: run `TARGET=<pane> LIMIT=1 /bin/bash <skill>/scripts/checkpointer.sh --once` in the background and end the turn. The memory prompt lands first, then the compact command.

## Reading the numbers

- The context number comes from the usage of the last main-thread turn in the session transcript. Right after a compact no turn has run yet, so the script prints the same number as before the compact. An unchanged or high number there is not a failed compact; the next turn's reading is the real one.
- The script finds that transcript from the session id that Herdr reports for the orchestrator pane. Herdr learns the id from a start hook, saves it with the layout and restores it after a restart, so it can name a transcript that the running session never wrote. When the named file is missing, or is older than the newest `.jsonl` of the same project folder, the script uses the newest `.jsonl` of that folder. With no named file, it takes the project folder that matches the working folder of the checkpointer pane, so start that pane in the orchestrator's working folder (`--cwd` of `herdr pane split`). The fallback cannot tell two sessions of one folder apart, so read the reason before you trust the number.
- Run `checkpoint probe` after every restart (of the orchestrator, Herdr or the machine). It prints which transcript the script chose and why, with the context tokens. Check that the file belongs to the current session, because the compact decision rests on that number: a stale file gives a wrong reading, or no file at all, and then the context grows with no bound.

## Living with it

- A background event that wakes the orchestrator between the idle check and the prompt only delays the prompt; the harness queues typed input.
- `checkpoint now` and the timed checkpointer wait for an idle orchestrator pane with no time limit. Background workflows or agents keep the pane `working`, so the prompt is never sent. When `checkpoint now` sends no prompt within a few minutes (read the task output), write the checkpoint in your own turn, stop the `--once` script by its task id, and send the compact command yourself when the context is over the limit. The stop matters because the script would send a second prompt when the pane turns idle.
- Each checkpoint turn and each waiter wake counts as activity, so on an idle route the checkpointer keeps sending its memory prompt, and each turn costs context and finds nothing new. When the route waits only on people, offer the user to close the checkpointer (`checkpoint stop`), and close it only on their go. Write its options in the route file, so that `checkpoint start` with the same options brings it back when work resumes.
- One checkpointer per orchestrator. Checkpointer panes in other workspaces belong to other sessions; find yours by the pane's `label` field, not by a text search of the pane list. The `checkpoint` actions find the pane by the exact label `checkpointer`, so never rename that pane after it starts, and never start a second pane whose label contains the word. When the user gives panes friendly labels, or another pane already has such a label, the lookup fails or finds the wrong pane: address the checkpointer by its pane id (`herdr pane read <id>`, `herdr pane close <id>`), and do not start another.
