# Dependencies of the conformance module

| Module | Used for | Ships in a binary |
| --- | --- | --- |
| `connectrpc.com/connect` | Tests only: a stock client that calls the unary binding, so compatibility is measured rather than claimed | No |

The module's pinned copy and its record live with the other upstreams, in `vendor/connectrpc.com/connect/`, and its source of truth is `third_party/vendored/connectrpc.com/connect/VENDOR.md`. The conformance module ships in no binary, so this dependency stays out of the release's software bill of materials.
