# connectrpc.com/connect

A stock Connect client for the conformance module's tests. It ships in no binary.

- Module: connectrpc.com/connect
- Upstream: https://github.com/connectrpc/connect-go
- Version: v1.21.1
- Commit: c4ad2c43b27f9614d1ec130bad99ac8cc35f6130
- License: Apache-2.0
- Sum: h1:HV0S3jFNToD+E4sEdhnwwq8tg/LbLy6scoyQYxpX6o8=

`Sum` covers the vendored packages, leaving out this file; `TestDependencyBudget` recomputes it offline. `go work vendor` writes only the packages the build imports. This file's source is `third_party/vendored/connectrpc.com/connect/VENDOR.md`; `make vendor` copies it back after each re-vendor, because the tool clears `vendor/`.

## Re-vendor

Follow "Re-vendor an upstream" in `MAINTAINING.md`; edit the file under `third_party/vendored/`, never the copy under `vendor/`.
