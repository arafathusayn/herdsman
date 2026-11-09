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
BUF               := github.com/bufbuild/buf/cmd/buf@v1.75.0

BUN_GLOBAL  := $(or $(BUN_INSTALL),$(HOME)/.bun)/install/global/node_modules
WEB_DIST    := internal/web/dist

# The test file names bun test runs. check-sdk-ts passes without running Bun
# only when no such file exists.
BUN_TEST_NAMES := $(foreach kind,.test _test .spec _spec,$(foreach ext,js jsx ts tsx mjs mts cjs cts,-o -name '*$(kind).$(ext)'))

# One target per module, in the order MODULES.md lists them.
MODULE_CHECKS := $(shell sed -n 's/^|.*| `make \(check-[a-z-]*\)` |.*/\1/p' MODULES.md)

# The Go module directories, from the same manifest.
GO_MODULES := $(shell sed -n 's/^|[^|]*| `\([^`]*\)` | Go |.*/\1/p' MODULES.md)

# The module proxy of the maintenance mode, the only mode that uses one.
DEPS_GOPROXY ?= https://proxy.golang.org,direct

# The repository mode, for every target and whatever the caller's environment.
# The maintenance targets (tools-go, vendor, deps-verify) leave it explicitly.
export GOFLAGS := -mod=vendor
export GOPROXY := off

# The pinned Go tools, installed by tools-go.
TOOLS_BIN ?= $(CURDIR)/.tools/bin

# Where make docs keeps the spec output it renders; ignored by git.
DOCS_INPUTS := $(CURDIR)/.tools/docs

# Where proto-check regenerates the SDK to compare it; ignored by git, and
# overwritten on each run. Contracts only grow, so no file there goes stale.
PROTO_CHECK_DIR := $(CURDIR)/.tools/proto-check

# need-tool stops with the step to run when a pinned tool is not installed.
define need-tool
	@test -x $(TOOLS_BIN)/$(1) || { echo "$(1) is not installed: run make tools-go"; exit 1; }
endef

.PHONY: docs acceptance proto proto-check proto-plugin tools-bun tools-go all tools web pages web-check web-e2e web-fmt build check check-root check-sdk check-conformance check-sdk-ts check-web guard-report test lint lint-fmt vuln vendor deps-verify release-check clean

all: check build

## tools: every pinned tool
tools: tools-bun tools-go

## tools-bun: install bun-types globally (outside the repo) for `bun check`
tools-bun:
	bun add -g bun-types@$(BUN_TYPES_VERSION)

## tools-go: install the pinned Go tools into .tools/bin (maintenance mode; uses
## the network)
tools-go:
	@for tool in $(STATICCHECK) $(GOVULNCHECK) $(GORELEASER) $(BUF); do \
		env -u GOFLAGS GOWORK=off GOPROXY=$(DEPS_GOPROXY) GOBIN=$(TOOLS_BIN) go install $$tool || exit 1; \
	done

## docs: the generated reference pages, from what the binary says about itself
docs:
	@mkdir -p $(DOCS_INPUTS)
	CGO_ENABLED=1 go build -trimpath -o $(DOCS_INPUTS)/herdsman ./cmd/herdsman
	$(DOCS_INPUTS)/herdsman spec cli > $(DOCS_INPUTS)/cli.json
	$(DOCS_INPUTS)/herdsman spec descriptor > $(DOCS_INPUTS)/descriptor.bin
	$(DOCS_INPUTS)/herdsman spec proto > $(DOCS_INPUTS)/contracts.proto
	go run ./cmd/herdsman-docs --cli $(DOCS_INPUTS)/cli.json --descriptor $(DOCS_INPUTS)/descriptor.bin --proto $(DOCS_INPUTS)/contracts.proto --out docs
	HERDSMAN_WRITE_MAN=1 go test -count=1 ./cmd/herdsman -run TestManPagesAreCurrent

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
	cd web && bun check --no-pretty -p ../bench/web/herdsman-ui-cases/legacy/tsconfig.json --typeRoots "$(BUN_GLOBAL)"
	cd web && bun test
	cd web && bun test ../bench/web/herdsman-ui-cases/legacy

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

## lint-fmt: gofmt on the repository's own files; pinned copies stay as upstream
## wrote them, or their sums would no longer match
lint-fmt:
	@bad="$$(find . -name '*.go' -not -path './vendor/*' -not -path './third_party/*' -exec gofmt -l {} +)"; \
	test -z "$$bad" || { echo "$$bad"; echo "run gofmt -w on the files above"; exit 1; }

## lint: gofmt, go vet and staticcheck
lint: lint-fmt
	$(call need-tool,staticcheck)
	go vet ./...
	$(TOOLS_BIN)/staticcheck ./...

## vuln: known vulnerabilities, the one online step of the checks: the code paths
## herdsman uses, then OSV on every pin, because a local replace hides a module
## from govulncheck
vuln:
	$(call need-tool,govulncheck)
	$(TOOLS_BIN)/govulncheck ./...
	bash .github/scripts/vendor-advisories.sh .

## acceptance: the real-platform gates, one suite; a capability requirements.md
## declares for this platform must work, and only one declared absent skips
acceptance:
	go run ./acceptance

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

