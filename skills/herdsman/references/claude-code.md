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
- To stop an idle Claude pane, type `/exit` and press enter (`herdr agent prompt <name> "/exit"`). `ctrl+c` and `ctrl+d` do not end the session. The exit prints the resume id in the scrollback. Before the next start in that pane, read a bare shell prompt ([`herdr.md`](herdr.md), Gotchas). After a session exits, the Herdr name is gone (`agent_not_found`); relaunch and send a full-contract prompt, because the new session remembers nothing of earlier rounds.
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
- Context (user rule): the same rule as for every agent. A re-review of a pull request the reviewer already reviewed is a follow-up: keep the context (it knows its own findings), and `/compact` it right after its previous turn ends, with no threshold ([`context-hygiene.md`](context-hygiene.md)). A pull request it has not seen is a new task: `/clear` first, confirm the empty prompt, then the review prompt (which names the contract file, so nothing is lost). A review of a medium pull request used about 10% of a 1M context at xhigh. Claude Code's percentage is in the pane footer ("19% 190k/1M"); `herdr agent list` shows none for Claude.

## Watching the reviewer

[`scripts/watch-reviewer.sh`](../scripts/watch-reviewer.sh) polls the pane every 90 s: REVIEW on a new `review-*.md`, BLOCKED on permission or password text ("Allow reads outside", "Do you want to proceed", "Password for"), IDLE when the screen changed and no working marker ("esc to interrupt", "thinking", "Incubating", "tokens") is present. Claude Code's working markers differ from Codex's; keep both lists in the scripts.

## Claude Code panes (every agent type)

