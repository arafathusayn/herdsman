# Third-party code

The Go code herdsman depends on is kept here at fixed versions, which lets compiling work offline.

| Location | What it holds | Who writes it |
| --- | --- | --- |
| `third_party/<name>/` | A whole upstream release, imported through a `replace` in `go.mod`, with a `VENDOR.md` | A person, from the upstream release |
| `vendor/` | The packages the build imports, for the whole Go workspace | `make vendor`, never by hand |
| `third_party/vendored/<module>/VENDOR.md` | The `VENDOR.md` of a module that lives only under `vendor/` | A person; `make vendor` copies it into `vendor/` |
| `VENDOR.template.md` | The fields every `VENDOR.md` carries | |

A module replaced into `third_party/` also appears under `vendor/`, because `go work vendor` copies replaced directories and the compiler reads `vendor/`. `make deps-verify` proves the two agree.

The list of upstreams is `dependencyBudget` in `internal/guard/budget_test.go`. Adding one is a decision: it needs an entry there, a `VENDOR.md`, and the steps in `MAINTAINING.md`.
