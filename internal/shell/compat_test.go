package shell

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var firstWrappers = map[string]string{"bash": "bash.sh", "zsh": "zsh.sh", "fish": "fish.fish"}

// The wrapper lives in people's startup files and does not update with the
// binary, so the wrapper as first shipped must keep working as the binary moves.
func TestFirstShippedWrapperStillFollowsTheDirectiveFile(t *testing.T) {
	for _, name := range Shells {
		t.Run(name, func(t *testing.T) {
			program, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			wrapper, err := os.ReadFile(filepath.Join("testdata", "first-wrapper", firstWrappers[name]))
			if err != nil {
				t.Fatal(err)
			}
			target, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			bin := fakeBinary(t, target, "future:ignored")
			path := filepath.Join(t.TempDir(), "s")
			os.WriteFile(path, []byte(script(name, string(wrapper))), 0o644)
			cmd := exec.Command(program, path)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v: %s", err, errOut.String())
			}
			got := strings.Fields(out.String())
			if len(got) != 2 || got[0] != target || got[1] != "5" {
				t.Fatalf("%q, stderr %q", out.String(), errOut.String())
			}
		})
	}
}

func TestInstallReplacesTheFirstShippedBlock(t *testing.T) {
	for _, name := range Shells {
		env := Env{Home: t.TempDir()}
		wrapper, err := os.ReadFile(filepath.Join("testdata", "first-wrapper", firstWrappers[name]))
		if err != nil {
			t.Fatal(err)
		}
		path, err := RCFile(name, env)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("before\n"+startMark+"\n"+string(wrapper)+endMark+"\nafter\n"), 0o644)
		if _, err := Install(name, env); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		text := string(data)
		if strings.Count(text, startMark) != 1 || !strings.HasPrefix(text, "before\n") || !strings.HasSuffix(text, "after\n") {
			t.Fatalf("%s: %q", name, text)
		}
	}
}
