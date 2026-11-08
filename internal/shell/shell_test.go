package shell

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	for in, want := range map[string]string{"/bin/zsh": "zsh", "/usr/bin/bash": "bash", "/opt/fish": "fish"} {
		if got, err := Detect(in); err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "/bin/tcsh"} {
		if _, err := Detect(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRCFileLocations(t *testing.T) {
	cases := []struct {
		shell, zdot, want string
	}{
		{"bash", "", "/h/.bashrc"},
		{"zsh", "", "/h/.zshrc"},
		{"zsh", "/z", "/z/.zshrc"},
		{"fish", "", "/h/.config/fish/conf.d/herdsman.fish"},
	}
	for _, c := range cases {
		got, err := RCFile(c.shell, Env{Home: "/h", ZDotDir: c.zdot})
		if err != nil || got != c.want {
			t.Errorf("%+v: %s %v", c, got, err)
		}
	}
	if _, err := RCFile("tcsh", Env{Home: "/h"}); err == nil {
		t.Error("unknown shell accepted")
	}
}

func TestInstallIsIdempotentAndKeepsTheRest(t *testing.T) {
	env := Env{Home: t.TempDir()}
	rc := filepath.Join(env.Home, ".bashrc")
	os.WriteFile(rc, []byte("export A=1\n"), 0o644)
	for range 2 {
		if _, err := Install("bash", env); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(rc)
	if strings.Count(string(data), startMark) != 1 || !strings.HasPrefix(string(data), "export A=1\n") {
		t.Fatalf("%s", data)
	}
}

func TestInstallUpgradesAnOldWrapperInPlace(t *testing.T) {
	env := Env{Home: t.TempDir()}
	rc := filepath.Join(env.Home, ".zshrc")
	os.WriteFile(rc, []byte("before\n"+startMark+"\nold body\n"+endMark+"\nafter\n"), 0o644)
	if _, err := Install("zsh", env); err != nil {
		t.Fatal(err)
	}
	text := mustRead(t, rc)
	if strings.Contains(text, "old body") || !strings.HasPrefix(text, "before\n") || !strings.HasSuffix(text, "\nafter\n") {
		t.Fatalf("%s", text)
	}
}

func TestInstallRefusesAnUnterminatedBlock(t *testing.T) {
	env := Env{Home: t.TempDir()}
	rc := filepath.Join(env.Home, ".bashrc")
	os.WriteFile(rc, []byte(startMark+"\nmine\n"), 0o644)
	if _, err := Install("bash", env); err == nil {
		t.Fatal("rewrote a file with half a block")
	}
	if got := mustRead(t, rc); !strings.Contains(got, "mine") {
		t.Fatal(got)
	}
}

func TestUninstallRemovesOnlyTheBlock(t *testing.T) {
	env := Env{Home: t.TempDir()}
	rc := filepath.Join(env.Home, ".bashrc")
	os.WriteFile(rc, []byte("export A=1\n"), 0o644)
	Install("bash", env)
	removed, err := Uninstall("bash", env)
	if err != nil || !removed {
		t.Fatalf("%v %v", removed, err)
	}
	if got := mustRead(t, rc); got != "export A=1\n" {
		t.Fatalf("%q", got)
	}
	if removed, err := Uninstall("bash", env); err != nil || removed {
		t.Fatalf("%v %v", removed, err)
	}
}

func TestUninstallWithNoFile(t *testing.T) {
	if removed, err := Uninstall("bash", Env{Home: t.TempDir()}); err != nil || removed {
		t.Fatalf("%v %v", removed, err)
	}
}

func TestInstallKeepsFilePermissions(t *testing.T) {
	env := Env{Home: t.TempDir()}
	rc := filepath.Join(env.Home, ".bashrc")
	os.WriteFile(rc, []byte("x\n"), 0o600)
	Install("bash", env)
	info, _ := os.Stat(rc)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%v", info.Mode())
	}
}

func TestCompletionsNameEveryVerb(t *testing.T) {
	for _, name := range Shells {
		text, err := Completions(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, verb := range []string{"new", "ls", "go", "hook"} {
			if !strings.Contains(text, verb) {
				t.Errorf("%s misses %s", name, verb)
			}
		}
	}
	if _, err := Completions("tcsh"); err == nil {
		t.Fatal("unknown shell accepted")
	}
	if _, err := Wrapper("tcsh"); err == nil {
		t.Fatal("unknown shell accepted")
	}
}

// fakeBinary is a herdsman that writes its directive lines, for the wrapper to obey.
func fakeBinary(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\nif [ -z \"$HERDSMAN_DIRECTIVE_FILE\" ]; then echo no-directive-file; exit 3; fi\n"
	for _, line := range lines {
		body += "echo '" + line + "' >> \"$HERDSMAN_DIRECTIVE_FILE\"\n"
	}
	body += "exit 5\n"
	if err := os.WriteFile(filepath.Join(dir, "herdsman"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func script(shellName, wrapper string) string {
	if shellName == "fish" {
		return wrapper + "\nherdsman go x\nset s $status\npwd\necho $s\n"
	}
	return wrapper + "\nherdsman go x\ns=$?\npwd\necho $s\n"
}

// The directive file is one path per line; a line of a later kind has a prefix
// that today's wrapper has to ignore.
func TestWrapperChangesFolderAndKeepsTheExitCode(t *testing.T) {
	for _, name := range Shells {
		t.Run(name, func(t *testing.T) {
			program, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			target, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			bin := fakeBinary(t, target, "future:ignored")
			wrapper, err := Wrapper(name)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "s")
			os.WriteFile(path, []byte(script(name, wrapper)), 0o644)
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

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
