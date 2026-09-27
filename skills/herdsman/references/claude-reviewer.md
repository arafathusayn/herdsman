# Claude Code reviewer

## Launch form (verified with Claude Code 2.1.x, 2026-09-22)

```
herdr agent start rv-1 --kind claude --pane <id> --timeout 90000 -- \
  --model 'claude-opus-5-5[1m]' --effort xhigh --permission-mode auto \
  --add-dir <route folder root>
```

- `interactive_ready:true` arrives in seconds; the pane header shows the model and effort ("Opus 5.5 (1M context) xhigh"). The user moved the reviewer from claude-fable-5-1 to Opus 5.5 on 2026-09-23; take the model from the interview.
- One `--add-dir` is safer: a launch with two was refused once by the auto-mode classifier as "Create Unsafe Agents".
- To stop an idle Claude pane, `ctrl+c` twice may not exit; type `/exit` and press enter. After a session exits, the Herdr name is gone (`agent_not_found`); relaunch and send a full-contract prompt, because the new session remembers nothing of earlier rounds.
- Without `--add-dir` the reviewer blocks on "Allow reads outside the working directories?" when the contract or the route files live outside its cwd. Do not answer that prompt for the user; relaunch with `--add-dir`.
- `--permission-mode auto` lets it run gh, git fetch, tests and the review skills without prompts; it still refuses dangerous shapes.
- Start it in its own tab ("Reviewer") with `--cwd <writable root>` so its worktree and report files are local.
- Review prompts go as `/goal <whole review>` (user rule 2026-09-23 14:4x). Before each new review: `/clear`, or `/compact` when the review needs the earlier rounds (a re-review of the same PR). Every goal says "Find only P0 and P1 issues."

## Prompt shape for Opus 5.5 (source: claude.dev/blog/getting-the-most-out-of-opus-5-5, read 2026-09-23)

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

- Define P0 and P1 in the contract (see `contracts.md`). Everything below P1 is omitted, not demoted.
- The `VERDICT:` line is the gate. `pass` = zero P0 and zero P1.
- Findings carry `file:line`, what happens, and a one-sentence fix, so the worker can act without reading the reviewer's reasoning.

## Loop mechanics

- One persistent reviewer, one pull request at a time, one prompt per review: contract path, PR number, branch, task contract, report path `review-<pr>-<k>.md`.
- After a `fix` verdict the worker fixes and pushes; the reviewer then reviews the new head as `review-<pr>-<k+1>`. Never overwrite a review file.
- Three fix rounds on one pull request without pass: stop, show the user the remaining findings and both sides' reasons.
- The reviewer posts nothing on GitHub. If the user wants review comments on the pull request, that is a separate, explicit step.
- Context (user rule, 2026-09-22, corrected the same day): the same rule as for implementers. A re-review of a pull request the reviewer already reviewed is a follow-up: keep the context (it knows its own findings), `/compact` first when the footer shows more than 50%. A pull request it has not seen is a new task: `/clear` first, confirm the empty prompt, then the review prompt (which names the contract file, so nothing is lost). A review of a medium pull request used about 10% of a 1M context at xhigh. Claude Code's percentage is in the pane footer ("19% 190k/1M"); `herdr agent list` shows none for Claude.

## Watching the reviewer

`scripts/watch-reviewer.sh` polls the pane every 90 s: REVIEW on a new `review-*.md`, BLOCKED on permission or password text ("Allow reads outside", "Do you want to proceed", "Password for"), IDLE when the screen changed and no working marker ("esc to interrupt", "thinking", "Incubating", "tokens") is present. Claude Code's working markers differ from Codex's; keep both lists in the scripts.
