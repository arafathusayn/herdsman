package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

func noDaemon(t *testing.T) {
	t.Helper()
	old := dialLedger
	t.Cleanup(func() { dialLedger = old })
	dialLedger = func() (daemonv1.LedgerService, func(), error) { return nil, nil, errors.New("no daemon") }
}

func TestLedgerVerbsReportNoDaemon(t *testing.T) {
	noDaemon(t)
	if code, _, errText := runVerb(runTeardown, "a", "-y"); code != 1 || !strings.Contains(errText, "no daemon") {
		t.Fatalf("teardown: %d %q", code, errText)
	}
	if code, _, errText := runVerb(runResource, "add", "--kind", "volume", "--id", "v"); code != 1 || !strings.Contains(errText, "no daemon") {
		t.Fatalf("resource: %d %q", code, errText)
	}
	if got := ledgerRunning(context.Background(), "/tree"); got != nil {
		t.Fatalf("running %v", got)
	}
	if _, err := runTeardownPlans("/dir", nil, 0, false); err == nil {
		t.Fatal("run")
	}
}

func TestTheRealDialReportsAMissingDaemon(t *testing.T) {
	t.Setenv("HERDSMAN_PRIVATE_HOME", t.TempDir())
	service, closeService, err := dialLedger()
	if err == nil {
		defer closeService()
		if _, err := service.LedgerHealth(context.Background(), &daemonv1.LedgerHealthRequest{Dir: "/tree"}); err == nil {
			t.Fatal("answered with no daemon")
		}
	}
}
