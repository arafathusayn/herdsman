module github.com/arafathusayn/herdsman/sdk/conformance

go 1.26.0

require (
	connectrpc.com/connect v1.21.1
	github.com/arafathusayn/herdsman/sdk v0.0.0
)

require google.golang.org/protobuf v1.36.12 // indirect

replace github.com/arafathusayn/herdsman/sdk => ../
