# OXC parser (build only)

Parses TypeScript for the vendored `@reactre/state` compiler. No OXC code or
native binding is included in the browser bundle or Herdsman executable.

- Module: oxc-parser
- Upstream: https://github.com/oxc-project/oxc
- Version: 0.156.0
- Commit: 75efdca4747c4ad9b252542743098d4894f8005b
- License: MIT
- Sum: See `provenance.json` for each original npm tarball's SHA-512 integrity and `sha256.json` for the extracted tree.

The commit is the resolved source dependency in the npm SLSA provenance for
`oxc-parser@0.156.0`, retrieved from
https://registry.npmjs.org/-/npm/v1/attestations/oxc-parser@0.156.0.
The release contains generated JavaScript/types and native bindings, preserved
byte for byte. The supplied upstream LICENSE files are retained; the root LICENSE also covers the native bindings, whose npm packages declare MIT but omit the license text. Supported build hosts
are macOS/Linux, arm64/x64 (Linux glibc and musl). This is independent of the Go
release's cross-compilation targets.

The only patches are to install metadata: remove development dependencies and
scripts (we use the published build), and restrict optional native packages to
the six supported build hosts. Original manifests are `package.upstream.json`.
`web/package.json` overrides every remaining dependency to its local copy;
`bun install --offline --frozen-lockfile` needs neither the registry nor a warm cache.
The build dependency budget and tree integrity are checked in `web/build.test.ts`.

## Re-vendor

1. Review the new Reactre compiler's required parser version and npm release provenance.
2. Update all eight pins in `provenance.json` with exact registry tarball URLs and SHA-512 integrity; update the source commit above.
3. Run `python3 web/vendor-oxc.py --output /tmp/oxc-reviewed`. It verifies every archive before extracting and applies only the manifest patches above. Review the generated tree and replace the eight package directories and `sha256.json` together. A same-version reproduction may use `python3 web/vendor-oxc.py` directly.
4. Update the build dependency budget, regenerate `web/bun.lock` with `cd web && bun install --offline`, and run `make web-check web-e2e build`.
5. Run `.github/scripts/vendor-advisories.sh .`. The existing daily advisory workflow checks these npm pins too.
