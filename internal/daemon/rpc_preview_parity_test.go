package daemon

import (
	"context"
	"testing"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// What the call refuses, its preview refuses the same way: a plan is a promise
// that the request is acceptable as the daemon stands.
func TestAPreviewRefusesWhatTheCallRefuses(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	s := &messageService{d: h.d}
	admin := context.Background()
	asAdj := partyContext(admin, adj)
	r := routeID("r")
	sum := pendingChecksum(t, h)
	validKey := key()

	cases := map[string]func(dry bool) error{
		"enroll with an invalid role": func(dry bool) error {
			_, err := s.Enroll(asAdj, &daemonv1.EnrollRequest{Route: r, Name: adj.name, Role: "emperor", DryRun: dry})
			return err
		},
		"approve with the wrong checksum": func(dry bool) error {
			_, err := s.ApprovePair(admin, &daemonv1.ApprovePairRequest{Route: r, Sha256: "wrong", DryRun: dry})
			return err
		},
		"approve with a bad credential option": func(dry bool) error {
			_, err := s.ApprovePair(admin, &daemonv1.ApprovePairRequest{Route: r, Sha256: sum, Credentials: map[string]string{"nobody": "file"}, DryRun: dry})
			return err
		},
		"retire with the wrong checksum": func(dry bool) error {
			_, err := s.RetirePair(admin, &daemonv1.RetirePairRequest{Route: r, Name: adj.name, Sha256: "wrong", DryRun: dry})
			return err
		},
		"retire an unknown name": func(dry bool) error {
			_, err := s.RetirePair(admin, &daemonv1.RetirePairRequest{Route: r, Name: "nobody", Sha256: sum, DryRun: dry})
			return err
		},
		"revoke an unknown name": func(dry bool) error {
			_, err := s.RevokeCredential(admin, &daemonv1.RevokeCredentialRequest{Route: r, Name: "nobody", DryRun: dry})
			return err
		},
		"claim with a malformed key": func(dry bool) error {
			_, err := s.Claim(asAdj, &daemonv1.ClaimRequest{Route: r, ReadKey: "short", DryRun: dry})
			return err
		},
		"extend an unknown claim": func(dry bool) error {
			_, err := s.Claim(asAdj, &daemonv1.ClaimRequest{Route: r, Token: validKey, ExtendKey: key(), DryRun: dry})
			return err
		},
		"ack a malformed id": func(dry bool) error {
			_, err := s.Ack(asAdj, &daemonv1.AckRequest{Route: r, Claim: validKey, Id: wireMessageID("!"), Record: "r", DryRun: dry})
			return err
		},
		"ack with no record": func(dry bool) error {
			_, err := s.Ack(asAdj, &daemonv1.AckRequest{Route: r, Claim: validKey, Id: wireMessageID("m000000000001"), DryRun: dry})
			return err
		},
		"ack an unknown claim": func(dry bool) error {
			_, err := s.Ack(asAdj, &daemonv1.AckRequest{Route: r, Claim: validKey, Id: wireMessageID("m000000000001"), Record: "r", DryRun: dry})
			return err
		},
		"release an unknown claim": func(dry bool) error {
			_, err := s.Release(asAdj, &daemonv1.ReleaseRequest{Route: r, Claim: validKey, DryRun: dry})
			return err
		},
		"hold an unknown target": func(dry bool) error {
			_, err := s.Hold(asAdj, &daemonv1.HoldRequest{Route: r, Target: "nobody", Reason: "why", DryRun: dry})
			return err
		},
		"unhold an unknown target": func(dry bool) error {
			_, err := s.Unhold(asAdj, &daemonv1.UnholdRequest{Route: r, Target: "nobody", DryRun: dry})
			return err
		},
		"unhold what the caller does not hold": func(dry bool) error {
			_, err := s.Unhold(asAdj, &daemonv1.UnholdRequest{Route: r, Target: orch.name, DryRun: dry})
			return err
		},
		"setup with no rules": func(dry bool) error {
			_, err := s.PutSetup(admin, &daemonv1.PutSetupRequest{Route: r, DryRun: dry})
			return err
		},
		"adopt an inbox that is not absolute": func(dry bool) error {
			_, err := s.AdoptRoute(admin, &daemonv1.AdoptRouteRequest{Route: routeID("elsewhere"), Inbox: "relative", ConfirmPairSha256: "x", DryRun: dry})
			return err
		},
		"resolve an unknown step": func(dry bool) error {
			_, err := s.ResolveOperation(admin, &daemonv1.ResolveOperationRequest{Id: "missing", Step: 0, Decision: "done", DryRun: dry})
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			callErr := call(false)
			if callErr == nil {
				t.Fatal("the call was expected to be refused")
			}
			real := rpc.FromError(callErr)
			previewErr := call(true)
			if previewErr == nil {
				t.Fatalf("the preview planned a call that is refused with %v", real)
			}
			preview := rpc.FromError(previewErr)
			if preview.Code != real.Code || preview.Message != real.Message {
				t.Fatalf("preview %v, call %v", preview, real)
			}
		})
	}
}

func pendingChecksum(t *testing.T, h *harness) string {
	t.Helper()
	rt, e := h.d.route("r")
	if e != nil {
		t.Fatal(e)
	}
	var sum string
	if e := rt.do(context.Background(), func() *APIError {
		pb, _ := rt.registryBytes("peers")
		sum = sha256Hex(pb)
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return sum
}
