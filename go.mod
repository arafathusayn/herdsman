module github.com/arafathusayn/herdsman

go 1.26.0

toolchain go1.27.3

require (
	github.com/mattn/go-sqlite3 v1.14.52
	golang.org/x/sys v0.49.0
	rsc.io/qr v0.2.0
)

require github.com/arafathusayn/herdsman/sdk v0.0.0

replace github.com/arafathusayn/herdsman/sdk => ./sdk

replace github.com/mattn/go-sqlite3 => ./third_party/go-sqlite3

replace rsc.io/qr => ./third_party/qr