- Implementer or integrator launch: the reviewer form with the model and effort from the interview, `--permission-mode auto`, and `--add-dir <route folder root>` when the route folder is outside the agent's cwd.
- The first launch in a new folder can show a folder-trust dialog and a project tool-server (MCP) dialog. Read each dialog and answer it by its visible option. Never send a blind `enter`, because the first option can be "No, exit", which ends the session. With "No, exit" selected, send `down`, then `enter`. The dispatch plan names the trust answer for each new folder, so the user's go covers it. Without that go, quote the dialog to the user first, because the answer grants access to the user's folder.
- Working marker: a spinner line "… (12s · ..." (`… \([0-9]` in the scripts), not "esc to interrupt".
- Wait for a `/compact` to end ("Compacting conversation" gone, the prompt line back) before the next prompt.
- A cleared pane can keep an unsent input line; clear the input before the next prompt. To pull back a queued message, send `up`, then `ctrl+u` once for each visual line, because the queued text sits in the input as several lines and one clear leaves lines behind.
- After a turn ends, Claude Code can show a prompt suggestion in the input line (for example `❯ is CI green yet?`). `herdr pane read` has no colors, so the grey suggestion looks like typed text. It is not sent: never press enter on it. A new `herdr agent prompt` replaces it. `esc` does not clear it; to tell it from real typed text, send one key (`herdr agent send-keys <name> x`), read the pane (a suggestion is gone, typed text keeps the key at its end), then `backspace`.
- A pane that shows "Update installed · Restart to update" keeps working on the old version. Do not restart mid-route: a restart loses the session's context and its launch flags. Restart between routes.
- When the auto-mode classifier blocks or stops ruling on agent work, the user may choose to launch implementers with `--dangerously-skip-permissions`. That launch is the user's decision; never choose it yourself.
- Each agent's session transcript is `~/.claude/projects/<cwd with slashes as dashes>/<session id>.jsonl`. Grep it for the commands and their output to check a report's claims (for example a full gate that finished too fast) without reading the pane.
- Never prompt a working Claude agent with its next job: the harness delivers the prompt in the middle of the turn. Wait for its report.
- That delivery is useful for amendments: a prompt to a working agent lands inside its running turn at the next tool call. Use it for a new due time, a scope change, or a defect found in the deployed check. A follow-up that says "do this after your report" also works; the agent then replies with both report paths.
- Stop a running task: send `esc` (the turn ends), then a stop prompt with steps: commit nothing more, no push, stop every process and test server, drop your test databases, write the report with `STATUS: stopped`, reply with only its path. Then clear the agent. A grey goal-clear text in the input line after that is a suggestion, not a sent command.
- A shell step that appends with a heredoc can wait for input forever (a step that shows "Running" for minutes). Send `esc`, check that the process is gone and whether the file got a partial append, then tell the agent to write with its file tools.
- The permission guard can refuse an agent's push to the default branch. See [`shipping.md`](shipping.md), Who pushes.
- A long prompt pasted into the input can show "paste again to expand"; read more pane lines to see whether the turn started.
- A `✦ Heads up · <finding>` notice with "1: Learn more  2: Knew this already  0: Dismiss" above the input is a side agent's finding about the agent's task, shown to the user. Such a notice can be a real contract defect (for example a unique-name clash or a broken test command). The waiter does not report them, so a separate poller reads every route pane (the orchestrator's own pane too) every 60 seconds for new notices; the user wants each one handled, not left. A notice is a pointer, not a ruling: verify its claim against the code. A note that argues against a finding can cite a superseded exception, so check the line that it cites against the current rule and the precedent in the code before you drop the finding.
- A notice does not block prompts: a prompt that starts with a letter goes to the input with the notice present, and a prompt sent while the agent works shows "Press up to edit queued messages" and lands at its next tool call. Prefer to send the orchestrator's ruling as a normal prompt, with no notice key.
- When the notice itself must be answered, keep this procedure. Zoom the pane first (in a narrow pane the details and choices are cut off), press `1` (Learn more) to show the details and the choices "1: Understood  2: Chat in main session  0: Dismiss", read them, verify against the code. A notice that is still true and needs the agent to act: `2` puts its text in the agent's input, `enter` sends it (queued while the agent works), then send the ruling as a prompt. A notice already resolved or covered by an earlier amendment: `1`. Unzoom after, and restore the user's keyboard focus ([`herdr.md`](herdr.md), Etiquette). Arrow keys sent to a pane go to the input, not the notice.
- Read the pane before a notice key and again after it. The notice can be closed already, and then the key lands in the agent's input as stray text: clear it with `backspace` before the next prompt. Never send keys to the orchestrator's own pane. Read a notice there from the pane text and tell the user, because the keys land in its own input line or answer a survey.
- A permission prompt closes itself with "No" after about one minute, and the agent can then try another way around the denial. A pane watcher that must catch a permission prompt polls every 60 seconds or less.
- "Held message from another session" with "Deny / Deliver this message to Claude" in an agent pane is a message from another Claude session on the machine (often another project), held because the permission classes differ. If it is not route work, choose Deny (it tells the sender) and tell the user; delivering it injects foreign context into the agent's task.
- To send work or a question to another Claude Code session, use `herdr agent prompt <name> "<text>"` and read the reply with `herdr pane read`. The session-to-session message tool can wait for the receiving user's approval when the permission modes differ, and a held message can expire without delivery. A Herdr prompt arrives as a user turn with no hold.
- A Claude agent can ask the user directly (an AskUserQuestion in its own pane), and the user may answer there without telling the orchestrator. Treat a report line such as "decided by the owner" as the user's decision only after reading the question and the answer in the agent's session transcript (grep its question header). Then write it as a numbered amendment in the task contract AND the reviewer contract, so the reviewer checks the new rule instead of reporting the changed behavior as a finding.
- Outside a compaction, "Waiting for API response · will retry in N m" recovers on its own within minutes. Read the pane again at the next wake before any restart. When `/compact` hangs on a "Waiting for API · will retry" notice, do not wait out the retry: send `esc`, clear the restored command text with `ctrl+u`, then send the brief, because the agent still has room in its context. A brief sent while `/compact` still runs is queued and runs after it; pull it back as described above if it is stale.
- Usage figures show in the pane status bar (the footer): the 5-hour and weekly windows, each with a percent and a reset time. Read them with `herdr pane read <pane> --lines 40`. A figure on an idle pane can stay unchanged for minutes, so read it again before you compute a rate. The 5-hour window counts every session of the account. The pacing rule is in [`flow-and-time.md`](flow-and-time.md).
- A separate Claude config folder logs the session out, because the login is in the system keychain. It is not isolation for a live test ([`contracts.md`](contracts.md), Live tests).
