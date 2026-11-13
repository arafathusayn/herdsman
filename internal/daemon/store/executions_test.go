package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func installation(t *testing.T, c Conn) (string, string) {
	t.Helper()
	backend, source, at, err := InstallationDefault(c)
	if err != nil {
		t.Fatal(err)
	}
	if at <= 0 {
		t.Fatalf("written_at %d", at)
	}
	return backend, source
}

// Gate "Upgrade": a store that already existed keeps the Herdr TUI, and a
// store this version creates starts on core. Each stored choice survives an
// upgrade or restart, including an explicit choice of the optional TUI.
func TestInstallationDefaultIsWrittenOnceByTheMigration(t *testing.T) {
	for _, v := range []int{1, 4} {
		p := loadFixture(t, v, KindAccount)
		c, err := Open(p, Writer, KindAccount)
		if err != nil {
			t.Fatal(err)
		}
		if b, s := installation(t, c); b != "herdr-tui" || s != DefaultFromExistingStore {
			t.Fatalf("schema-%d upgraded to %s from %s", v, b, s)
		}
		_ = c.Close()
	}

	p := filepath.Join(privateDir(t), "account.db")
	c, err := Open(p, Writer, KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	if b, s := installation(t, c); b != "herdr-core" || s != DefaultFromNewStore {
		t.Fatalf("a new store has %s from %s", b, s)
	}
	_ = c.Close()

	// A later open migrates nothing and writes nothing.
	c, err = Open(p, Writer, KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if b, s := installation(t, c); b != "herdr-core" || s != DefaultFromNewStore {
		t.Fatalf("reopened as %s from %s", b, s)
	}
	prev, err := SetInstallationDefault(c, "herdr-tui", 5)
	if err != nil || prev != "herdr-core" {
		t.Fatalf("set: %q %v", prev, err)
	}
	if b, s := installation(t, c); b != "herdr-tui" || s != DefaultSet {
		t.Fatalf("after set: %s from %s", b, s)
	}
	if _, err := SetInstallationDefault(c, "herdr-gui", 6); err == nil {
		t.Fatal("an unknown backend was stored")
	}
}

// Gate "Migration": once the store holds version 5, a binary that knows only
// version 4 refuses it by name, the Herdr TUI routes included.
func TestOlderBinaryRefusesTheMigratedStore(t *testing.T) {
	for _, kind := range []Kind{KindAccount, KindRoute} {
		p := loadFixture(t, 4, kind)
		c, err := Open(p, Writer, kind)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		older := schemaUpTo(t, kind, Newest(kind)-1)
		raw, err := openDriver(p, Writer)
		if err != nil {
			t.Fatal(err)
		}
		err = migrate(raw, p, kind, older)
		_ = raw.Close()
		if !errors.Is(err, ErrSchemaNewer) || !strings.Contains(err.Error(), "newer than this herdsman") {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}

func execution(id, intent string) Execution {
	return Execution{
		ID: id, Repo: "/srv/repo", Route: "harbor", Agent: "repo-harbor-orchestrator", Role: "orchestrator",
		LaunchIntent: intent, Backend: "herdr-tui", EndpointID: "/srv/herdr.sock", Pane: "w1:p2", HarnessKind: "codex",
		DesiredState: "running", Process: "live", Readiness: "waiting", Program: "idle", Visibility: "verified",
		Orchestration: "active", ObservedAt: 10,
	}
}

// Conversation evidence, once recorded, survives a write that does not carry
// it: a writer that read the record before the evidence came cannot clear it.
func TestExecutionKeepsItsConversationEvidence(t *testing.T) {
	for _, kind := range []Kind{KindAccount, KindRoute} {
		t.Run(string(kind), func(t *testing.T) {
			c := openKind(t, kind)
			e := execution("11111111-1111-4111-8111-111111111111", "op-1:agent:a")
			if _, err := PutExecution(c, e, 100); err != nil {
				t.Fatal(err)
			}
			withConv := e
			withConv.NativeConversation = `{"value":"0b7f3c1e-5d2a-4c8e-9f10-2a3b4c5d6e7f","provenance":"codex:rollout-session-meta"}`
			if _, err := PutExecution(c, withConv, 200); err != nil {
				t.Fatal(err)
			}
			stale := e
			stale.Program = "working"
			got, err := PutExecution(c, stale, 300)
			if err != nil || got.NativeConversation != withConv.NativeConversation {
				t.Fatalf("the returned record: %q %v", got.NativeConversation, err)
			}
			one, _, err := ExecutionByID(c, got.ID)
			if err != nil || one.NativeConversation != withConv.NativeConversation || one.Program != "working" {
				t.Fatalf("the stored record: %+v %v", one, err)
			}
		})
	}
}

// The experiment label: the migration names runtime.core/0.1.0 on every
// herdr-core record already written and nothing on a herdr-tui one, and a
// record keeps the label it was first written with.
func TestExecutionNamesTheExperimentThatWroteIt(t *testing.T) {
	for _, kind := range []Kind{KindAccount, KindRoute} {
		t.Run(string(kind), func(t *testing.T) {
			p := loadFixture(t, 6, kind)
			c, err := Open(p, Writer, kind)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			coreRow, ok, err := ExecutionByID(c, "11111111-1111-4111-8111-111111111111")
			if err != nil || !ok || coreRow.Experiment != "runtime.core/0.1.0" {
				t.Fatalf("the herdr-core row: %q %v %v", coreRow.Experiment, ok, err)
			}
			tuiRow, ok, err := ExecutionByID(c, "22222222-2222-4222-8222-222222222222")
			if err != nil || !ok || tuiRow.Experiment != "" {
				t.Fatalf("the herdr-tui row: %q %v %v", tuiRow.Experiment, ok, err)
			}
			e := execution("33333333-3333-4333-8333-333333333333", "op-9:agent:a")
			e.Backend, e.Experiment = "herdr-core", "runtime.core/0.1.0"
			if _, err := PutExecution(c, e, 100); err != nil {
				t.Fatal(err)
			}
			e.Experiment = "runtime.core/0.2.0" // a later writer does not relabel it
			got, err := PutExecution(c, e, 200)
			if err != nil || got.Experiment != "runtime.core/0.1.0" {
				t.Fatalf("relabelled: %q %v", got.Experiment, err)
			}
			if one, _, _ := ExecutionByID(c, got.ID); one.Experiment != "runtime.core/0.1.0" {
				t.Fatalf("stored: %q", one.Experiment)
			}
		})
	}
}

func TestExecutionRecordsKeepIdentityAndCountGenerations(t *testing.T) {
	for _, kind := range []Kind{KindAccount, KindRoute} {
		t.Run(string(kind), func(t *testing.T) {
			c := openKind(t, kind)
			first, err := PutExecution(c, execution("11111111-1111-4111-8111-111111111111", "op-1:agent:a"), 100)
			if err != nil {
				t.Fatal(err)
			}
			if first.Generation != 1 || first.SchemaVersion != ExecutionSchema {
				t.Fatalf("%+v", first)
			}
			// The same intent again is the same execution, whatever id the
			// caller made up this time.
			again := execution("22222222-2222-4222-8222-222222222222", "op-1:agent:a")
			again.Program, again.RawProgram = "working", "working"
			got, err := PutExecution(c, again, 200)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != first.ID || got.Generation != 1 || got.Created != 100 || got.Updated != 200 {
				t.Fatalf("%+v", got)
			}
			second, err := PutExecution(c, execution("33333333-3333-4333-8333-333333333333", "op-2:agent:a"), 300)
			if err != nil {
				t.Fatal(err)
			}
			if second.Generation != 2 {
				t.Fatalf("generation %d", second.Generation)
			}
			list, err := Executions(c, "/srv/repo", "harbor")
			if err != nil || len(list) != 2 || list[0].ID != second.ID || list[1].Program != "working" {
				t.Fatalf("%v %+v", err, list)
			}
			one, found, err := ExecutionByID(c, first.ID)
			if err != nil || !found || one.Pane != "w1:p2" || one.PID != 0 {
				t.Fatalf("%v %v %+v", err, found, one)
			}
			bad := execution("44444444-4444-4444-8444-444444444444", "op-3:agent:a")
			bad.Readiness = "ready-ish"
			if _, err := PutExecution(c, bad, 400); err == nil {
				t.Fatal("an unknown readiness was stored")
			}
		})
	}
}

func TestRouteKeepsItsBackend(t *testing.T) {
	c := openKind(t, KindRoute)
	if _, _, found, err := RouteRuntime(c); err != nil || found {
		t.Fatalf("%v %v", found, err)
	}
	if err := BindRouteRuntime(c, "herdr-tui", "/srv/herdr.sock", 1); err != nil {
		t.Fatal(err)
	}
	if err := BindRouteRuntime(c, "herdr-tui", "/srv/herdr.sock", 2); err != nil {
		t.Fatal(err)
	}
	err := BindRouteRuntime(c, "herdr-core", "core-1", 3)
	var op *OpError
	if !errors.As(err, &op) || op.Code != "backend_migration_required" {
		t.Fatalf("%v", err)
	}
	if b, _, _, _ := RouteRuntime(c); b != "herdr-tui" {
		t.Fatalf("backend %s", b)
	}
}

// Registration evidence must survive persistence and stale observer writes.
func TestExecutionSealsRegistrationEvidence(t *testing.T) {
	for _, kind := range []Kind{KindAccount, KindRoute} {
		t.Run(string(kind), func(t *testing.T) {
			c := openKind(t, kind)
			e := execution("11111111-1111-4111-8111-111111111111", "launch")
			e.ArgvDigest, e.PolicyDigest = "planned-argv", "planned-policy"
			e.AppBinding = `{"core_id":"core-1","socket":"/srv/core.sock","binary":"/opt/hc"}`
			if _, err := PutExecution(c, e, 1); err != nil {
				t.Fatal(err)
			}
			stale := e
			e.PID, e.ProcessStart, e.InitialCreatedMS = 42, "process-start", 123
			e.LaunchGuard, e.ArgvDigest, e.PolicyDigest = "registered time", "resolved-argv", "resolved-policy"
			if _, err := PutExecution(c, e, 2); err != nil {
				t.Fatal(err)
			}
			// Even a replacement PID cannot inherit the registered execution.
			stale.PID, stale.ProcessStart = 99, "replacement"
			stale.AppBinding = `{"core_id":"core-1","visibility":"verified"}`
			if _, err := PutExecution(c, stale, 3); err != nil {
				t.Fatal(err)
			}
			got, _, err := ExecutionByID(c, e.ID)
			if err != nil || got.PID != 42 || got.ProcessStart != e.ProcessStart || got.InitialCreatedMS != 123 || got.LaunchGuard != e.LaunchGuard || got.ArgvDigest != e.ArgvDigest || got.PolicyDigest != e.PolicyDigest || !strings.Contains(got.AppBinding, `"socket":"/srv/core.sock"`) {
				t.Fatalf("lost registration: %+v %v", got, err)
			}
		})
	}
}
