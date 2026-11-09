package daemon

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ledger"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// ledgerClient reaches the service through the daemon's own RPC handler.
func ledgerClient(t *testing.T) daemonv1.LedgerService {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.WriteFile(bin, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := New(Config{PrivateHome: root, Home: root, Version: testVersion, Executable: bin, Log: func(string) {}})
	server := httptest.NewServer(d.rpcHandler())
	t.Cleanup(server.Close)
	return daemonv1.NewLedgerServiceClient(server.Client(), server.URL)
}

func TestLedgerMethodsAnswerOverTheDaemonsRPC(t *testing.T) {
	w := setupLedger(t, "a")
	useLedger(t, w.service)
	w.pids = []ledger.Process{{Pid: 5, Group: 5, Parent: 1, Start: ledgerStart, Command: "server"}}
	client := ledgerClient(t)
	ctx := context.Background()
	added, err := client.AddResource(ctx, &daemonv1.AddResourceRequest{CommonDir: w.common, Task: "a", Execution: "zz", Kind: ledger.KindProcess, Id: "5", Source: "rpc"})
	if err != nil {
		t.Fatal(err)
	}
	if added.GetRecord().GetCommand() != "server" || added.GetRecord().GetStart() == "" {
		t.Fatalf("added = %v", added)
	}
	plan, err := client.Plan(ctx, &daemonv1.PlanRequest{CommonDir: w.common, Task: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if lines := plan.GetPlan().GetLines(); len(lines) != 1 || lines[0].GetId() != "5" || lines[0].GetAction() != string(ledger.ActionStop) {
		t.Fatalf("plan = %v", plan)
	}
	resources, err := client.Resources(ctx, &daemonv1.ResourcesRequest{CommonDir: w.common, Task: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.GetRecords()) != 1 || len(resources.GetPlan().GetLines()) != 1 {
		t.Fatalf("resources = %v", resources)
	}
}

func TestLedgerMethodsRefuseWhatTheyCannotServe(t *testing.T) {
	w := setupLedger(t, "a")
	useLedger(t, w.service)
	client := ledgerClient(t)
	ctx := context.Background()
	for _, target := range []struct{ dir, task string }{{"relative", "a"}, {w.common, ""}, {w.common, "missing"}} {
		if _, err := client.Plan(ctx, &daemonv1.PlanRequest{CommonDir: target.dir, Task: target.task}); err == nil {
			t.Errorf("plan %v accepted", target)
		}
		if _, err := client.Resources(ctx, &daemonv1.ResourcesRequest{CommonDir: target.dir, Task: target.task}); err == nil {
			t.Errorf("resources %v accepted", target)
		}
	}
	for _, target := range []struct{ dir, task string }{{"relative", "a"}, {w.common, ""}} {
		if _, err := client.AddResource(ctx, &daemonv1.AddResourceRequest{CommonDir: target.dir, Task: target.task, Execution: "zz", Kind: ledger.KindVolume, Id: "v"}); err == nil {
			t.Errorf("add %v accepted", target)
		}
	}
	if _, err := client.AddResource(ctx, &daemonv1.AddResourceRequest{CommonDir: w.common, Task: "a", Execution: "zz", Kind: ledger.KindVolume, Id: "absent"}); err == nil {
		t.Error("a resource whose identity cannot be read")
	}
	w.fail = errors.New("no read")
	if _, err := client.Plan(ctx, &daemonv1.PlanRequest{CommonDir: w.common, Task: "a"}); err == nil {
		t.Error("a failed read")
	}
}

func TestWirePlanKeepsEverySignalAndLeavesAZeroStartEmpty(t *testing.T) {
	plan := wirePlan(ledger.Plan{Lines: []ledger.Line{{Kind: ledger.KindProcess, ID: "5", Signals: []ledger.Signal{"a", "b"}}}})
	if got := plan.GetLines()[0].GetSignals(); len(got) != 2 || got[1] != "b" {
		t.Fatalf("signals = %v", got)
	}
	if got := wireRecord(ledger.Record{Kind: ledger.KindContainer, ID: "c"}).GetStart(); got != "" {
		t.Fatalf("start = %q", got)
	}
}

func TestAnInspectDaemonServesNoLedgerCall(t *testing.T) {
	w := setupLedger(t, "a")
	useLedger(t, w.service)
	l := ledgerRPC{inspect: true}
	ctx := context.Background()
	if _, err := l.Plan(ctx, &daemonv1.PlanRequest{CommonDir: w.common, Task: "a"}); err == nil {
		t.Error("plan")
	}
	if _, err := l.Resources(ctx, &daemonv1.ResourcesRequest{CommonDir: w.common, Task: "a"}); err == nil {
		t.Error("resources")
	}
	if _, err := l.AddResource(ctx, &daemonv1.AddResourceRequest{CommonDir: w.common, Task: "a"}); err == nil {
		t.Error("add")
	}
}
