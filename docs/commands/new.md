# herdsman new

Make a task's branch and worktree.

```text
usage: herdsman new <task> [--base <ref>] [--from pr:<n>] [--type <type>] [--no-setup]
                    [--clobber] [--agent <command>] [--integrator] [--json] [-- <prompt>]

Makes the task's branch and worktree from the fetched base, then runs its hooks
and setup.

exit codes:
  0  the worktree is ready
  1  it could not be made, or a hook or the setup failed (the message says which)
  2  bad usage
```

## Notes

- Run it from inside the clone. The worktree goes to `<clone>.worktrees/<task>`, next to the clone.
- The branch is `<type>/<task>`, and `<type>` is `feat` unless `--type` names another.
- `--base` takes a branch name such as `main`. A remote-qualified name such as `origin/main` prints a warning that it could not be fetched and falls back to the local copy.
- Naming a task that already has a worktree fails with exit code 1 and changes nothing.
