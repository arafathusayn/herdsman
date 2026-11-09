package main

import (
	"testing"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// The first call spent the verdicts; the daemon refuses one on a finished step.
func TestConfirmedPairDoesNotResendTheVerdicts(t *testing.T) {
	req := &daemonv1.UpRequest{
		Caller:   &daemonv1.UpCaller{Pid: 7},
		Verdicts: []*daemonv1.StepVerdict{{Step: 1, Verdict: "done"}},
	}
	upConfirmedPair(req, "abc")
	if len(req.GetVerdicts()) != 0 {
		t.Errorf("verdicts sent again: %v", req.GetVerdicts())
	}
	if !req.GetContinue() || req.GetCaller().GetConfirmedChecksum() != "abc" || req.GetCaller().GetPid() != 7 {
		t.Errorf("request %v", req)
	}
}
