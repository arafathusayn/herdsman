package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestIsTerminalAsksTheKernel(t *testing.T) {
	for _, device := range []string{"/dev/zero", "/dev/null"} {
		file, err := os.Open(device)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if IsTerminal(file) {
			t.Errorf("%s is a character device, not a terminal", device)
		}
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if IsTerminal(reader) {
		t.Error("a pipe is not a terminal")
	}
	if IsTerminal(strings.NewReader("yes\n")) {
		t.Error("a reader is not a terminal")
	}
	var none *os.File
	if IsTerminal(none) {
		t.Error("a nil file is not a terminal")
	}
}

// TestIsTerminalAcceptsARealTerminal runs itself under script(1), so that its
// standard input is the person's side of a pseudo-terminal.
func TestIsTerminalAcceptsARealTerminal(t *testing.T) {
	const resultEnv = "HERDSMAN_TEST_TERMINAL_RESULT"
	if path := os.Getenv(resultEnv); path != "" {
		if err := os.WriteFile(path, []byte(strconv.FormatBool(IsTerminal(os.Stdin))), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skipf("script is not installed: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := self + " -test.run=^TestIsTerminalAcceptsARealTerminal$"
	args := append([]string{"-q", "/dev/null"}, strings.Fields(run)...)
	if runtime.GOOS == "linux" {
		args = []string{"-q", "-c", run, "/dev/null"}
	}
	result := filepath.Join(t.TempDir(), "result")
	cmd := exec.Command("script", args...)
	cmd.Env = append(os.Environ(), resultEnv+"="+result)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "true" {
		t.Fatalf("a pseudo-terminal's standard input gave %s", data)
	}
}
