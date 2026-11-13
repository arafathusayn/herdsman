# SDK

Everything the `herdsman` command and the web app do goes through contracts defined in Protocol Buffers. You can use the same contracts from your own program.

## Reading the contracts

The binary prints what it was built with, so these always match the version you have:

| Command | Output |
| --- | --- |
| `herdsman spec cli` | Every command, flag and exit code, as JSON |
| `herdsman spec proto` | The source of every contract |
| `herdsman spec descriptor` | A serialized `FileDescriptorSet`, for generating code in another language |
| `herdsman config schema --format jsonschema` | A JSON Schema for the configuration file |

## Go

The module is `github.com/arafathusayn/herdsman/sdk`. `client.Dial` finds the daemon's socket the way the `herdsman` command does, and the returned client holds one field per service.

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

func main() {
	daemon, err := client.Dial(client.Options{})
	if err != nil {
		log.Fatal(err)
	}
	status, err := daemon.Message.DaemonStatus(context.Background(), &daemonv1.DaemonStatusRequest{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(status)
}
```

`client.Options` can name the socket and add headers, for every call or per call. Failures are typed by the `sdk/rpc` package.

## TypeScript

The package `@herdsman/sdk` lives in `sdk/ts`. It runs on plain `fetch` and JSON: `createClient(service, options)` takes a generated service table from `sdk/ts/src/generated` and a `baseUrl`, and returns one typed function per method. A failed call throws a `HerdsmanError` carrying its error code. Under Bun, pass `init: { unix: path }` to reach a Unix socket. `openStream` opens the frame stream.

## Compatibility

A released contract only grows; see [ADR 0003](adr/0003-public-interfaces-only-ever-grow.md). `sdk/version` holds the contract version, which `spec cli` also reports. The exceptions are listed under [experimental surfaces](experimental.md).

## Mutation previews

Implemented MessageService mutations and route lifecycle methods accept `dry_run` (`DryRun` in Go, `dryRun` in TypeScript). They return a plan without performing the requested change. A plan describes current admission checks; it does not reserve the state or guarantee a later write. Runtime `ResumeGoal`, configuration edits, project/task records, plugins, suggestions and ledger mutations do not advertise previews until their handlers implement them.

The Go SDK, TypeScript SDK and MCP client send previews with `application/vnd.herdsman.preview+proto` or `application/vnd.herdsman.preview+json`. The payload remains ordinary protobuf or protobuf JSON and the response keeps its standard encoding. These media types are required for safe previews: an older daemon rejects them before calling a handler, instead of ignoring an unknown `dry_run` field and executing the write. Raw HTTP or stock Connect clients must select the preview media type explicitly. Never retry an unsupported preview using the ordinary media type.

A daemon also rejects preview encoding when the decoded method has no true `dry_run` field. MCP uses the schema spelling `dry_run` and refuses unsupported previews locally. Message previews retain caller authentication, admission and boundary checks; a refusal does not write stale-proof latches or exposure records. Real delivery still writes those safety records.
