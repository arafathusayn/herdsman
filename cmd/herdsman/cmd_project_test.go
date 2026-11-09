package main

import (
	"bytes"
	"strings"
	"testing"
)

func runProjectCommand(args ...string) (string, string, int) {
	var out, errOut bytes.Buffer
	code := commands["project"].run(args, nil, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestProjectDispatchesItsVerbs(t *testing.T) {
	root := contextFolderUnderDaemon(t)
	out, errOut, code := runProjectCommand("context", "init")
	if code != 0 || out != root+"\n" {
		t.Fatalf("context init: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if out, errOut, code = runProjectCommand("log"); code != 0 || out != "" {
		t.Fatalf("log: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestProjectUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"create"}} {
		if out, errOut, code := runProjectCommand(args...); code != 2 || out != "" || !strings.Contains(errOut, "usage:") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	if out, _, code := call(t, "project", "--help"); code != 0 || !strings.Contains(out, "project log") {
		t.Errorf("--help: exit %d, %q", code, out)
	}
	if out, _, code := call(t, "project", "log", "--help"); code != 0 || !strings.Contains(out, "--since") {
		t.Errorf("log --help: exit %d, %q", code, out)
	}
}
