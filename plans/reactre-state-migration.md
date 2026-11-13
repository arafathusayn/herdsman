# Reactre state migration

Upstream: Reactre main, `8bae29b9fce00ab1e192f906e15191c6057550d7` (merged state package). Keep the Git submodule, upstream license and source build; add only the pinned parser needed by the state compiler.

## Implementation sequence

1. Integrate state compiler and source exports into both production build and test preload.
2. Move shared app/navigation, local controls, preferences, docs, dialogs and view data to actor ownership. Keep DOM nodes, observers, callback implementations and disposers outside snapshots. SDK data and collections cross a lossless data-only JSON boundary because upstream contracts reject optional fields, index signatures, Maps and Sets.
3. Implement explicit workflow machines: asynchronous resources and commands; pairing; route setup and submission; terminal input/polling; stream connection. Use guards, stale-result rejection, cancellation and cleanup.
4. Audit remaining mutable fields, persistence, view/controller lifetimes and error paths. Document statecharts. Test transitions and representative UI workflows; run format, types, tests and build.
5. Review diff, commit, open PR and monitor latest-head checks and reviews. Do not merge.
6. Add one combined core/state entry to the existing herdsman-cases benchmark, preserve its contract and competitors, profile unnecessary work, and publish the full measured comparison with its A/A noise floor.

## State ownership inventory

- Shared app: server snapshots, event history, selected page/route/tab/file/pane, theme, channel and connection (`app-state`, `store`, `main`).
- Browser session: host/device/pair token, startup pairing, stream reconnect and HTTP caches (`api`, `main`, `sdk`). Client objects and transport handles are resources.
- Route views: document cache/search/groups/raw mode; diff polling/layout; artifact listing/opening; message expansion/body cache; run-state pagination; team grouping.
- Route wizard: recommended/previous selection, catalog probing, draft/validation, GitHub account probe, inbox polling, submission and partial failure.
- Terminal: input queue, poll state, mode, zoom, measurements, compose/send status, scrolling and stale mode responses.
- Pages: SDK resource loading/reload, projects/tasks mutations, suggestion search/body/add, dry-run preview and resource lookup.
- Local controls: hosts dialog/rename, appearance, palette query/cursor/prompt, sidebar, resize, tooltips, toasts, docs navigation/search/copy/tabs.
- Persistence: URL, localStorage and sessionStorage are initialization/output boundaries; actors own current values.

## Validation log

- `make web-check`: formatting, SDK and state ownership guards, normal/strict types (including the combined benchmark entry), 318 app tests and 82 legacy tests passed.
- `make web-e2e`: 309 Chrome checks passed, including tab exit/reopen, pairing, dialogs, route submission, terminal input, artifacts, docs and large-load responsiveness regressions.
- `make build`, Go race tests, Go vet/staticcheck and repository guards passed. The ADR guard records the new decision explicitly.
- OXC reproduced from eight verified npm archives with identical file hashes. Installation passed with an empty cache and network disabled. OSV reported no advisories for the vendored Go/npm pins.
- Architecture, tradeoffs, ownership inventory and workflow diagrams: [ADR 0032](../docs/adr/0032-actor-owned-web-state.md). CI and review readiness must be verified on the PR's latest head separately.
- Profiled and removed repeated whole-application serialization, terminal palette invalidation and per-run ANSI teardown overhead. [Measured results and reproduction](../web/e2e/PERFORMANCE.md).
- CI identified GO-2026-6859 in the existing Go 1.27.3 pin. Updated the module/workspace toolchain to 1.27.4; the local vulnerability gate passes without exemptions.
