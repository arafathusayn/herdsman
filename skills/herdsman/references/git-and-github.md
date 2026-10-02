# Git, worktrees and GitHub

## Identity and credentials

- Never change git identity, remotes or global config in any pane. The one-shot credential helper is the only way to authenticate a push or fetch:
  `git -c credential.helper='!f() { echo username=<gh account>; echo password=$(gh auth token --user <gh account>); }; f' <fetch|pull|push ...>`
- `gh` with a non-default account: `GH_TOKEN=$(gh auth token --user <gh account>) gh ...` inline on every call. The default account may not see the organisation at all ("Could not resolve to a Repository").
- GitHub handles are names: keep them out of run-state files and pull-request bodies (roles or issue numbers instead).

## Worktrees (optional per route)

- One worktree per task, lettered, under a git-ignored `worktrees/` folder next to the clone: `git -C <clone> worktree add <worktrees>/<x> -b <branch> origin/<base>`.
- Fetch before branching; the local base branch is usually behind `origin/<base>`.
- Existing worktrees on the target branch may be reused if clean; say which letters are free in the contract, or an implementer picks one that a sibling expects.
- Reviewer worktree: detached (`--detach origin/<base>`), then `git checkout --detach origin/<head>` per review.
- Stacked work rebases onto the sibling branch inside the same repository (`git rebase <sibling>` works across worktrees).
- Migration numbering collides across open pull requests; the shared rules must state the next free number and which open branches already use the following ones.

## No-commit routes (unborn branch, "no commit until done")

- No worktrees (they need a commit). Review units are tree snapshots taken with a temporary index: `cp .git/index <scratch>/snap.index && GIT_INDEX_FILE=<scratch>/snap.index git add -A && GIT_INDEX_FILE=<scratch>/snap.index git write-tree`; review with `git diff <before> <after>`; prove the user's index is untouched with `git diff --cached --quiet <S0 tree>`.
- Baseline copy: `git checkout-index -a --prefix=../<name>-baseline/`. Parallel work: APFS clones (`cp -cR`), ported back with `git apply --3way` on a temp index. Only on disjoint files; a whole-repo rename in one tree makes a parallel clone a duplicate.
- A hand-run `git add -A` after re-init drops ignored-but-tracked files; list them and `git add -f` them in the final commit.
- Implementers: no git command that writes, `mv` not `git mv`, throwaway `HOME`.

## Pull requests

