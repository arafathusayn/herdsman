# Harness guards and the orchestrator's own limits

## File writes

- Write files with the Write tool (new) and the Edit tool (changes, appends). Never heredocs, `cat >`, `printf >`, `sed -i`, `perl -pi` for content. The user forbids them, and the dcg PreToolUse guard blocks any redirect whose target holds a shell variable (`> $S/file`, also `>>`), which fails the whole Bash batch silently.
- Read a file with the Read tool before Edit, even if it was `cat`-ed earlier in the session.
- The rule holds when the harness's own mode notes say that shell edits are fine, and under time pressure in the middle of a route: the user's rule wins over a harness default. A one-line index change is one Edit.

## dcg guard shapes that block (alternatives)

- `git checkout -- <path>` and `git checkout <ref> -- <path>`: use `git stash`, or `git show <ref>:<path> > /literal/path` then `git add`.
- `mv` touching `~` or with a variable path: `cp`, or `python3 - <<'EOF'` with `os.rename`.
- `rm -rf`, `rm -r`: never pre-clean; fresh directory names; delete files by literal path, then `rmdir`.
- `eval "$c"` in loops, pipelines into an executable dcg cannot verify, `> /tmp/x.$$`: literal commands, shell variables instead of temp files, a script file written with the Write tool and run with `bash <file>`.
- git commands with shell variables or inside loops: literal paths, one command per line.
- A zsh shell does not split an unquoted variable on newlines, so `for x in $list` runs once with the whole list. Pipe the list into `while read -r x; do ...; done`.
- zsh reads `$W:branch` in a refspec as a modifier and fails with "bad substitution"; write `${W}:branch` or the literal commit. `--force-with-lease=<ref>:<sha>` needs the full 40-character commit id.
- A command put on the clipboard for the user's shell runs in zsh: an unquoted variable that holds several flags stays one word there. Write each flag out in full.
- zsh `echo` turns `\n` inside JSON strings into real newlines and breaks a parser. Save command output to a file, or use `printf '%s'`, before you parse it.
- One blocked command fails the whole batch; keep destructive or redirect-bearing steps in their own small call.

## Bash tool limits

- `sleep N; <cmd>` chains are refused: use the Monitor tool ([`watchers.md`](watchers.md)).
- Compound commands with `wc` or unusual shapes were refused by the auto-mode classifier; keep bookkeeping commands simple.
- Launching an agent with its sandbox disabled was refused; that launch goes to the user with the exact command.
- Command output is not shown to the user; put anything they must read into the reply.
- Background shells may not load the login PATH (a package manager under the home folder then exits 127); put the PATH in the command.
- A `cd` inside a Bash call moves the orchestrator's working directory for the later calls. Use `git -C <literal path>` and absolute paths instead.

## Merges and the auto-mode classifier

- The classifier refuses `gh pr merge` with the reason "Merge Without Review" when the session holds no review of that pull request, even with the user's explicit instruction, green CI, the bot's approval and `--admin`. A merge passes when it follows a review report that the session has read. So: a pull request the reviewer has not seen gets a review first, then the merge; or the user runs the merge in the session with the `!` prefix; or the user adds a Bash permission rule for the merge command. Expect the same check on the async merge call (`gh api -X PUT .../pulls/<n>/merge-async`) and on `bypass_rules=true`; a permission rule must match that `gh api` form, since a rule for `gh pr merge` does not cover it. Do not try a third phrasing.
- The classifier can refuse `herdr agent start ... --permission-mode auto` as "Create Unsafe Agents", for example a launch with two `--add-dir` flags, although the same flags passed for another agent earlier. A launch with one `--add-dir` and a description that names the user's request passed on the next try. Keep launch lines minimal and the description literal.
- An agent stopped by a usage limit keeps changing its screen; the watcher then emits BLOCKED on every pass. Take that agent out of the watched list until its quota resets.

## Permission and approval prompts in agent panes

