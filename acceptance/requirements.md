# Platform requirements of the acceptance suite

What each platform promises to `make acceptance`. The suite reads the table below when it starts.

- `provided`: the suite probes the capability before any gate that needs it. A failed probe fails the run.
- `absent`: gates that need the capability are skipped and reported by name, with the capability that caused it.
- A capability with no row is an error for any gate that needs it. No other condition may skip a gate, and a test that skips itself fails the run.

| Capability | darwin | linux | How it is checked |
| --- | --- | --- | --- |
| `ps` | provided | provided | Reads this process's start time, then its environment column (`-E` on macOS, `e` on Linux) |
| `lsof` | provided | provided | Lists this process's working directory |
| `signals` | provided | provided | Sends signal 0 to this process |
| `docker` | absent | provided | `docker info` answers and the local image `alpine:3` exists; the gate never pulls |
| `btrfs-loopback` | absent | provided | `mkfs.btrfs` and `losetup` exist, and the user is root or has passwordless `sudo` |
| `keychain` | provided | absent | `security list-keychains` answers on macOS; `secret-tool` exists on Linux |
| `pty` | provided | provided | `/dev/ptmx` opens |
| `herdr-core` | provided | absent | `hc --version` answers; a gate starts its own private core and never the user's |
| `herdr-app` | provided | absent | `herdr-app` is on the path; nothing runs it, so no window opens |

Docker is declared absent on macOS because the hosted macOS runners have no container engine.

herdr-core and its app are claimed for macOS on Apple silicon only, so both are declared absent on Linux, and on an Intel Mac both probes fail with that reason. The core gate runs the installed `hc` in a session of its own, with every XDG directory under a temporary folder, and stops it at the end. It never opens the app: app behaviour is proved with the fake app in `internal/runtime/core` and `internal/route`.

Gates run with a scratch `HOME` and no login tokens. Stand-ins for `gh`, `claude` and `codex` come first on `PATH` and record any call, and a recorded call fails the gate, so a result cannot depend on what the machine has installed or logged in to.

The separate package `acceptance/restricted` needs `git` with `clone --reference --dissociate` and `maintenance run`. Its folder sandbox gate also needs `codex` with its `sandbox` command, and is skipped by name when `codex` is not on the path.
