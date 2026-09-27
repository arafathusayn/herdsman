# Muse Code workers (verified with Muse 1.4.0, 2026-09-27)

## Launch

```
herdr agent start wk-<x> --kind muse --pane <id> --timeout 90000 -- --model muse-spark-1.3 --reasoning-effort high --yolo --workspace <repo>
```

- The auto-mode classifier refuses the second and later `--yolo` launches ("Create Unsafe Agents"). Do not retry: put the exact lines on the clipboard (`printf '%s\n' ... | pbcopy`) and the user runs them with `!`. Prompts to those workers pass afterwards.
- A fresh `muse` in an untrusted folder shows a trust dialog; `--yolo` skips it. Start scratch Muse sessions in an already trusted folder.
- The user chose `muse-spark-1.3` (not `-contributor`) on 2026-09-27; the CLI default is the contributor model.

## Prompts

- Muse takes plain prompts (no `/goal`). A prompt sent while Muse is working is dropped. To queue into a busy worker: `herdr pane send-text <pane> "<text>"` then `herdr pane send-keys <pane> alt+enter` ("queued for the next turn").
- Brief shape (user rule, "prompt better"): end-state goal the user would see, numbered steps with files, a Check per step (command plus expected result), a leave-alone list with owners, hard rules (no git writes, throwaway HOME, time limits), and the report shape. Put fix rounds in a file and summarize the goal in the prompt. Pointer-only prompts ("Read X and do Y") let a worker stop at its path scope and leave obvious leftovers.
- Give time limits in the brief: targeted tests while working, each full suite once with `timeout 2400`, no background job over 40 minutes.

## Slash commands

- `/clear` (fresh session, same process and Herdr name), `/compact`, `/effort`, `/model`. Close a menu with `esc`.
- `/compact` right after a finished turn can answer "no compactable run is available" (`agent_prompt_stalled`); then `/clear` and make the next brief self-contained.
- `/model` needs the pane surface, not `agent prompt` (its Enter lands before the picker renders): `pane send-text <pane> "/model"`, `pane wait-output --match "Choose model"`, `send-keys enter` (a second Enter when the first only accepted the autocomplete), `wait-output --match "enter confirm"`, arrows (order: muse-spark-1.3, 1.3-contributor, 1.2, 1.2-contributor; the cursor starts on the current model), `enter`, effort picker `enter`. The footer shows the new model. A background Workflow started earlier keeps its old model.
- After the user restarts a worker with `muse resume`: the default model is back, the Herdr name is gone, background jobs are cancelled. Fix: `herdr agent rename <pane> <name>`, `esc`, `/model`, then a prompt listing what finished and what to rerun.

## State and health

- Muse starts long commands and Workflows as background tasks (footer `└ ◆ <label> running 23m`). Herdr then reports `idle`/`done` while work continues: read the footer before prompting or clearing.
- The "Thinking (1h 09m)" / "Calling tools" timer counts the whole turn. Real progress: files written in the last 10 minutes (`find <tree> -type f -mmin -10`; BSD `-newermt '-10 minutes'` silently matches nothing). `scripts/worker-status.sh` prints both and flags jobs over 30 minutes and trees with no writes.
- The report file may be written before a long run ends; its mtime churns, so the waiter emits several REPORT-UPDATED events. Act when the pane shows `Worked for ...`.

## Deadlines (2026-09-27)

- A worker does not enforce a "kill at HH:MM" line in its own brief; it kept starting test shards past it. At the deadline the orchestrator queues (send-text + `alt+enter`): "DEADLINE PASSED. Stop now: cancel every running command and background job, start no new runs. Write <report> from the results you already have, with STATUS gaps listing what did not run. Reply with only the report path." A queued steer lands only when the current turn ends, and a long turn may not end: after about 10 minutes with no report, `esc`. The interrupt ends the turn and the queued steer runs next (verified 18:21); re-prompt only if nothing was queued.
