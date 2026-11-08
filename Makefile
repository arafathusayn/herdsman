# Developer tasks. Users never need any of this: they download one binary.
#
# Tools: Go (the version in go.mod's toolchain line) and Bun. Bun builds,
# type-checks, formats and tests the web app; it is not needed at run time.
# The web app uses Reactre's source submodule, with no app package.json or
# node_modules. Bun's own types come from a global install (`make tools`). Only its browser checks (web/e2e) have
# a package.json, for puppeteer-core, and they need Chrome or Chromium.

BUN_TYPES_VERSION := 1.4.4
STATICCHECK       := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK       := golang.org/x/vuln/cmd/govulncheck@v1.9.0
GORELEASER        := github.com/goreleaser/goreleaser/v2@v2.19.0

BUN_GLOBAL  := $(or $(BUN_INSTALL),$(HOME)/.bun)/install/global/node_modules
WEB_DIST    := internal/web/dist

# One target per module, in the order MODULES.md lists them.
MODULE_CHECKS := $(shell sed -n 's/^|.*| `make \(check-[a-z-]*\)` |.*/\1/p' MODULES.md)

.PHONY: all tools web pages web-check web-e2e web-fmt build check check-root check-sdk check-conformance check-sdk-ts check-web guard-report test lint vuln release-check clean

all: check build

## tools: install bun-types globally (outside the repo) for `bun check`
tools:
	bun add -g bun-types@$(BUN_TYPES_VERSION)

## web: build the web app into internal/web/dist for go:embed
web:
	@test -f web/vendor/reactre/packages/core/src/index.ts || { echo "run git submodule update --init web/vendor/reactre"; exit 1; }
	find $(WEB_DIST) -type f ! -name .gitkeep -delete
	cd web && bun run build.ts

## pages: the web app as a static site (Cloudflare Pages runs this), with its
## security headers
pages: web
	cp web/public/_headers $(WEB_DIST)/

## web-check: format check, type check and tests of the web app
web-check:
	cd web && bun fmt --check
	cd web && bun check --no-pretty --typeRoots "$(BUN_GLOBAL)"
	cd web && bun check --no-pretty -p tsconfig.strict.json --typeRoots "$(BUN_GLOBAL)"
	cd web && bun check --no-pretty -p ../bench/herdsman-cases/legacy/tsconfig.json --typeRoots "$(BUN_GLOBAL)"
	cd web && bun test
	cd web && bun test ../bench/herdsman-cases/legacy

## web-e2e: the built app and its forms in headless Chrome (CHROME=path picks
## the browser)
web-e2e: web
	cd web/e2e && bun install --frozen-lockfile
	cd web && bun check --no-pretty -p e2e/tsconfig.json --typeRoots "$(BUN_GLOBAL)"
	cd web/e2e && bun run e2e

## web-fmt: format the web app
web-fmt:
	cd web && bun fmt

## build: the herdsman binary with the web app inside, into bin/ (cgo: the
## daemon store links the SQLite driver)
build: web
	CGO_ENABLED=1 go build -trimpath -o bin/herdsman ./cmd/herdsman

## test: Go tests with the race detector
test:
	go test -race -count=1 ./...

## lint: gofmt, go vet and staticcheck
lint:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w ."; exit 1; }
	go vet ./...
	go run $(STATICCHECK) ./...

## vuln: known vulnerabilities in the code paths herdsman uses
vuln:
	go run $(GOVULNCHECK) ./...

## check: every module in MODULES.md, one after another; every module runs, and
## the target fails when any failed
check:
	@test -n "$(MODULE_CHECKS)" || { echo "no modules found in MODULES.md"; exit 1; }
	@failed=""; \
	for target in $(MODULE_CHECKS); do \
		echo "==> $$target"; \
		$(MAKE) --no-print-directory $$target || failed="$$failed $$target"; \
	done; \
	if [ -n "$$failed" ]; then echo "failed:$$failed"; exit 1; fi; \
	echo "all modules passed"

## check-root: the root module, with the web app built for go:embed
check-root: web lint test guard-report vuln

## guard-report: the import guard with -v, so every allowlisted file it skipped
## shows in each run
guard-report:
	go test -count=1 -v -run '^TestImportGuard$$' ./internal/guard

# go-module-check runs format, vet, staticcheck and tests from a module's root.
define go-module-check
	@cd $(1) && test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w $(1)"; exit 1; }
	cd $(1) && go vet ./...
	cd $(1) && go run $(STATICCHECK) ./...
	cd $(1) && go test -race -count=1 ./...
endef

## check-sdk: the SDK module
check-sdk:
	$(call go-module-check,sdk)

## check-conformance: the conformance module
check-conformance:
	$(call go-module-check,sdk/conformance)

## check-sdk-ts: the TypeScript SDK; passes while the module has no test files
check-sdk-ts:
	@if [ -n "$$(find sdk/ts -type f ! -path '*/node_modules/*' \( -name '*.test.*' -o -name '*_test.*' -o -name '*.spec.*' -o -name '*_spec.*' \) 2>/dev/null)" ]; then \
		cd sdk/ts && bun test; \
	else \
		echo "sdk/ts: no test files yet"; \
	fi

## check-web: format check, type check and tests of the web app
check-web: web-check

## release-check: validate the goreleaser config
release-check:
	go run $(GORELEASER) check

clean:
	find $(WEB_DIST) -type f ! -name .gitkeep -delete
	rm -f bin/herdsman
