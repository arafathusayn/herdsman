package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/mcp"
	"github.com/arafathusayn/herdsman/sdk/client"
	"github.com/arafathusayn/herdsman/sdk/config"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const mcpHelp = `usage: herdsman mcp [--socket <path>] [--config <file>] [--deny <tool>]...

Serves the daemon's services as MCP tools on stdin and stdout.

  --socket  the daemon's socket (default: found as other commands find it)
  --config  the configuration file (default: found as other commands find it)
  --deny    refuse a tool: a name, a name pattern, or service/method (repeatable)

The [policy] deny list of the configuration refuses tools in the same three
forms, and --deny adds to it. The configuration is read on every call; while it
cannot be read, every tool is refused.

Calls that act for a party identify it by HERDR_PANE_ID and HERDSMAN_NAME.

Exit codes:
  0  the client closed the connection
  1  serving failed
  2  bad usage
`

func init() {
	register(command{name: "mcp", summary: "Serve the daemon's services as MCP tools over stdio.", help: mcpHelp, run: runMCPCommand})
}

// mcpCaller reaches the daemon over its socket, in the binary form of the
// binding.
func mcpCaller(socket string) mcp.Caller {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		DisableCompression: true,
	}
	hc := &http.Client{Transport: &rpc.Transport{Base: transport}}
	return func(ctx context.Context, procedure string, body []byte) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://herdsman"+procedure, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", rpc.ContentTypeProto)
		req.Header.Set(mcp.HeaderVersion, daemonVersion())
		if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
			req.Header.Set(mcp.HeaderPane, pane)
		}
		if name := os.Getenv("HERDSMAN_NAME"); name != "" {
			req.Header.Set(mcp.HeaderName, name)
		}
		res, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		return io.ReadAll(io.LimitReader(res.Body, rpc.MaxBody))
	}
}

// mcpDeny is the deny list in force: the effective configuration's, read anew
// for each call so that a limit added while the server runs holds at once,
// plus the command line's.
func mcpDeny(configPath string, flags []string) func() ([]string, error) {
	return func() ([]string, error) {
		result, err := config.Load(config.Options{ConfigPath: configPath, RepoRoot: repoRoot()})
		if err != nil {
			return nil, err
		}
		return append(slices.Clone(flags), result.Config.GetPolicy().GetDeny()...), nil
	}
}

func runMCPCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, mcpHelp) }
	socket := fs.String("socket", "", "the daemon's socket")
	configPath := fs.String("config", "", "the configuration file")
	var deny multi
	fs.Var(&deny, "deny", "refuse a tool (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "herdsman mcp: unexpected argument %q\n", strings.Join(fs.Args(), " "))
		return 2
	}
	path := *socket
	if path == "" {
		found, err := client.SocketPath(os.Getenv)
		if err != nil {
			fmt.Fprintln(stderr, "herdsman mcp:", err)
			return 1
		}
		path = found
	}
	options := mcp.Options{
		Call:    mcpCaller(path),
		Deny:    mcpDeny(*configPath, deny),
		Version: version,
	}
	if err := serveMCP(mcp.Files(), options, stdin, stdout); err != nil {
		fmt.Fprintln(stderr, "herdsman mcp:", err)
		return 1
	}
	return 0
}

func serveMCP(files []protoreflect.FileDescriptor, options mcp.Options, stdin io.Reader, stdout io.Writer) error {
	server, err := mcp.NewServer(files, options)
	if err != nil {
		return err
	}
	return server.Serve(context.Background(), stdin, stdout)
}
