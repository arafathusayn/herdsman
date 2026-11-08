# github.com/mattn/go-sqlite3

The SQLite driver and the SQLite amalgamation it embeds, used by the daemon's store and `internal/routedb`.

- Module: github.com/mattn/go-sqlite3
- Upstream: https://github.com/mattn/go-sqlite3
- Version: v1.14.52
- Commit: b0be46fa28d17ee0b65c79774ac0dad84b6db068
- License: MIT (the driver); the SQLite amalgamation is in the public domain
- Sum: h1:wVbm2Qnf4OXkqhBTSPuCRZDRnxfbVrrmiCEroVdog8U=

The copy is the upstream release, byte for byte. `Sum` is the release's `go.sum` hash; `TestDependencyBudget` recomputes it offline, leaving out this file.

## Re-vendor

Follow "Re-vendor an upstream" in `MAINTAINING.md`, with `third_party/go-sqlite3` as the directory.
