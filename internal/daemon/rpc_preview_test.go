package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/proto"
)

// inboxFingerprint hashes every file a mutation could write: queues, journals,
// credentials and route state.
func inboxFingerprint(t *testing.T, roots ...string) string {
	t.Helper()
	var out strings.Builder
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				fmt.Fprintf(&out, "%s/\n", path)
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&out, "%s %x\n", path, sha256.Sum256(data))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}

func planned(m proto.Message) *corev1.Plan {
	type planner interface{ GetPlan() *corev1.Plan }
	return m.(planner).GetPlan()
}

// previewCase is one mutation asked for a preview, in whose name it acts.
type previewCase struct {
	name string
	ctx  context.Context
	call func(context.Context, *messageService) (proto.Message, error)
}

// previewFixture is live state the previews act on, so that each is a request
// the call itself would accept.
type previewFixture struct {
	sum, rules          string
	claim, message      string
	emptyClaim          string
	adoptInbox, adopted string
	orchParty           context.Context
}

const protectedBoundary = `{"version":1,"inventory_current":true,"private_home":true,"human_checked":true,"protected_domain":true,"launch_admission":true,"courier_panes":[],"lower_trust":[]}`

func previewCases(ctx, party context.Context, f previewFixture, adj, orch *party) []previewCase {
	r := routeID("r")
	return []previewCase{
		{"Shutdown", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.Shutdown(c, &daemonv1.ShutdownRequest{Reason: "stop", DryRun: true})
		}},
		{"CreateRoute", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.CreateRoute(c, &daemonv1.CreateRouteRequest{Repo: "repo", Route: routeID("fresh"), DryRun: true})
		}},
		{"AdoptRoute", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.AdoptRoute(c, &daemonv1.AdoptRouteRequest{Route: routeID("elsewhere"), Inbox: f.adoptInbox, ConfirmPairSha256: f.adopted, DryRun: true})
		}},
		{"RemoveRoute", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.RemoveRoute(c, &daemonv1.RemoveRouteRequest{Route: r, DryRun: true})
		}},
		{"ResetEvents", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.ResetEvents(c, &daemonv1.ResetEventsRequest{Route: r, ConfirmRoute: "r", DryRun: true})
		}},
		{"PutSetup", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.PutSetup(c, &daemonv1.PutSetupRequest{Route: r, Rules: []string{f.rules}, PolicyVerified: true, BoundaryJson: protectedBoundary, DryRun: true})
		}},
		{"Enroll", party, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.Enroll(c, &daemonv1.EnrollRequest{Route: r, Name: adj.name, Role: adj.role, DryRun: true})
		}},
		{"ApprovePair", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.ApprovePair(c, &daemonv1.ApprovePairRequest{Route: r, Sha256: f.sum, DryRun: true})
		}},
		{"RetirePair", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.RetirePair(c, &daemonv1.RetirePairRequest{Route: r, Name: adj.name, Sha256: f.sum, DryRun: true})
		}},
		{"RevokeCredential", ctx, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.RevokeCredential(c, &daemonv1.RevokeCredentialRequest{Route: r, Name: adj.name, DryRun: true})
		}},
		{"Send", party, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.Send(c, &daemonv1.SendRequest{Route: r, To: orch.name, Kind: "brief", Body: "preview", Key: key(), DryRun: true})
		}},
		{"Claim", party, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.Claim(c, &daemonv1.ClaimRequest{Route: r, Token: f.claim, ExtendKey: key(), DryRun: true})
		}},
		{"Ack", party, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.Ack(c, &daemonv1.AckRequest{Route: r, Claim: f.claim, Id: wireMessageID(f.message), Record: "r", DryRun: true})
		}},
		{"Release", f.orchParty, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.Release(c, &daemonv1.ReleaseRequest{Route: r, Claim: f.emptyClaim, DryRun: true})
		}},
		{"Hold", party, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.Hold(c, &daemonv1.HoldRequest{Route: r, Target: orch.name, Reason: "why", DryRun: true})
		}},
		{"Unhold", party, func(c context.Context, s *messageService) (proto.Message, error) {
			return s.Unhold(c, &daemonv1.UnholdRequest{Route: r, Target: orch.name, DryRun: true})
		}},
	}
}

