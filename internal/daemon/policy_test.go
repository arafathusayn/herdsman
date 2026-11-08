package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/policy"
)

var gateStart = time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)

func gateAt(wall time.Duration, elapsed time.Duration) *PolicyGate {
	return &PolicyGate{
		Load: func() (policy.Policy, error) { return policy.Policy{Budget: policy.Budget{Wall: wall}}, nil },
		Usage: func(policy.Operation) (policy.Usages, error) {
			return policy.Usages{Task: policy.Usage{Started: gateStart}}, nil
		},
		Now: func() time.Time { return gateStart.Add(elapsed) },
	}
}

func TestPolicyGateRefusesPastTheDeadlineAsAHumanBoundary(t *testing.T) {
	e := gateAt(time.Hour, 2*time.Hour).Admit(policy.Operation{Kind: "new", Task: "t"})
	if e == nil || e.Code != "human_boundary" || e.Retryable {
		t.Fatalf("got %+v", e)
	}
	if !strings.Contains(e.Message, "budget.wall") {
		t.Fatalf("message: %s", e.Message)
	}
}

func TestPolicyGateAdmitsBeforeTheDeadline(t *testing.T) {
	if e := gateAt(time.Hour, time.Minute).Admit(policy.Operation{Kind: "new", Task: "t"}); e != nil {
		t.Fatalf("got %+v", e)
	}
}

func TestPolicyGateFailsClosed(t *testing.T) {
	broken := errors.New("disk")
	ok := func() (policy.Policy, error) { return policy.Policy{}, nil }
	okUsage := func(policy.Operation) (policy.Usages, error) { return policy.Usages{}, nil }
	for name, gate := range map[string]*PolicyGate{
		"nil":        nil,
		"no loader":  {Usage: okUsage},
		"no usage":   {Load: ok},
		"load fails": {Load: func() (policy.Policy, error) { return policy.Policy{}, broken }, Usage: okUsage},
		"usage fails": {Load: ok, Usage: func(policy.Operation) (policy.Usages, error) {
			return policy.Usages{}, broken
		}},
	} {
		if e := gate.Admit(policy.Operation{Kind: "new"}); e == nil || e.Code != "internal" {
			t.Errorf("%s: got %+v", name, e)
		}
	}
}

func TestPolicyGateDefaultsToTheRealClock(t *testing.T) {
	gate := gateAt(time.Hour, 0)
	gate.Now = nil
	// The fixed start lies in the past of any real clock this test runs on.
	if e := gate.Admit(policy.Operation{Kind: "new", Task: "t"}); e == nil {
		t.Fatal("expected a refusal against the real clock")
	}
}

func TestAppendUsageWritesOneLinePerRecordPrivately(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "usage.jsonl")
	for _, tokens := range []int64{1, 2} {
		if err := AppendUsage(file, UsageRecord{Task: "t", Tokens: tokens, CostMicros: 3}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"tokens":1`) || !strings.Contains(lines[1], `"schema":"1.0.0"`) {
		t.Fatalf("lines: %q", lines)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info.Mode(), err)
	}
}

func TestAppendUsageRefusesWhatItCannotRecord(t *testing.T) {
	dir := t.TempDir()
	if err := AppendUsage(filepath.Join(dir, "u"), UsageRecord{Tokens: -1}); err == nil {
		t.Error("negative tokens accepted")
	}
	if err := AppendUsage(filepath.Join(dir, "u"), UsageRecord{CostMicros: -1}); err == nil {
		t.Error("negative cost accepted")
	}
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendUsage(filepath.Join(blocker, "u"), UsageRecord{}); err == nil {
		t.Error("parent that is a file accepted")
	}
	if err := AppendUsage(dir, UsageRecord{}); err == nil {
		t.Error("a directory as the log accepted")
	}
}
