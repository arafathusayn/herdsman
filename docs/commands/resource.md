# herdsman resource

Record a resource a task created: resource add.

```text
usage: herdsman resource add --kind <kind> --id <id> [--task <task>] [--execution <id>]

Records that the current execution created a resource, which is what lets
teardown stop it. A process is recorded with its start time and command line.
Run it from the worktree, or name the task.

exit codes:
  0  recorded
  1  it could not be recorded (the message says why)
  2  bad usage
```

## Notes

- It needs an execution to attach to. In a worktree that has none it fails with `task "<task>" has no execution yet`.
