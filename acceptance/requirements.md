# Platform requirements of the acceptance suite

What a machine must provide for `make acceptance` to mean anything. Each gate names the entries it depends on, and skips with a named reason when an entry is missing, never silently passes.

| Capability | Needed by | How it is checked |
| --- | --- | --- |
| `ps` with `-o lstart=` and the environment column (`-E` on macOS, `e` on Linux) | `teardown` | The gate reads its own child's start time and argv |
| `lsof` with `-d cwd` and `-iTCP` state filters | `teardown` | The gate sees its own child's listener and connection |
| Signals to a process and a process group of the current user | `teardown` | The gate stops children it started |
| A container engine (`docker`, or `podman`) running with `compose` | `teardown`, the container gate only | Skipped when `docker info` fails |
| `git` with `clone --reference --dissociate` and `maintenance run` | `restricted` | The gate clones and runs maintenance on its own repository |
| `codex` with its `sandbox` command | `restricted`, the folder sandbox gate only | Skipped by name when `codex` is not on the path |
