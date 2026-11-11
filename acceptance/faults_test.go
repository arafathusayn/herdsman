package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// standIns puts scripts that exit 0 on an otherwise empty PATH.
func standIns(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func replace[T any](t *testing.T, target *T, value T) {
	t.Helper()
	saved := *target
	*target = value
	t.Cleanup(func() { *target = saved })
}

// herdr-core and its app are claimed on Apple silicon only: on an Intel Mac
// both probes fail with that reason, though the tools answer; Linux declares
// both absent, so its probes are never reached.
func TestTheCoreProbesClaimAppleSiliconOnly(t *testing.T) {
	standIns(t, "hc", "herdr-app")
	replace(t, &goarch, "amd64")
	for _, name := range []string{"herdr-core", "herdr-app"} {
		if err := probesFor("darwin")[name](); err == nil || !strings.Contains(err.Error(), "Apple silicon") {
			t.Errorf("%s on darwin/amd64: %v", name, err)
		}
	}
	replace(t, &goarch, "arm64")
	for _, name := range []string{"herdr-core", "herdr-app"} {
		if err := probesFor("darwin")[name](); err != nil {
			t.Errorf("%s on darwin/arm64: %v", name, err)
		}
	}
}

func TestProbesPassWhenTheirToolsAnswerOnEachPlatform(t *testing.T) {
	// The core probes claim Apple silicon; the tools are what this test
	// proves on each platform.
	replace(t, &goarch, "arm64")
	standIns(t, "ps", "lsof", "docker", "security", "secret-tool", "mkfs.btrfs", "losetup", "sudo", "hc", "herdr-app")
	for _, goos := range platforms {
		for _, uid := range []int{0, 1000} {
			replace(t, &geteuid, func() int { return uid })
			for name, probe := range probesFor(goos) {
				if err := probe(); err != nil {
					t.Errorf("%s on %s as %d: %v", name, goos, uid, err)
				}
			}
		}
	}
}

func TestProbesFailOnEachPlatformWithoutTheirTools(t *testing.T) {
	standIns(t)
	for _, goos := range platforms {
		for _, name := range []string{"ps", "lsof", "docker", "btrfs-loopback", "keychain", "herdr-core", "herdr-app"} {
			if err := probesFor(goos)[name](); err == nil {
				t.Errorf("%s on %s passed with no tools", name, goos)
			}
		}
	}
}

func TestBtrfsProbeNeedsRootOrSudo(t *testing.T) {
	standIns(t, "mkfs.btrfs", "losetup")
	replace(t, &geteuid, func() int { return 1000 })
	if err := probesFor("linux")["btrfs-loopback"](); err == nil {
		t.Fatal("passed with neither root nor sudo")
	}
}

func TestPtyProbeReportsADeviceThatWillNotOpen(t *testing.T) {
	replace(t, &ptyDevice, filepath.Join(t.TempDir(), "missing"))
	if err := probesFor("linux")["pty"](); err == nil {
		t.Fatal("passed without the device")
	}
}

func TestJudgeKeepsOnlyTheEndOfALongFailure(t *testing.T) {
	var lines []string
	for i := range reasonTailLines + 10 {
		lines = append(lines, fmt.Sprintf("output-%03d", i))
	}
	got := judge(Gate{Name: "g", Test: "TestG"}, strings.Join(lines, "\n"), errors.New("exit 1"))
	if strings.Contains(got.Reason, "output-000") || !strings.Contains(got.Reason, lines[len(lines)-1]) {
		t.Fatalf("reason:\n%s", got.Reason)
	}
}

func TestJudgeKeepsTheFailureLineOfALongOutput(t *testing.T) {
	lines := []string{"before", "    gate_test.go:12: the real cause"}
	for i := range reasonTailLines + 10 {
		lines = append(lines, fmt.Sprintf("noise-%03d", i))
	}
	got := judge(Gate{Name: "g", Test: "TestG"}, strings.Join(lines, "\n"), errors.New("exit 1"))
	if !strings.Contains(got.Reason, "the real cause") || strings.Contains(got.Reason, "before") {
		t.Fatalf("reason:\n%s", got.Reason)
	}
}

func TestPlantPoisonStopsWhenAStandInCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, hostTools[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := plantPoison(dir); err == nil {
		t.Fatal("no error")
	}
}

func TestGoEnvironmentFailures(t *testing.T) {
	t.Run("no go", func(t *testing.T) {
		standIns(t)
		if _, err := goEnvironment(); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("a short answer", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\necho one\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		if _, err := goEnvironment(); err == nil || !strings.Contains(err.Error(), "got 1 values") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestExecGateReportsEverySetupFailure(t *testing.T) {
	gate := Gate{Name: "g", Package: ".", Test: "TestG"}
	wantFail := func(t *testing.T) {
		t.Helper()
		got := execGate(context.Background(), gate, nil)
		if got.Status != StatusFail || got.Gate != "g" || got.Reason == "" {
			t.Fatalf("%+v", got)
		}
	}
	t.Run("no scratch folder", func(t *testing.T) {
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		wantFail(t)
	})
	t.Run("no scratch home", func(t *testing.T) {
		replace(t, &makeDir, func(string, os.FileMode) error { return errors.New("planted") })
		wantFail(t)
	})
	t.Run("no stand-ins", func(t *testing.T) {
		// The folder is reported made but is not there, so writing into it fails.
		replace(t, &makeDir, func(string, os.FileMode) error { return nil })
		wantFail(t)
	})
}

func TestRealMainStopsOnSetupFailures(t *testing.T) {
	name := Gates[0].Name
	run := func(t *testing.T) (int, string) {
		t.Helper()
		var out, errOut strings.Builder
		code := realMain([]string{"-gate", name}, &out, &errOut)
		return code, errOut.String()
	}
	t.Run("an unreadable declaration", func(t *testing.T) {
		replace(t, &requirementsFile, "no table here\n")
		if code, stderr := run(t); code != 2 || !strings.Contains(stderr, "no capability table") {
			t.Fatalf("code %d: %s", code, stderr)
		}
	})
	t.Run("no go toolchain", func(t *testing.T) {
		standIns(t)
		if code, stderr := run(t); code != 1 || !strings.Contains(stderr, "go env") {
			t.Fatalf("code %d: %s", code, stderr)
		}
	})
}

func TestMainExitsWithRealMainsCode(t *testing.T) {
	var got []int
	replace(t, &exit, func(code int) { got = append(got, code) })
	replace(t, &os.Args, []string{"acceptance", "-unknown-flag"})
	main()
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("exit calls %v", got)
	}
}
