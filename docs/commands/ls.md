# herdsman ls

List the repository's worktrees.

```text
usage: herdsman ls [--full] [--all] [--json]
       herdsman ls --statusline [--format text|json|claude-code]

Prints one row per worktree of the repository. --all adds branches without a
tree, --full adds the commit and path. The JSON form is for scripts:

  herdsman ls --json | jq -r '.rows[] | select(.status != "") | .task'

exit codes:
  0  printed
  1  the table could not be read
  2  bad usage
```