- READY pull requests, not drafts, when the user said so. `gh pr create --repo <org>/<repo> --base <base> --head <branch> --title ... --body-file <file>`.
- Body rules (user's standard): Summary, per-file bullets with why, Verification with exact commands and databases, Known gaps. No reviewer or tool names, no people, no metrics, no process history. Grep the body for banned words before creating.
- Under a "CI green before the body" rule: open the pull request with a minimal body, wait for CI, then the integrator writes, checks and pushes the full body.
- Landing-order gates ("rebase after PR n merges") are Known-gap lines, not defects, so the pull request can open.
- Stacked pull requests open with `--base <sibling branch>`; CI still runs against the workflow of the default branch, so a failing check may come from the base.
- GitHub native stacks (`repos/O/R/stacks`, no preview header needed):
  - Create, bottom to top: `printf '{"pull_requests":[101,102]}' | gh api -X POST repos/O/R/stacks --input -` (integers, 2 to 100; each base ref must equal the previous head ref; same repository). The answer carries the stack `number`.
  - Append on top: `printf '{"pull_requests":[103]}' | gh api -X POST repos/O/R/stacks/<stack>/add --input -` (the first new base ref must equal the current top's head ref).
  - Find a pull request's stack: `gh api repos/O/R/stacks --paginate --jq '.[] | select(any(.pull_requests[]; .number==<n>)) | .number'`; read one with `gh api repos/O/R/stacks/<stack>`.
  - Unstack: `gh api -X POST repos/O/R/stacks/<stack>/unstack` removes every unmerged pull request it can (queued ones stay); 200 returns the rest, 204 means the stack is gone, 409 means another request is changing it.
  - Creating, extending or dissolving a stack, and retargeting a base, are GitHub mutations that need the user's go unless the task file grants them.

## Review bots (Pullfrog seen)

- Reviews arrive within minutes of a push. Loop: fetch review body and inline comments, verify the claim, test-first fix, push, reply on the thread via `gh api repos/O/R/pulls/<n>/comments/<id>/replies` with "Addressed in <sha>".
- "No new issues found" in a review body means clean; the approval check flips to pass only then. The approval check attaches to the reviewed commit and never clears on thread resolution alone; post an issue comment mentioning the bot and asking for a fresh review of the current head.
- The bot keeps finding the next layer on concurrency and object-store code; design two-phase claims with unique tokens up front, no store I/O inside a database transaction, and race tests that are shown to fail on the old code and synchronise with barriers, not sleeps.
- Review rounds, not coding, take most of the time from implementation to an open green pull request.
- GitHub Actions can stop for the whole organization on a billing failure ("The job was not started because recent account payments have failed or your spending limit needs to be increased"); every job and the review bot then fail within seconds of scheduling. Only the organization owner fixes it; a re-run of a failed run is the cheapest probe. The local review stays valid; merging without CI is the user's call.
- Pullfrog runs on the same model account as the Codex implementers; it is a third-party bot, not a GitHub feature. When that account's usage limit is reached, Pullfrog's reviews and its approval check fail as well, so a merge gate that needs the bot cannot pass until the reset. Tell the user; the merge decision without the bot is theirs.

## Merges

Merges are the user's unless the interview (or a later instruction) authorizes the orchestrator. An authorized form: "Merge the PRs when both the local review and the bot reviewer on GH approve them. Use squash and merge technique."

- Gate, on the SAME head commit: latest `review-<pr>-<n>.md` says `VERDICT: pass`; the bot approval check is SUCCESS; every CI check green; `mergeable: MERGEABLE`. A push after the review invalidates the pass.
- Command: the asynchronous merge REST API, the only merge path that handles native stacks and merge queues (`gh pr merge` cannot merge a stacked pull request: "must be merged using the asynchronous merge REST API"). Read the repo's merge settings first (`gh api repos/<org>/<repo> --jq '{allow_squash_merge,delete_branch_on_merge,squash_merge_commit_title,squash_merge_commit_message}'`); pass title and message explicitly when the defaults would concatenate commit messages. Then request the merge, pinned to the reviewed head:
  `GH_TOKEN=$(gh auth token --user <account>) gh api -X PUT repos/<org>/<repo>/pulls/<n>/merge-async -f merge_method=squash -f merge_action=direct_merge -f sha=<reviewed head> -f commit_title="<pr title> (#<n>)" -F commit_message=@<pr body file> --jq '.status, .details.uuid, .details.message'`
  - Answers: 202 `pending` with a `uuid`; 200 when already `merged` (with the merge commit sha) or already `enqueued`; 409 when a request for this pull request is already pending: its answer names that request's uuid and options, which may differ from yours. Adopt that uuid only when `expected_head_sha` is the reviewed head and `merge_method`, `merge_action` and `bypass_rules` match what the user authorized; otherwise do not poll it as this merge and do not send another request: stop and show the user the pending request; 400 `failed` for basic state (closed, draft). Rulesets and branch protection are checked later, in the background, so a 202 is not a merge.
  - Poll with the Monitor tool, never a sleep loop, with the same account: `GH_TOKEN=$(gh auth token --user <account>) gh api repos/<org>/<repo>/pulls/<n>/merge-async/<uuid> --jq '.status, .details.message'` until a final status: `merged` (`.details.sha` is the merge commit), `enqueued`, or `failed` (`.details.message` says why). Stop polling the uuid on any of the three. A push to the head after the request cancels the merge; that is the `sha` pin working. A uuid expires 24 hours after its last update (404).
  - `merge_action`: `direct_merge` merges now; `merge_queue` adds to the base branch's merge queue; `default` (or omitted) picks the queue when one is configured. `commit_title`, `commit_message` and `merge_method` apply to direct merges only. `enqueued` is final for this API and does NOT mean merged: confirm the merge in a second stage with `GH_TOKEN=$(gh auth token --user <account>) gh api repos/<org>/<repo>/pulls/<n>/merge` (204 merged, 404 not yet) or `GH_TOKEN=$(gh auth token --user <account>) gh pr view <n> -R <org>/<repo> --json state,mergeCommit`.
  - Stacks: requesting the merge of a stacked pull request merges it together with every open pull request below it in the stack. Every one of them must pass the gate above on its own head before you request the top one; to land only the bottom part, request the merge of the highest pull request that passed.
  - `gh pr merge <n> --squash --subject ... --body-file ...` still works for a pull request outside any stack, but prefer the async call so one procedure covers every case.
- A ruleset on the base branch may require a second human approval after the last push (`gh api repos/<org>/<repo>/rules/branches/<base>`); pull requests authored by the user's own account cannot get it from that account, and `gh pr merge` answers "the base branch policy prohibits the merge". The same holds when the integrator pushed with the user's account to another author's pull request: the approval of the account that pushed last does not count. Ask the user: admin bypass, a colleague's approval, or a ruleset change. Never bypass without that answer. The bypass is `-F bypass_rules=true` on the merge-async call (it bypasses only the rules the token's account may bypass, and works for stacked pull requests, so unstacking is no longer needed for it); with `gh pr merge` it is `--admin`.
- Branch after merge: when the user says so ("Remove branch if it's safe"), delete the remote branch ONLY when the PR state is MERGED and `gh pr list --state open --base <branch>` is EMPTY. Deleting a branch that an open pull request targets makes GitHub CLOSE that pull request (GitHub does not retarget on an API ref delete). Recovery: recreate the ref at its last sha (`gh api -X POST repos/<o>/<r>/git/refs -f ref=refs/heads/<b> -f sha=<sha>`), `gh pr reopen <n>`, `gh pr edit <n> --base main`, then delete again. Correct order for a stack: retarget the dependents to main FIRST (`gh pr edit <dep> --base main`), then delete. Make the delete command gate on the dependent count in the same shell (`if [ "$dep" = "0" ]`), never print-and-delete. Keep local worktrees and branches until route close. Merge dependent pull requests in order; a pull request whose Known gaps say "rebase after PR n merges" gets that rebase from its implementer first.
- Scope is the route's pull requests only. Older open pull requests without a local review are not covered; ask.
- After each merge: run-state line, memory, tell the user in the reply. Report the final table (number, branch, head, checks, verdict, merged commit).
