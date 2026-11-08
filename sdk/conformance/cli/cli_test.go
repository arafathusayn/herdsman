// Package cli checks a command-line implementation against the published
// command contract. HERDSMAN_CONFORMANCE_CLI names the program to check; without
// it the bundled herdsman binary is built and checked.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

type specCommand struct {
	Name        string        `json:"name"`
	Summary     string        `json:"summary"`
	Help        string        `json:"help"`
	Subcommands []specCommand `json:"subcommands"`
}

type spec struct {
	Contract string        `json:"contract"`
	Commands []specCommand `json:"commands"`
}

func program(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("HERDSMAN_CONFORMANCE_CLI"); path != "" {
		return path
	}
	path := filepath.Join(t.TempDir(), "herdsman")
	build := exec.Command("go", "build", "-o", path, "./cmd/herdsman")
	build.Dir = filepath.Join("..", "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return path
}

func run(t *testing.T, path string, args ...string) (stdout, stderr []byte, code int) {
	t.Helper()
	cmd := exec.Command(path, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("%v: %v", args, err)
	}
	return out.Bytes(), errOut.Bytes(), code
}

func TestCLIConformance(t *testing.T) {
	path := program(t)
	stdout, stderr, code := run(t, path, "spec", "cli")
	if code != 0 {
		t.Fatalf("spec cli: exit %d: %s", code, stderr)
	}

	t.Run("the spec equals the published transcript", func(t *testing.T) {
		golden, err := os.ReadFile(filepath.Join("testdata", "spec.golden.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stdout, golden) {
			t.Fatal("spec cli differs from testdata/spec.golden.json")
		}
	})

	var document spec
	if err := json.Unmarshal(stdout, &document); err != nil {
		t.Fatalf("spec cli is not JSON: %v", err)
	}

	t.Run("the document names its contract and lists commands in order", func(t *testing.T) {
		if document.Contract == "" || len(document.Commands) == 0 {
			t.Fatalf("document %+v", document)
		}
		if !sort.SliceIsSorted(document.Commands, func(i, j int) bool { return document.Commands[i].Name < document.Commands[j].Name }) {
			t.Fatal("commands are not sorted by name")
		}
	})

	t.Run("every command prints the help the spec records", func(t *testing.T) {
		for _, command := range document.Commands {
			if command.Summary == "" {
				t.Errorf("%s has no summary", command.Name)
			}
			if command.Help == "" {
				continue
			}
			out, _, code := run(t, path, command.Name, "--help")
			if code != 0 || string(out) != command.Help {
				t.Errorf("%s --help: exit %d, output differs from the spec", command.Name, code)
			}
			for _, sub := range command.Subcommands {
				out, _, code := run(t, path, command.Name, sub.Name, "--help")
				if code != 0 || string(out) != sub.Help {
					t.Errorf("%s %s --help: exit %d, output differs from the spec", command.Name, sub.Name, code)
				}
			}
		}
	})

	t.Run("an unknown spec kind is a usage error", func(t *testing.T) {
		out, _, code := run(t, path, "spec", "no-such-kind")
		if code != 2 || len(out) != 0 {
			t.Fatalf("exit %d, stdout %q", code, out)
		}
	})
}