## guard-report: the import guard, the dependency budget and the offline
## compile, with -v, so every allowlisted file and upstream shows in each run
guard-report:
	go test -count=1 -v -run '^(TestImportGuard|TestDependencyBudget|TestNoNetworkBuildFromEveryModuleRoot)$$' ./internal/guard

# go-module-check runs format, vet, staticcheck and tests from a module's root.
define go-module-check
	@cd $(1) && test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run gofmt -w $(1)"; exit 1; }
	$(call need-tool,staticcheck)
	cd $(1) && go vet ./...
	cd $(1) && $(TOOLS_BIN)/staticcheck ./...
	cd $(1) && go test -race -count=1 ./...
endef

## check-sdk: the SDK module, then its generated code proven current
check-sdk:
	$(call go-module-check,sdk)
	@$(MAKE) --no-print-directory proto-check

## check-conformance: the conformance module
check-conformance:
	$(call go-module-check,sdk/conformance)

## check-sdk-ts: the TypeScript SDK's strict type check, tests and build
check-sdk-ts:
	@if [ -n "$$(find sdk/ts -type f ! -path '*/node_modules/*' \( -false $(BUN_TEST_NAMES) \) 2>/dev/null)" ]; then \
		cd sdk/ts && bun check --no-pretty --typeRoots "$(BUN_GLOBAL)" && bun test && bun run build.ts; \
	else \
		echo "sdk/ts: no test files yet"; \
	fi

## check-web: format check, type check and tests of the web app
check-web: web-check

## vendor: rewrite vendor/ from the workspace (maintenance mode), then restore
## the VENDOR.md files the tool clears
vendor:
	env -u GOFLAGS GOPROXY=$(DEPS_GOPROXY) go work vendor
	@find third_party/vendored -name VENDOR.md | while read -r file; do \
		cp "$$file" "vendor/$${file#third_party/vendored/}" || exit 1; \
	done

## deps-verify: every module tidy, and a fresh re-vendor changes nothing that
## is committed (maintenance mode; uses the network)
deps-verify:
	@test -n "$(GO_MODULES)" || { echo "no Go modules found in MODULES.md"; exit 1; }
	@for dir in $(GO_MODULES); do \
		echo "==> go mod tidy -diff in $$dir"; \
		(cd $$dir && env -u GOFLAGS GOPROXY=$(DEPS_GOPROXY) go mod tidy -diff) || exit 1; \
	done
	@$(MAKE) --no-print-directory vendor
	@changed="$$(git status --porcelain -- vendor go.work.sum go.sum sdk/go.sum sdk/conformance/go.sum)"; \
	test -z "$$changed" || { echo "$$changed"; echo "the committed tree differs from a re-vendor: run make vendor and commit"; exit 1; }

## proto-plugin: build the code generators from the vendored sources
proto-plugin:
	go build -o $(TOOLS_BIN)/protoc-gen-herdsman-go ./cmd/protoc-gen-herdsman-go
	go build -o $(TOOLS_BIN)/protoc-gen-herdsman-ts ./cmd/protoc-gen-herdsman-ts

## proto: regenerate the SDK's Go and TypeScript code and the two goldens
proto: proto-plugin
	$(call need-tool,buf)
	PATH="$(TOOLS_BIN):$$PATH" $(TOOLS_BIN)/buf generate
	go run ./cmd/herdsman spec descriptor > sdk/testdata/descriptor.golden.new
	mv sdk/testdata/descriptor.golden.new sdk/testdata/descriptor.golden
	go run ./cmd/herdsman spec cli > sdk/conformance/cli/testdata/spec.golden.json.new
	mv sdk/conformance/cli/testdata/spec.golden.json.new sdk/conformance/cli/testdata/spec.golden.json

## proto-check: lint, no breaking change, and generated code that matches the
## contracts; the descriptor golden is compared by the cmd/herdsman tests
proto-check: proto-plugin
	$(call need-tool,buf)
	$(TOOLS_BIN)/buf lint
	$(TOOLS_BIN)/buf breaking --against proto/baseline.binpb
	@mkdir -p $(PROTO_CHECK_DIR)
	@PATH="$(TOOLS_BIN):$$PATH" $(TOOLS_BIN)/buf generate -o $(PROTO_CHECK_DIR)
	@cd $(PROTO_CHECK_DIR)/sdk && find . -name '*.pb.go' -o -path './ts/src/generated/*.ts' | sort > $(PROTO_CHECK_DIR)/fresh
	@cd sdk && find . -name '*.pb.go' -o -path './ts/src/generated/*.ts' | sort > $(PROTO_CHECK_DIR)/committed
	@diff $(PROTO_CHECK_DIR)/fresh $(PROTO_CHECK_DIR)/committed || { echo "generated files differ: run make proto"; exit 1; }
	@while read -r file; do \
		cmp "$(PROTO_CHECK_DIR)/sdk/$$file" "sdk/$$file" || { echo "$$file is stale: run make proto"; exit 1; }; \
	done < $(PROTO_CHECK_DIR)/fresh

## release-check: validate the goreleaser config
release-check:
	$(call need-tool,goreleaser)
	$(TOOLS_BIN)/goreleaser check

clean:
	find $(WEB_DIST) -type f ! -name .gitkeep -delete
	rm -f bin/herdsman
