# Developer tasks. Users never need any of this: they download one binary.
#
# Tools: Go (the version in go.mod's toolchain line) and Bun. Bun builds,
# type-checks, formats and tests the web app; it is not needed at run time.
# The web app has no package.json and no node_modules: Bun's own types come
# from a global install (`make tools`).

BUN_TYPES_VERSION := 1.4.4
STATICCHECK       := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK       := golang.org/x/vuln/cmd/govulncheck@v1.9.0
GORELEASER        := github.com/goreleaser/goreleaser/v2@v2.19.0

BUN_GLOBAL  := $(or $(BUN_INSTALL),$(HOME)/.bun)/install/global/node_modules
WEB_DIST    := internal/web/dist
# Two pages: the app at / and the docs at /docs/. Names keep their folder, so
# both pages can be index.html; the app's files keep the names they had.
BUN_BUILD   := bun build ./index.html ./docs/index.html --outdir ../$(WEB_DIST) --minify \
               --entry-naming '[dir]/[name].[ext]' --asset-naming '[name].[ext]' --chunk-naming '[dir]/[name].[ext]'

.PHONY: all tools web pages web-check web-fmt build check test lint vuln release-check clean

all: check build

## tools: install bun-types globally (outside the repo) for `bun check`
tools:
	bun add -g bun-types@$(BUN_TYPES_VERSION)

## web: build the web app into internal/web/dist for go:embed
web:
	find $(WEB_DIST) -type f ! -name .gitkeep -delete
	cd web && $(BUN_BUILD)

## pages: the web app as a static site (Cloudflare Pages runs this), with its
## security headers
pages: web
	cp web/public/_headers $(WEB_DIST)/

## web-check: format check, type check and tests of the web app
web-check:
	cd web && bun fmt --check
	cd web && bun check --no-pretty --typeRoots "$(BUN_GLOBAL)"
	cd web && bun test

## web-fmt: format the web app
web-fmt:
	cd web && bun fmt

## build: the herdsman binary with the web app inside, into bin/
build: web
	CGO_ENABLED=0 go build -trimpath -o bin/herdsman ./cmd/herdsman

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

## check: everything CI runs
check: web-check web lint test vuln

## release-check: validate the goreleaser config
release-check:
	go run $(GORELEASER) check

clean:
	find $(WEB_DIST) -type f ! -name .gitkeep -delete
	rm -f bin/herdsman
