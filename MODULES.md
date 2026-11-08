# Modules

The repository's modules and the command that checks each one. `make check` reads the table below and runs every target, one after another; it runs all of them and fails when any fails. Add a module by adding a row and its target.

Go's `./...` stops at a module boundary, so each Go module is checked from its own root. `go.work` lists the three Go modules.

| Module | Path | Kind | Check | What the check runs |
| --- | --- | --- | --- | --- |
| root | `.` | Go | `make check-root` | The web app build for `go:embed`, `gofmt`, `go vet`, `staticcheck`, tests with the race detector, the import guard report, `govulncheck` and the OSV query of every pin |
| sdk | `sdk` | Go | `make check-sdk` | `gofmt`, `go vet`, `staticcheck`, tests with the race detector |
| conformance | `sdk/conformance` | Go | `make check-conformance` | `gofmt`, `go vet`, `staticcheck`, tests with the race detector |
| sdk-ts | `sdk/ts` | Bun | `make check-sdk-ts` | The strict `bun check`, `bun test`, the `build.ts` bundle; passes while the module has no test files |
| web | `web` | Bun | `make check-web` | `bun fmt --check`, the type checks, `bun test` |
