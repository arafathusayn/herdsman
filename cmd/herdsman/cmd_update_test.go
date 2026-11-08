package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/update"
)

// fakeUpdate replaces the update run and records its options.
func fakeUpdate(t *testing.T, err error) *update.Options {
	t.Helper()
	got := &update.Options{}
	old := updateRun
	updateRun = func(_ context.Context, o update.Options) error {
		*got = o
		return err
	}
	t.Cleanup(func() { updateRun = old })
	return got
}

func TestUpdateFlags(t *testing.T) {
	got := fakeUpdate(t, nil)
	var out, errOut bytes.Buffer
	if code := runUpdate([]string{"--check", "--version", "v1.2.3", "--allow-downgrade"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if !got.Check || got.Target != "1.2.3" || !got.AllowDowngrade || got.Current != version || got.Exe == "" || got.Smoke == nil || got.Stdout != &out {
		t.Fatalf("options %+v", got)
	}
}

func TestUpdateUsageErrors(t *testing.T) {
	fakeUpdate(t, nil)
	for _, args := range [][]string{{"extra"}, {"--nope"}} {
		var out, errOut bytes.Buffer
		if code := runUpdate(args, nil, &out, &errOut); code != 2 {
			t.Errorf("%v: code %d", args, code)
		}
	}
}

func TestUpdateRefusalExitsOne(t *testing.T) {
	fakeUpdate(t, update.ErrBadSignature)
	var out, errOut bytes.Buffer
	if code := runUpdate(nil, nil, &out, &errOut); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut.String(), "signature") {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestUpdateRealRunRefusesWithoutAKey(t *testing.T) {
	// The real wiring, cut off before the network: this build's key list is
	// used, and with no key the run must refuse.
	keys, err := update.TrustedKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) > 0 {
		t.Skip("this build carries a release key")
	}
	var out, errOut bytes.Buffer
	if code := runUpdate([]string{"--version", "1.2.3"}, nil, &out, &errOut); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut.String(), update.ErrNoKeys.Error()) {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestUpdateIsRegisteredWithHelp(t *testing.T) {
	c, ok := commands["update"]
	if !ok || !strings.Contains(c.help, "exit codes") {
		t.Fatalf("update command %+v", c)
	}
}
