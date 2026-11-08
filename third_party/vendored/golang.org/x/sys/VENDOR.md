# golang.org/x/sys

Platform calls for `internal/daemon` and `internal/inbox`.

- Module: golang.org/x/sys
- Upstream: https://go.googlesource.com/sys
- Version: v0.49.0
- Commit: b6b8557e8e2974925aec7a136ba0e323eedc5f43
- License: BSD-3-Clause
- Sum: h1:p66HhBRQjSeMkUEDa4V5X3cWYxNtkz1CbArsoQ44RuQ=

`Sum` covers the vendored packages, leaving out this file; `TestDependencyBudget` recomputes it offline. `go work vendor` writes only the packages the build imports. This file's source is `third_party/vendored/golang.org/x/sys/VENDOR.md`; `make vendor` copies it back after each re-vendor, because the tool clears `vendor/`.

## Re-vendor

Follow "Re-vendor an upstream" in `MAINTAINING.md`; edit the file under `third_party/vendored/`, never the copy under `vendor/`.
