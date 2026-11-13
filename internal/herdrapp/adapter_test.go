package herdrapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// script writes an executable stand-in for herdr-app.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "herdr-app")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecRunnerPassesExplicitHomeAndArgs(t *testing.T) {
	t.Setenv("HERDR_APP_HOME", "/somewhere/else")
	bin := script(t, `printf '%s|%s|%s' "$HERDR_APP_HOME" "$1" "$2"`)
	home := t.TempDir()
	out, err := ExecRunner{Binary: bin}.Run(context.Background(), Invocation{Home: home, Args: []string{"do", `{"id":"x y"}`}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out.Stdout), home+`|do|{"id":"x y"}`; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestExecRunnerRefusesRelativeHome(t *testing.T) {
	if _, err := (ExecRunner{Binary: script(t, "true")}).Run(context.Background(), Invocation{Home: "rel"}); err == nil {
		t.Fatal("relative home accepted")
	}
}

func TestExecRunnerReportsExitCodes(t *testing.T) {
	bin := script(t, `echo "herdr-app: no Herdr window at $HERDR_APP_HOME/herdr-app.sock: No such file or directory (os error 2)" >&2; exit 2`)
	app := New(ExecRunner{Binary: bin}, t.TempDir())
	if _, err := app.List(context.Background()); !errors.Is(err, ErrAppClosed) {
		t.Fatalf("err = %v, want ErrAppClosed", err)
	}
}

func TestExecRunnerTimesOut(t *testing.T) {
	bin := script(t, "exec sleep 5")
	_, err := ExecRunner{Binary: bin, Timeout: 100 * time.Millisecond}.Run(context.Background(), Invocation{Home: t.TempDir(), Args: []string{"ls"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
}

func TestExecRunnerBoundsOutput(t *testing.T) {
	bin := script(t, "head -c 4096 /dev/zero")
	_, err := ExecRunner{Binary: bin, MaxOutput: 1024}.Run(context.Background(), Invocation{Home: t.TempDir(), Args: []string{"ls"}})
	if !errors.Is(err, errOutputTooLong) {
		t.Fatalf("err = %v, want errOutputTooLong", err)
	}
}

func TestDoRefusesCommandsOutsideTheAllowList(t *testing.T) {
	app := New(ExecRunner{Binary: script(t, "exit 99")}, t.TempDir())
	for _, command := range []string{"terminal.pin", "terminal.park", "space.close", "tab.close", "terminal.kill", "layout.set"} {
		if _, err := app.do(context.Background(), command, nil); err == nil || !strings.Contains(err.Error(), "not one the adapter sends") {
			t.Errorf("%s: err = %v", command, err)
		}
	}
}

func TestDoReportsCommandError(t *testing.T) {
	bin := script(t, `echo '{ "error": "no such space" }'; exit 1`)
	_, err := New(ExecRunner{Binary: bin}, t.TempDir()).do(context.Background(), "space.rename", map[string]string{"id": "hs-x", "name": "n"})
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) || cmdErr.Message != "no such space" {
		t.Fatalf("err = %v", err)
	}
}

func TestVersionParsesInstalledLine(t *testing.T) {
	bin := script(t, `echo "herdr-app 0.1.15"`)
	v, err := New(ExecRunner{Binary: bin}, t.TempDir()).Version(context.Background())
	if err != nil || v != "0.1.15" {
		t.Fatalf("version = %q, %v", v, err)
	}
}
