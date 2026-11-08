package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/mcp"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func mcpServer(t *testing.T, call mcp.Caller) *mcp.Server {
	t.Helper()
	s, err := mcp.NewServer(mcp.Files(), mcp.Options{Call: call, Deny: func() ([]string, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A tool the daemon does not serve would answer every call with a 404, and a
// method added to the daemon must be given a tool or withheld on purpose.
func TestMCPToolsAreExactlyWhatTheDaemonServes(t *testing.T) {
	var served []string
	for _, procedure := range (&Daemon{}).rpcProcedures() {
		served = append(served, procedure.Path)
	}
	tools := mcpServer(t, func(context.Context, string, []byte) ([]byte, error) { return nil, nil }).Procedures()
	for _, procedure := range tools {
		if !slices.Contains(served, procedure) {
			t.Errorf("tool for %s, which the daemon does not serve", procedure)
		}
	}
	withheld := []string{"Shutdown", "Enroll", "ApprovePair", "RetirePair", "RevokeCredential", "ResetEvents",
		"ContextInit", "ContextCheck", "ContextNote", "Log"}
	for _, procedure := range served {
		if !slices.Contains(tools, procedure) && !slices.Contains(withheld, procedure[strings.LastIndex(procedure, "/")+1:]) {
			t.Errorf("%s is served but has no tool and is not withheld", procedure)
		}
	}
}

func TestMCPHeadersAreTheDaemons(t *testing.T) {
	if mcp.HeaderVersion != HeaderVersion || mcp.HeaderPane != HeaderPane || mcp.HeaderName != HeaderName {
		t.Fatalf("%s %s %s", mcp.HeaderVersion, mcp.HeaderPane, mcp.HeaderName)
	}
}

// The real tool against the real daemon: the plan is the daemon's and nothing
// is journaled.
func TestMCPRouteUpPlansOnTheRealDaemon(t *testing.T) {
	h := newHarness(t)
	socket := SocketPath(h.ph)
	hc := &http.Client{Transport: &rpc.Transport{Base: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
	t.Cleanup(hc.CloseIdleConnections)
	s := mcpServer(t, func(ctx context.Context, procedure string, body []byte) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://herdsman"+procedure, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", rpc.ContentTypeProto)
		res, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		return io.ReadAll(res.Body)
	})
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), inR, outW) }()
	t.Cleanup(func() {
		inW.Close()
		go io.Copy(io.Discard, outR)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	replies := bufio.NewScanner(outR)
	replies.Buffer(nil, rpc.MaxBody)
	exchange := func(line string) string {
		t.Helper()
		if _, err := io.WriteString(inW, line+"\n"); err != nil {
			t.Fatal(err)
		}
		if !replies.Scan() {
			t.Fatal(replies.Err())
		}
		return replies.Text()
	}
	exchange(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	args, err := json.Marshal(map[string]any{
		"route": map[string]string{"value": "harbor"}, "repo": filepath.Join(h.ph, "proj"), "prompt": "ship it",
		"adjutant_command": "claude doctor && claude", "orchestrator_command": "codex",
		"kinds": []string{"claude", "codex"}, "date": "2026-11-09", "dry_run": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	line := exchange(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"daemon_route_up","arguments":` + string(args) + `}}`)
	var reply struct {
		Result struct {
			IsError    bool `json:"isError"`
			Structured struct {
				Plan struct {
					State string `json:"state"`
					Plan  string `json:"plan"`
				} `json:"plan"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Result.IsError || reply.Result.Structured.Plan.State != "planned" || !strings.Contains(reply.Result.Structured.Plan.Plan, "claude doctor") {
		t.Fatalf("%s", line)
	}
	list, err := routeClient(t, h).Route.List(context.Background(), &daemonv1.RouteServiceListRequest{})
	if err != nil || len(list.GetStarts()) != 0 {
		t.Fatalf("a dry run was journaled: %v %v", list, err)
	}
}
