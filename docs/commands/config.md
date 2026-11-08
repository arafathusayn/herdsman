# herdsman config

Read and change the configuration; install the shell integration.

```text
usage: herdsman config get <key> [-c <path>]
       herdsman config set <key> <value> [-c <path>]
       herdsman config unset <key> [-c <path>]
       herdsman config show [--strict] [-c <path>]
       herdsman config export --format toml|yaml|json|bin [-c <path>]
       herdsman config update [-c <path>]
       herdsman config init [--format toml|yaml|json|bin] [-c <path>]
       herdsman config schema --format jsonschema|descriptor
       herdsman config approvals list
       herdsman config approvals clear [<project>]
       herdsman config shell install|uninstall [bash|zsh|fish] [--json]

Keys are dotted paths such as merge.strategy; a kebab-case spelling is the same
key. -c names a file; otherwise HERDSMAN_CONFIG, then the repository's parent
folder, then the repository root.

show prints the file in use and then one "key = value (source)" line each.
A warning goes to stderr; --strict makes it an error. An unknown key in the
policy or launch section always stops the command.

shell adds or removes the wrapper that lets herdsman go change the shell's
folder, with static completions. The shell defaults to $SHELL.

exit codes:
  0  done
  1  the configuration is invalid, or the file cannot be read or written
  2  usage error
```

## Notes

- With no file, `show` prints `file: none` and every value with the source `(default)`. Values read from a file show `(project)`.
- `get` on an unknown key exits 1. An unknown key in a file is only a warning for `show`, and exit 1 with `--strict`.
- `init` writes `herdsman.config.toml` by default, in the repository's parent folder. `herdsman new` stops on that file: it reads project hooks only from `herdsman.config.json`. Use `herdsman config init --format json` in a repository that runs `herdsman new`.
- `herdsman new` does not follow `branch.name` or `worktree.path` yet.