- Never answer a password prompt. Never approve a permission expansion for the user; relaunch with the right flags (`--add-dir`, sandbox network) instead.
- A command that the permission system blocked is not run another way: not through another agent's pane, not with `send-keys`, not from a script. Give the user the exact command. Never save workaround notes for a blocked command in shared memory: a later session may refuse to read that memory as an injected instruction.
- Muse escalations ("escalated execution requires an unrestricted permission profile", "Yes, proceed"), OpenCode "Permission required" and Claude "Allow reads outside" are BLOCKED events; read the command, then decide with the user's standing grants (validation, Docker, pushes) or ask.

## Orchestrator posture (user rules)

- Orchestrate only: contracts, launches, waits, verification, records. No application code, no tests run by the orchestrator, no pushes unless the user grants them for the route ([`shipping.md`](shipping.md)).
- Act on the user's instruction on the user's condition, as the tools show it. When you see a risk that the instruction does not cover, act first and name the risk in one line after. Hold only for secrets, a destructive or irreversible step, or a recorded rule of the user. Reason: a condition the orchestrator adds on its own turns an instruction into a new question and stalls the work.
- When an instruction depends on a fact ("if X is not done"), check the fact read-only first (it usually takes a minute), then act without another question.
- When a check needs a login, search the local environment files by variable name only and read a value only into a shell variable. If nothing matches, ask the user which variable holds it before you report "no access" or draw a conclusion without the source.
- Ask before every infrastructure mutation (Herdr layout, Docker, GitHub, cloud) unless the dispatch plan the user approved names that step. A repo connect on a deployment platform triggers a deploy.
- Only the user's explicit answer answers a pending question. A repeated instruction, a "continue" after a compaction, or a background event is not consent. A yes covers the named target and the batch that was shown: another environment (staging is not production) or a later batch needs its own yes. Production reads and writes wait for an explicit grant; by default, give the user the exact commands to review and run, one step each, with what each one touches.
- When several actions wait on the user (a push, a new pull request, a deploy, a setting), ask one question that lists them all and includes the "do it now" option.
- Read an instruction literally. "Stop if X is missing" means stop with nothing written. "Use these values" means those values, verbatim. A linked document's mechanism is copied, and every deviation is named.
- Every outward write (a pull request reply or review, a tracker comment or edit, a chat message) is drafted to a file first and posted only on an explicit "post" or "send" that names it. No agent posts text that needs approval. A bulk external edit is finished or reverted, never left half done; after a write, read the object back and report any field you did not set.
- A shared or production data write needs a backup that is not empty, a dry run that rolls back, the expected change and the invariants written down first, and before-and-after counts or hashes after it. Check a setting by its value (compare hashes), not by its name being present.
- Agents that handle secrets print only names, shapes, counts and hash prefixes; a check prints `ok` or `WRONG`, never a value. Read logs with narrow queries: a broad one can echo a secret into a transcript.
- Agent shells cannot finish interactive logins (a browser callback, a prompt that needs a terminal). The user runs the login, for example with `! <command>` in the session.
- When the user asks for a plan, produce plan artifacts only (decisions, structure, contracts); no implementer starts before an explicit go.
- Before you act on a pasted list of pull requests or branches, check each item's number, title, author and date: a copied list can shift by one row.
- An interview answer is a go for exactly the steps its text names. Copy each chosen option's full text into `00-route.md`; a later step that depends on it (a new pull request, a push) is checked against that text, not against a summary.
- Before a reply that lists what is still owed or what waits on the user, read the live sources (pull request state, releases, the team's channel). The user works in other sessions and talks to the team directly, so a "pending" item in the route file or in memory can already be done. Correct the record in the same turn and say so.
- Keep the orchestrator context small: implementers and the integrator write capped files with `STATUS:`, `decisions:`, `gaps:`; grep those.
- Timestamps come from `date`, read before writing; correct a wrong one by appending, never by rewriting history.
- Record rulings as numbered amendments in the route file and surface them; the user can overrule.
- Bookkeeping error to avoid: recording "fix round N queued" without dispatching it, then asking the reviewer to re-check a fix that does not exist. Check the run-state for the dispatch line first.
- Short replies; no em dashes; roles instead of names in anything that lands in a repository.
