# herdsman teardown

Stop what a task runs, keeping what it holds.

```text
usage: herdsman teardown [<task>|<selector>] [--route <folder>|--all] [-y]
                         [--grace <seconds>] [--include associated] [--json]

Stops what a task runs and keeps everything it holds: it prints what it would do, asks,
then signals only what the task exclusively owns, checking each one again just
before its signal. Nothing is removed.

exit codes:
  0  everything approved was stopped
  1  a plan stopped because the machine changed, or a stop failed
  2  bad usage, or no terminal to ask on and no -y
```

## Notes

- Exit code 0 with `nothing to stop` does not mean nothing is running. Read the table: a row whose action is `report` is still running, because herdsman could not prove the task alone owns it.
- The worktree and the branch stay.
