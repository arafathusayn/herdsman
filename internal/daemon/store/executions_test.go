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
// store this version creates selects core.
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
	if b, _ := installation(t, c); b != "herdr-core" {
		t.Fatalf("reopened as %s", b)
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