// Every mutation previews without touching what it would change, and answers
// only with its plan.
func TestAPreviewPlansAndChangesNothing(t *testing.T) {
	h := newHarness(t)
	adj, orch, inbox := h.route("r")
	s := &messageService{d: h.d}
	ctx := context.Background()
	party := partyContext(ctx, adj)
	f := previewFixture{sum: pendingChecksum(t, h), rules: h.rules, orchParty: partyContext(ctx, orch)}
	f.adoptInbox, f.adopted = fileInbox(t, h)
	r := routeID("r")
	if _, err := s.Send(f.orchParty, &daemonv1.SendRequest{Route: r, To: adj.name, Kind: "brief", Body: "work", Key: key()}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(party, &daemonv1.ClaimRequest{Route: r, ReadKey: key()})
	if err != nil {
		t.Fatal(err)
	}
	f.claim, f.message = claimed.GetClaim(), claimed.GetManifest()[0].GetId().GetValue()
	empty, err := s.Claim(f.orchParty, &daemonv1.ClaimRequest{Route: r, ReadKey: key()})
	if err != nil {
		t.Fatal(err)
	}
	f.emptyClaim = empty.GetClaim()
	if _, err := s.Hold(party, &daemonv1.HoldRequest{Route: r, Target: orch.name, Reason: "held"}); err != nil {
		t.Fatal(err)
	}
	before := inboxFingerprint(t, inbox, f.adoptInbox, h.d.routesFile())
	for _, c := range previewCases(ctx, party, f, adj, orch) {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.call(c.ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			plan := planned(res)
			if len(plan.GetSteps()) != 1 || plan.GetSteps()[0].GetAction() == "" || plan.GetSteps()[0].GetDescription() == "" {
				t.Fatalf("plan %v", plan)
			}
		})
	}
	if after := inboxFingerprint(t, inbox, f.adoptInbox, h.d.routesFile()); after != before {
		t.Fatalf("a preview changed the files:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	var routes []string
	h.d.mu.Lock()
	for name := range h.d.routes {
		routes = append(routes, name)
	}
	h.d.mu.Unlock()
	if len(routes) != 1 || routes[0] != "r" {
		t.Fatalf("routes %v", routes)
	}
}

// A preview refuses what the call would refuse, and tells nobody anything
// about a route without proof of who is asking.
func TestAPreviewRefusesAsTheCallWould(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	s := &messageService{d: h.d}
	ctx := context.Background()
	party := partyContext(ctx, adj)
	r, gone := routeID("r"), routeID("gone")
	cases := map[string]struct {
		call func() error
		want rpc.Code
	}{
		"bad reason": {func() error {
			_, err := s.Shutdown(ctx, &daemonv1.ShutdownRequest{Reason: "nap", DryRun: true})
			return err
		}, rpc.InvalidArgument},
		"bad name": {func() error {
			_, err := s.CreateRoute(ctx, &daemonv1.CreateRouteRequest{Repo: "a/b", Route: routeID("x"), DryRun: true})
			return err
		}, rpc.InvalidArgument},
		"taken": {func() error {
			_, err := s.CreateRoute(ctx, &daemonv1.CreateRouteRequest{Repo: "repo", Route: r, DryRun: true})
			return err
		}, rpc.AlreadyExists},
		"adopt bad name": {func() error {
			_, err := s.AdoptRoute(ctx, &daemonv1.AdoptRouteRequest{Route: routeID("a/b"), Inbox: "x", DryRun: true})
			return err
		}, rpc.InvalidArgument},
		"remove unknown": {func() error {
			_, err := s.RemoveRoute(ctx, &daemonv1.RemoveRouteRequest{Route: gone, DryRun: true})
			return err
		}, rpc.NotFound},
		"reset unconfirmed": {func() error {
			_, err := s.ResetEvents(ctx, &daemonv1.ResetEventsRequest{Route: r, ConfirmRoute: "other", DryRun: true})
			return err
		}, rpc.InvalidArgument},
		"reset unknown": {func() error {
			_, err := s.ResetEvents(ctx, &daemonv1.ResetEventsRequest{Route: gone, ConfirmRoute: "gone", DryRun: true})
			return err
		}, rpc.NotFound},
		"setup unknown": {func() error {
			_, err := s.PutSetup(ctx, &daemonv1.PutSetupRequest{Route: gone, DryRun: true})
			return err
		}, rpc.NotFound},
		"enroll unknown": {func() error {
			_, err := s.Enroll(party, &daemonv1.EnrollRequest{Route: gone, Name: "n", Role: "orchestrator", DryRun: true})
			return err
		}, rpc.NotFound},
		"enroll no pane": {func() error {
			_, err := s.Enroll(ctx, &daemonv1.EnrollRequest{Route: r, Name: "n", Role: "orchestrator", DryRun: true})
			return err
		}, rpc.Unauthenticated},
		"approve unknown": {func() error {
			_, err := s.ApprovePair(ctx, &daemonv1.ApprovePairRequest{Route: gone, DryRun: true})
			return err
		}, rpc.NotFound},
		"retire unknown": {func() error {
			_, err := s.RetirePair(ctx, &daemonv1.RetirePairRequest{Route: gone, DryRun: true})
			return err
		}, rpc.NotFound},
		"revoke unknown": {func() error {
			_, err := s.RevokeCredential(ctx, &daemonv1.RevokeCredentialRequest{Route: gone, DryRun: true})
			return err
		}, rpc.NotFound},
		"send without proof": {func() error {
			_, err := s.Send(ctx, &daemonv1.SendRequest{Route: r, To: orch.name, Kind: "brief", Body: "x", Key: key(), DryRun: true})
			return err
		}, rpc.Unauthenticated},
		"send unknown route": {func() error {
			_, err := s.Send(party, &daemonv1.SendRequest{Route: gone, To: orch.name, Kind: "brief", Body: "x", Key: key(), DryRun: true})
			return err
		}, rpc.NotFound},
		"send unknown peer": {func() error {
			_, err := s.Send(party, &daemonv1.SendRequest{Route: r, To: "nobody", Kind: "brief", Body: "x", Key: key(), DryRun: true})
			return err
		}, rpc.InvalidArgument},
		"claim without proof": {func() error {
			_, err := s.Claim(ctx, &daemonv1.ClaimRequest{Route: r, ReadKey: key(), DryRun: true})
			return err
		}, rpc.Unauthenticated},
		"ack without proof": {func() error {
			_, err := s.Ack(ctx, &daemonv1.AckRequest{Route: r, DryRun: true})
			return err
		}, rpc.Unauthenticated},
		"release without proof": {func() error {
			_, err := s.Release(ctx, &daemonv1.ReleaseRequest{Route: r, DryRun: true})
			return err
		}, rpc.Unauthenticated},
		"hold without proof": {func() error {
			_, err := s.Hold(ctx, &daemonv1.HoldRequest{Route: r, DryRun: true})
			return err
		}, rpc.Unauthenticated},
		"unhold without proof": {func() error {
			_, err := s.Unhold(ctx, &daemonv1.UnholdRequest{Route: r, DryRun: true})
			return err
		}, rpc.Unauthenticated},
		"resolve bad decision": {func() error {
			_, err := s.ResolveOperation(ctx, &daemonv1.ResolveOperationRequest{Id: "x", Decision: "maybe", DryRun: true})
			return err
		}, rpc.InvalidArgument},
		"resolve unknown": {func() error {
			_, err := s.ResolveOperation(ctx, &daemonv1.ResolveOperationRequest{Id: "x", Decision: "done", DryRun: true})
			return err
		}, rpc.NotFound},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := rpc.FromError(c.call()); got == nil || got.Code != c.want {
				t.Fatalf("got %v, want %s", got, c.want)
			}
		})
	}
}

