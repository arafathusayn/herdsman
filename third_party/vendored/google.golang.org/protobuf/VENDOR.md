# google.golang.org/protobuf

The SDK's messages and their encodings, and the code generator's library.

- Module: google.golang.org/protobuf
- Upstream: https://go.googlesource.com/protobuf
- Version: v1.36.12
- Commit: cdd4c5f7406e82462949c7a65defa9f3029c162d
- License: BSD-3-Clause
- Sum: h1:z83M+Mswu/oaxvB7aZ3JbLoF4JK0a4d0kGIiRkrhWOI=

`Sum` covers the vendored packages, leaving out this file; `TestDependencyBudget` recomputes it offline. `go work vendor` writes only the packages the build imports. This file's source is `third_party/vendored/google.golang.org/protobuf/VENDOR.md`; `make vendor` copies it back after each re-vendor, because the tool clears `vendor/`.

## Re-vendor

Follow "Re-vendor an upstream" in `MAINTAINING.md`; edit the file under `third_party/vendored/`, never the copy under `vendor/`.
