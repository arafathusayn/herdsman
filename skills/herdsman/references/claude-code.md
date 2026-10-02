# Claude Code

Claude Code can take any agent type. Most notes below come from reviewer panes; "Claude Code panes" at the end holds the notes for every type.

## Reviewer launch form

```
herdr agent start rv-1 --kind claude --pane <id> --timeout 90000 -- \
  --model 'claude-opus-5-5[1m]' --effort xhigh --permission-mode auto \
  --add-dir <route folder root>
```

- `interactive_ready:true` arrives in seconds; the pane header shows the model and effort ("Opus 5.5 (1M context) xhigh"). Take the model from the interview.
- One `--add-dir` is safer: a launch with two was refused once by the auto-mode classifier as "Create Unsafe Agents".
- To stop an idle Claude pane, `ctrl+c` twice may not exit; type `/exit` and press enter. After a session exits, the Herdr name is gone (`agent_not_found`); relaunch and send a full-contract prompt, because the new session remembers nothing of earlier rounds.
- Without `--add-dir` the reviewer blocks on "Allow reads outside the working directories?" when the contract or the route files live outside its cwd. Do not answer that prompt for the user; relaunch with `--add-dir`.
- `--permission-mode auto` lets it run gh, git fetch, tests and the review skills without prompts; it still refuses dangerous shapes.
- Start it in its own tab ("Reviewer") with `--cwd <writable root>` so its worktree and report files are local.
- Review prompts go as `/goal <whole review>` (user rule). Before each new review: `/clear`, or `/compact` when the review needs the earlier rounds (a re-review of the same PR). Every goal says "Find only P0 and P1 issues."

## Prompt shape for Opus 5.5 (source: claude.dev/blog/getting-the-most-out-of-opus-5-5)

- Give the whole review in one message and name the finish line: the report file with a `VERDICT:` line for the head commit.
- Remove "think carefully" and "think step by step". The model reasons before each reply without them.
- Review wording from the source: "List only problems you'd block the merge for." It matches the P0 and P1 gate.
- Stopping rule: stop and ask only when it cannot continue without the orchestrator, or before anything destructive. In the report this is a `NEEDS:` line first under FINDINGS.
- Findings that it could not confirm say so and say where it looked.
- When a long run ends, read first what the reviewer waits on (`NEEDS:`), then the findings.
- Follow-up messages during a run are allowed (scope additions), but for this loop prefer a new prompt per review.
- A flagged message switches the session to an older model automatically. After such an event check the pane header still shows "Opus 5.5"; return with `/model`. The switch can be turned off in `/config` ("Switch models when a message is flagged"), but that is the user's setting: ask first.

## Skills

- The review skills must exist in the reviewer's harness: `ls ~/.claude/skills` (and plugin marketplaces) before writing the contract. Seen: `react-review` (static React Doctor scan plus the effects checklist; the runtime React Scan layer needs a running app, skip it), `thermo-nuclear-code-quality-review` (strict maintainability review grounded in the engineering canon; `disable-model-invocation: true`, so it runs only when named in the prompt or the contract).
- Name the skill with its slash form in the contract and say which file set it applies to.
- If a named skill is missing, the reviewer must say so in the report and do a direct pass with the same checklist; do not let it silently skip.

## Severity gate

- Define P0 and P1 in the contract (see [`contracts.md`](contracts.md)). Everything below P1 is omitted, not demoted.
- The `VERDICT:` line is the gate. `pass` = zero P0 and zero P1.
- Findings carry `file:line`, what happens, and a one-sentence fix, so the implementer or the integrator can act without reading the reviewer's reasoning.

## Loop mechanics

- Each reviewer is persistent and takes one review at a time, one prompt per review: contract path, PR number, branch, task contract, report path `review-<pr>-<k>.md`.
- After a `fix` verdict the implementer (or, after the final review, the integrator) fixes; the reviewer then reviews the new head as `review-<pr>-<k+1>`. Never overwrite a review file.
- Three fix rounds on one pull request without pass: stop, show the user the remaining findings and both sides' reasons.
- The reviewer posts nothing on GitHub. If the user wants review comments on the pull request, that is a separate, explicit step.
- Context (user rule): the same rule as for every agent. A re-review of a pull request the reviewer already reviewed is a follow-up: keep the context (it knows its own findings), `/compact` first when the footer shows more than 50%. A pull request it has not seen is a new task: `/clear` first, confirm the empty prompt, then the review prompt (which names the contract file, so nothing is lost). A review of a medium pull request used about 10% of a 1M context at xhigh. Claude Code's percentage is in the pane footer ("19% 190k/1M"); `herdr agent list` shows none for Claude.

## Watching the reviewer

[`scripts/watch-reviewer.sh`](../scripts/watch-reviewer.sh) polls the pane every 90 s: REVIEW on a new `review-*.md`, BLOCKED on permission or password text ("Allow reads outside", "Do you want to proceed", "Password for"), IDLE when the screen changed and no working marker ("esc to interrupt", "thinking", "Incubating", "tokens") is present. Claude Code's working markers differ from Codex's; keep both lists in the scripts.

## Claude Code panes (every agent type)

- Implementer or integrator launch: the reviewer form with the model and effort from the interview, `--permission-mode auto`, and `--add-dir <route folder root>` when the route folder is outside the agent's cwd.
- The first launch in a folder shows a trust prompt with "No, exit" selected: send `down`, then `enter`.
- Working marker: a spinner line "… (12s · ..." (`… \([0-9]` in the scripts), not "esc to interrupt".
- Wait for a `/compact` to end ("Compacting conversation" gone, the prompt line back) before the next prompt.
- A cleared pane can keep an unsent input line; clear the input before the next prompt.
- After a turn ends, Claude Code can show a prompt suggestion in the input line (for example `❯ is CI green yet?`). `herdr pane read` has no colors, so the grey suggestion looks like typed text. It is not sent: never press enter on it. A new `herdr agent prompt` replaces it.
- A pane that shows "Update installed · Restart to update" keeps working on the old version. Do not restart mid-route: a restart loses the session's context and its launch flags. Restart between routes.
- When the auto-mode classifier blocks or stops ruling on agent work, the user may choose to launch implementers with `--dangerously-skip-permissions`. That launch is the user's decision; never choose it yourself.
- Each agent's session transcript is `~/.claude/projects/<cwd with slashes as dashes>/<session id>.jsonl`. Grep it for the commands and their output to check a report's claims (for example a full gate that finished too fast) without reading the pane.
- Never prompt a working Claude agent with its next job: the harness delivers the prompt in the middle of the turn. Wait for its report.
- That delivery is useful for amendments: a prompt to a working agent lands inside its running turn at the next tool call. Use it for a new due time, a scope change, or a defect found in the deployed check. A follow-up that says "do this after your report" also works; the agent then replies with both report paths.
- Stop a running task: send `esc` (the turn ends), then a stop prompt with steps: commit nothing more, no push, stop every process and test server, drop your test databases, write the report with `STATUS: stopped`, reply with only its path. Then clear the agent. A grey goal-clear text in the input line after that is a suggestion, not a sent command.
- A shell step that appends with a heredoc can wait for input forever (a step that shows "Running" for minutes). Send `esc`, check that the process is gone and whether the file got a partial append, then tell the agent to write with its file tools.
- The permission guard can refuse an agent's push to the default branch. See [`shipping.md`](shipping.md), Who pushes.
- A long prompt pasted into the input can show "paste again to expand"; read more pane lines to see whether the turn started.
- "Waiting for API response · will retry in N m" recovers on its own within minutes. Read the pane again at the next wake before any restart.
