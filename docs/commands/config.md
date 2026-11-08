# herdsman config

Install the shell integration.

```text
usage: herdsman config shell install|uninstall [bash|zsh|fish] [--json]

Adds or removes the wrapper that lets herdsman go change the shell's folder,
with static completions. The shell defaults to $SHELL.

exit codes:
  0  done
  1  the startup file could not be changed, or the shell is not supported
  2  bad usage
```
