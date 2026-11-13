package trace

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
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

func TestRunValidRefusesNonFiniteValues(t *testing.T) {
	pass := true
	for _, duration := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		value := duration
		suite := t.TempDir()
		if err := AppendRun(suite, Run{Task: "t", Arm: "a", Pass: &pass, DurationMS: &value}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", duration, err)
		}
		if _, err := os.Stat(filepath.Join(suite, RunsFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v touched the runs file: %v", duration, err)
		}
	}
}

func TestSpanValidRefusesNonFiniteDurations(t *testing.T) {
	for _, duration := range []float64{math.Inf(1), math.NaN()} {
		if err := Append(t.TempDir(), Span{Kind: KindCheck, Name: "c", Start: time.Unix(1, 0), DurationMS: duration}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", duration, err)
		}
	}
}

func TestAppendRunReturnsAnEncodingError(t *testing.T) {
	saved := encodeRun
	t.Cleanup(func() { encodeRun = saved })
	encodeRun = func(Run) ([]byte, error) { return nil, errors.New("cannot encode") }
	pass, duration := true, 1.0
	suite := t.TempDir()
	if err := AppendRun(suite, Run{Task: "t", Arm: "a", Pass: &pass, DurationMS: &duration}); err == nil || !strings.Contains(err.Error(), "cannot encode") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(suite, RunsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed encoding touched the runs file: %v", err)
	}
}
