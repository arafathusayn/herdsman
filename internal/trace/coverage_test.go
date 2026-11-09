package trace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendRunRefusesAnIncompleteRun(t *testing.T) {
	if err := AppendRun(t.TempDir(), Run{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
}

func TestAppendRunReportsAFolderItCannotWriteTo(t *testing.T) {
	pass, duration := true, 1.0
	run := Run{Task: "t", Arm: "a", Pass: &pass, DurationMS: &duration}
	if err := AppendRun(filepath.Join(t.TempDir(), "absent"), run); err == nil {
		t.Fatal("a missing folder must be an error")
	}
}

func TestReadOnlyCollectUsesAStoredIdentity(t *testing.T) {
	route := t.TempDir()
	if err := Append(route, Span{Kind: KindCheck, Name: "c", Start: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	written, err := Collect(Sources{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	read, err := Collect(Sources{Route: route, ReadOnly: true})
	if err != nil || read.TraceID != written.TraceID {
		t.Fatalf("%v: %s and %s", err, read.TraceID, written.TraceID)
	}
}

func TestReadOnlyCollectReportsADamagedIdentity(t *testing.T) {
	route := t.TempDir()
	if err := Append(route, Span{Kind: KindCheck, Name: "c", Start: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(route, traceDir, routeIDFile), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(Sources{Route: route, ReadOnly: true}); err == nil {
		t.Fatal("a damaged identity must be an error")
	}
}

func TestIdentityFallsBackWhenTheRouteCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	for name, block := range map[string]func(route string) string{
		"route": func(route string) string { return route },
		"trace": func(route string) string {
			folder := filepath.Join(route, traceDir)
			if err := os.Mkdir(folder, 0o700); err != nil {
				t.Fatal(err)
			}
			return folder
		},
	} {
		t.Run(name, func(t *testing.T) {
			route := t.TempDir()
			folder := block(route)
			if err := os.Chmod(folder, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(folder, 0o700) })
			id, err := routeIdentity(route)
			if err != nil || id != digest("path", route)[:32] {
				t.Fatalf("%q %v", id, err)
			}
		})
	}
}
