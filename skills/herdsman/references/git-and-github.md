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
- GitHub native stacks: link with `printf '{"pull_requests":[base,top]}' | gh api --method POST -H "X-GitHub-Api-Version: 2026-03-10" repos/O/R/stacks --input -` (integers; each base ref must equal the previous head ref; same repository). `.../stacks/N/unstack` removes. Dissolving a stack and retargeting a base is a GitHub mutation that needs the user's go unless the task file grants it.

## Review bots (Pullfrog seen)

- Reviews arrive within minutes of a push. Loop: fetch review body and inline comments, verify the claim, test-first fix, push, reply on the thread via `gh api repos/O/R/pulls/<n>/comments/<id>/replies` with "Addressed in <sha>".
- "No new issues found" in a review body means clean; the approval check flips to pass only then. The approval check attaches to the reviewed commit and never clears on thread resolution alone; post an issue comment mentioning the bot and asking for a fresh review of the current head.
- The bot keeps finding the next layer on concurrency and object-store code; design two-phase claims with unique tokens up front, no store I/O inside a database transaction, and race tests that are shown to fail on the old code and synchronise with barriers, not sleeps.
- Review rounds, not coding, take most of the time from implementation to an open green pull request.
- A pull request that belongs to a GitHub native stack cannot be merged with `gh pr merge` ("must be merged using the asynchronous merge REST API"). `PUT repos/O/R/pulls/N/merge-async` (commit_title, commit_message, sha, merge_method) enqueues the merge and `GET .../merge-async/<uuid>` reports pending, merged or failed; it does NOT carry an admin bypass, so a ruleset (approval, thread resolution) fails it. When the bypass is the agreed path: `POST repos/O/R/stacks/<stack number>/unstack` with the `X-GitHub-Api-Version: 2026-03-10` header, then `gh pr merge --squash --admin`.
- GitHub Actions can stop for the whole organization on a billing failure ("The job was not started because recent account payments have failed or your spending limit needs to be increased"); every job and the review bot then fail within seconds of scheduling. Only the organization owner fixes it; a re-run of a failed run is the cheapest probe. The local review stays valid; merging without CI is the user's call.
- Pullfrog runs on the same model account as the Codex implementers; it is a third-party bot, not a GitHub feature. When that account's usage limit is reached, Pullfrog's reviews and its approval check fail as well, so a merge gate that needs the bot cannot pass until the reset. Tell the user; the merge decision without the bot is theirs.

## Merges

Merges are the user's unless the interview (or a later instruction) authorizes the orchestrator. An authorized form: "Merge the PRs when both the local review and the bot reviewer on GH approve them. Use squash and merge technique."

- Gate, on the SAME head commit: latest `review-<pr>-<n>.md` says `VERDICT: pass`; the bot approval check is SUCCESS; every CI check green; `mergeable: MERGEABLE`. A push after the review invalidates the pass.
- Command: `GH_TOKEN=$(gh auth token --user <account>) gh pr merge <n> -R <org>/<repo> --squash --subject "<pr title> (#<n>)" --body-file <pr body file>`. Read the repo's merge settings first (`gh api repos/<org>/<repo> --jq '{allow_squash_merge,delete_branch_on_merge,squash_merge_commit_title,squash_merge_commit_message}'`); pass subject and body explicitly when the defaults would concatenate commit messages.
- A ruleset on the base branch may require a second human approval after the last push (`gh api repos/<org>/<repo>/rules/branches/<base>`); pull requests authored by the user's own account cannot get it from that account, and `gh pr merge` answers "the base branch policy prohibits the merge". The same holds when the integrator pushed with the user's account to another author's pull request: the approval of the account that pushed last does not count. Ask the user: admin bypass (`--admin`), a colleague's approval, or a ruleset change. Never bypass without that answer.
- Branch after merge: when the user says so ("Remove branch if it's safe"), delete the remote branch ONLY when the PR state is MERGED and `gh pr list --state open --base <branch>` is EMPTY. Deleting a branch that an open pull request targets makes GitHub CLOSE that pull request (GitHub does not retarget on an API ref delete). Recovery: recreate the ref at its last sha (`gh api -X POST repos/<o>/<r>/git/refs -f ref=refs/heads/<b> -f sha=<sha>`), `gh pr reopen <n>`, `gh pr edit <n> --base main`, then delete again. Correct order for a stack: retarget the dependents to main FIRST (`gh pr edit <dep> --base main`), then delete. Make the delete command gate on the dependent count in the same shell (`if [ "$dep" = "0" ]`), never print-and-delete. Keep local worktrees and branches until route close. Merge dependent pull requests in order; a pull request whose Known gaps say "rebase after PR n merges" gets that rebase from its implementer first.
- Scope is the route's pull requests only. Older open pull requests without a local review are not covered; ask.
- After each merge: run-state line, memory, tell the user in the reply. Report the final table (number, branch, head, checks, verdict, merged commit).
