package route

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
)

type emptyKeychain struct{}

func (emptyKeychain) Load(string, string) (string, error) { return "v", nil }

func TestPaneEnvRefusesAJournaledBadReference(t *testing.T) {
	if _, err := paneEnv(Deps{}, stepData{SecretRefs: "broken"}); err == nil {
		t.Fatal("accepted")
	}
}

func TestPaneEnvUsesTheSystemKeychainByDefault(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := paneEnv(Deps{}, stepData{SecretRefs: "T=s/a"}); err == nil {
		t.Fatal("no keychain tool, no error")
	}
}

func TestPaneEnvWithNoOtherVariables(t *testing.T) {
	env, err := paneEnv(Deps{Keychain: emptyKeychain{}}, stepData{SecretRefs: "T=s/a"})
	if err != nil || env["T"] != "v" {
		t.Fatalf("%v %v", env, err)
	}
}

func TestConfinedArgsRefusesAKindThatChangedInTheJournal(t *testing.T) {
	root := t.TempDir()
	if _, err := confinedArgs(stepData{Kind: "claude", Sandbox: &sandboxData{Root: root}}); err == nil {
		t.Fatal("accepted")
	}
}

func TestCloneStepStates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if v, _, err := probeClone(ctx, Deps{}, nil, stepData{Clone: filepath.Join(dir, "none")}); err != nil || v != ops.Absent {
		t.Fatalf("%v %v", v, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "made", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	made := stepData{Clone: filepath.Join(dir, "made")}
	if v, _, err := probeClone(ctx, Deps{}, nil, made); err != nil || v != ops.Matches {
		t.Fatalf("%v %v", v, err)
	}
	if err := applyClone(ctx, Deps{}, nil, made); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "made"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "made"), 0o755) })
	if v, _, err := probeClone(ctx, Deps{}, nil, made); !errors.Is(err, os.ErrPermission) || v != ops.Unknown {
		t.Fatalf("%v %v", v, err)
	}
	if err := applyClone(ctx, Deps{}, nil, stepData{Clone: filepath.Join(dir, "new"), Repo: "relative"}); err == nil {
		t.Fatal("accepted")
	}
}
