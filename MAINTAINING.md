# Maintaining herdsman

## Build modes

Every Go command runs in one of three modes, never a mix.

| Mode | Environment | Used by |
| --- | --- | --- |
| Repository | `GOPROXY=off`, `GOFLAGS=-mod=vendor` | Each `Makefile` target that builds or tests Go, including `make check`, and the release build |
| Maintenance | `GOFLAGS` unset, a module proxy | `make tools-go`, `make vendor`, `make deps-verify`, `go mod tidy` |
| External consumer | `GOWORK=off`, `GOFLAGS` empty | `.github/scripts/sdk-consumer.sh` |

The `Makefile` exports the repository mode for every target; the maintenance targets leave it explicitly.

`staticcheck`, `govulncheck` and `goreleaser` are build tools. `make tools-go` installs them at the versions the `Makefile` pins, into `.tools/bin`, in the maintenance mode. They ship in no binary. Run it once per checkout, and again when a pin changes.

## Re-vendor an upstream

The goal is a fix landed within a day of an upstream advisory. A bot's pull request is the signal; these steps are the change.

For a copy under `third_party/` (for example `third_party/qr`):

1. Download the release: `go mod download -json <module>@<version>`, which prints its `Dir` and `Sum`.
2. Replace the directory's contents with `Dir`'s, keeping `VENDOR.md`. Change nothing else: `TestDependencyBudget` hashes the copy.
3. Update `Version`, `Commit` and `Sum` in `VENDOR.md`, and the `require` line in every `go.mod` that has one.

For a module only under `vendor/` (for example `golang.org/x/sys`):

1. Update the `require` line in every `go.mod` that has one.
2. Update `Version` and `Commit` in `third_party/vendored/<module>/VENDOR.md`. After `make vendor` below, set `Sum` to the value `TestDependencyBudget` reports, then run `make vendor` again. For these modules `Sum` covers the vendored packages, not the whole release; `make deps-verify` proves they match upstream.

Then, for both:

1. `go mod tidy` in each module that changed.
2. `make vendor`.
3. `make deps-verify` on the commit, then `make check`.

## Add an upstream

A new upstream is a decision. Record it in `docs/adr/`, add it to `dependencyBudget` in `internal/guard/budget_test.go`, and write its `VENDOR.md` from `third_party/VENDOR.template.md`.

## Advisories

`make vuln` is the one online step of the checks, and it asks two sources because neither is enough alone. `govulncheck` follows the code paths, but a module behind a local `replace`, such as go-sqlite3 and qr, is invisible to it. `.github/scripts/vendor-advisories.sh` asks OSV about the version every `VENDOR.md` pins, so those modules stay covered. `check-root`, and through it `release.yml`, runs `make vuln`.

`.github/workflows/vuln.yml` runs daily. A failure says which module is affected and by which advisory; re-vendor a fixed release with the steps above.

## The yearly routine

Once a year, open an issue from the "Yearly routine" template. Its checklist should take an afternoon: replace the release keys, reread `docs/threat-model.md`, move to a newer Go toolchain, refresh every upstream copy, leave the fuzz targets going for a long session, and repeat the benchmarks.
