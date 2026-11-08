package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/status"
)

func healthWorld(t *testing.T, state string, replies map[string]string) (*[]verbCall, string) {
	t.Helper()
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	a := herdrtest.Agent("w9:p1", "s")
	a["name"], a["agent_status"] = "im-a", state
	fx.SetAgent("w9:p1", a)
	fx.SetScreen("w9:p1", "working\n")
	t.Setenv("HERDR_SOCKET_PATH", srv.Socket)
	t.Setenv("LIMIT_MIN", "")
	return stubWorktree(t, replies), "im-a:w9:p1:/tree"
}

func healthFlags(t *testing.T, specs string) ([]string, string) {
	t.Helper()
	var out bytes.Buffer
	statusWithLedger(context.Background(), &out, specs)
	return status.Flags(out.String()), out.String()
}

func TestTheWaiterFlagsOwnedResourcesAfterDone(t *testing.T) {
	calls, specs := healthWorld(t, "done", map[string]string{"LedgerHealth": `{"running":[{"kind":"process","id":"5"}]}`})
	flags, out := healthFlags(t, specs)
	if !strings.Contains(strings.Join(flags, "\n"), "im-a OWNED RESOURCES OUTLIVE DONE") {
		t.Fatalf("flags = %v in %q", flags, out)
	}
	if (*calls)[0].body["dir"] != "/tree" {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestTheWaiterIsQuietOtherwise(t *testing.T) {
	for name, tc := range map[string]struct {
		state   string
		specs   string
		replies map[string]string
	}{
		"working":      {"working", "", map[string]string{"LedgerHealth": `{"running":[{"kind":"process","id":"5"}]}`}},
		"nothing left": {"done", "", map[string]string{"LedgerHealth": `{"running":[]}`}},
		"failed read":  {"done", "", nil},
		"bad answer":   {"done", "", map[string]string{"LedgerHealth": `not json`}},
		"no tree":      {"done", "im-a:w9:p1:", map[string]string{"LedgerHealth": `{"running":[{"kind":"process","id":"5"}]}`}},
	} {
		t.Run(name, func(t *testing.T) {
			_, specs := healthWorld(t, tc.state, tc.replies)
			if tc.specs != "" {
				specs = tc.specs
			}
			flags, _ := healthFlags(t, specs)
			if strings.Contains(strings.Join(flags, "\n"), "OUTLIVE") {
				t.Fatalf("flags = %v", flags)
			}
		})
	}
}

func TestAnUnknownAgentIsNoFlag(t *testing.T) {
	srv := herdrtest.Start(t)
	herdrtest.Serve(srv)
	t.Setenv("HERDR_SOCKET_PATH", srv.Socket)
	t.Setenv("LIMIT_MIN", "")
	stubWorktree(t, map[string]string{"LedgerHealth": `{"running":[{"kind":"process","id":"5"}]}`})
	if flags, _ := healthFlags(t, "nobody:w9:p1:/tree"); strings.Contains(strings.Join(flags, "\n"), "OUTLIVE") {
		t.Fatalf("flags = %v", flags)
	}
}