// A send already committed under its key previews as that send, and a key
// reused for other content is refused.
func TestASendPreviewKnowsItsKey(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	s := &messageService{d: h.d}
	ctx := partyContext(context.Background(), adj)
	k := key()
	first, err := s.Send(ctx, &daemonv1.SendRequest{Route: routeID("r"), To: orch.name, Kind: "brief", Body: "x", Key: k})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Send(ctx, &daemonv1.SendRequest{Route: routeID("r"), To: orch.name, Kind: "brief", Body: "x", Key: k, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.GetPlan().GetSteps()[0].GetDescription(), first.GetId().GetValue()) {
		t.Fatalf("plan %v", res.GetPlan())
	}
	_, err = s.Send(ctx, &daemonv1.SendRequest{Route: routeID("r"), To: orch.name, Kind: "brief", Body: "other", Key: k, DryRun: true})
	if got := rpc.FromError(err); got == nil || got.Code != rpc.AlreadyExists {
		t.Fatalf("got %v", got)
	}
}

// Side-agent data goes through only under RELAY, in a preview as in the send.
func TestASideAgentPreviewFollowsTheAuthority(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	s := &messageService{d: h.d}
	ctx := partyContext(context.Background(), adj)
	preview := func() error {
		_, err := s.Send(ctx, &daemonv1.SendRequest{Route: routeID("r"), To: orch.name, Kind: "side_agent_data", Body: `{"version":1,"text":"x","source":{"pane":"p9","session":"s9"}}`, Key: key(), DryRun: true})
		return err
	}
	if got := rpc.FromError(preview()); got == nil || got.Code != rpc.FailedPrecondition {
		t.Fatalf("under HUMAN: %v", got)
	}
	h.do("PUT", "/v1/routes/r/setup", SetupRequest{Rules: []string{h.rules}, PolicyVerified: true, Boundary: json.RawMessage(protectedBoundary)}, nil)
	if err := preview(); err != nil {
		t.Fatalf("under RELAY: %v", err)
	}
}

func TestAClaimPreviewAcceptsAFirstRead(t *testing.T) {
	h := newHarness(t)
	adj, _, _ := h.route("r")
	s := &messageService{d: h.d}
	res, err := s.Claim(partyContext(context.Background(), adj), &daemonv1.ClaimRequest{Route: routeID("r"), ReadKey: key(), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetPlan().GetSteps()) != 1 || res.GetClaim() != "" {
		t.Fatalf("response %v", res)
	}
}
