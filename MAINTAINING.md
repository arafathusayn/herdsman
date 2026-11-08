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

## Releases

A pushed `v*` tag runs `.github/workflows/release.yml`. `.github/scripts/release-tag.sh` builds the tag twice from empty caches and compares `checksums.txt`; a difference stops the run before anything is public. The second build's files are then attested and uploaded unchanged, and the Homebrew cask is pushed last. Nothing is built after the comparison.

What a release carries:

- `checksums.txt`, for every archive and the SBOM.
- `checksums.txt.sig`, one line: the scheme, the id of the key, the signature. `herdsman update` checks it against the keys in `internal/update/release_keys.txt`, then the archive against the checksums, and only then replaces the binary.
- `herdsman_<version>.spdx.json`, written by `internal/update/releasetool`: every `VENDOR.md` under `vendor/` and `third_party/`, the SQLite inside the driver, each submodule, the Go standard library, and the musl and compiler runtime that zig links into the linux binaries. A folder named `conformance` holds test-only code and is left out.
- A provenance attestation; check it with `gh attestation verify <file> --repo arafathusayn/herdsman`.

### The signing key

The private key is the repository secret `HERDSMAN_RELEASE_SIGNING_KEY`, and it exists nowhere else. The public half is committed, one `ed25519:<base64>` line per key. The release stops when the secret is absent or when its public half is not in that file, so no release can ship that its own binaries would refuse.

To make or replace a key, on a machine you trust:

1. `go run ./internal/update/releasetool keygen --seed-out <file> --public-out <file>`.
2. Add the public line to `internal/update/release_keys.txt`, keeping any old line, and merge that.
3. Store the seed as the secret, then destroy the file.
4. Once every supported binary trusts the new key, remove the old line.

If a key leaks, remove its line at once and publish a release signed by a new key. Binaries that still trust only the old key must be replaced by hand; say so in the advisory.

### Man pages

`docs/man/` is generated from the command table. `TestManPagesAreCurrent` fails when a command's help changes without it; regenerate with `HERDSMAN_WRITE_MAN=1 go test ./cmd/herdsman -run TestManPagesAreCurrent`.

### Fuzzing

`.github/workflows/fuzz.yml` runs every native fuzz target each night, starting from the corpus under each package's `testdata/fuzz/`. A failure uploads the input that broke it: fix the bug and commit the input with the fix, so the corpus only grows. For a long session, run `.github/scripts/fuzz-nightly.sh <time per target> . sdk`.

## The yearly routine

Once a year, open an issue from the "Yearly routine" template. Its checklist should take an afternoon: replace the release keys, reread `docs/threat-model.md`, move to a newer Go toolchain, refresh every upstream copy, leave the fuzz targets going for a long session, and repeat the benchmarks.
