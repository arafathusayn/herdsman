package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// A preview that cannot read the registry fails rather than plans on a guess.
func TestPreviewsFailWhenTheRegistryCannotBeRead(t *testing.T) {
	h := newHarness(t)
	_, _, inbox := h.route("r")
	if err := os.WriteFile(h.d.routesFile(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &messageService{d: h.d}
	ctx := context.Background()
	cases := map[string]func() error{
		"remove": func() error {
			_, err := s.RemoveRoute(ctx, &daemonv1.RemoveRouteRequest{Route: routeID("r"), DryRun: true})
			return err
		},
		"adopt": func() error {
			_, err := s.AdoptRoute(ctx, &daemonv1.AdoptRouteRequest{Route: routeID("elsewhere"), Inbox: inbox + "-other", DryRun: true})
			return err
		},
		"create": func() error {
			_, err := s.CreateRoute(ctx, &daemonv1.CreateRouteRequest{Repo: "repo", Route: routeID("fresh"), DryRun: true})
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if got := rpc.FromError(call()); got == nil || got.Code != rpc.Internal {
				t.Fatalf("got %v, want %s", got, rpc.Internal)
			}
		})
	}
}

func TestACreatePreviewRefusesARelativeSocket(t *testing.T) {
	h := newHarness(t)
	s := &messageService{d: h.d}
	_, err := s.CreateRoute(context.Background(), &daemonv1.CreateRouteRequest{Repo: "repo", Route: routeID("fresh"), HerdrSocket: "relative.sock", DryRun: true})
	if got := rpc.FromError(err); got == nil || got.Code != rpc.InvalidArgument {
		t.Fatalf("got %v, want %s", got, rpc.InvalidArgument)
	}
}

func TestAResolvePreviewPlansAKnownOperation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	stop := ""
	e := ops.NewEngine(db, ops.Options{Effects: map[string]ops.Effect{"unknown": unknownEffect{}}, Fault: func(p string) {
		if p == stop {
			panic("crash")
		}
	}})
	spec := ops.Spec{Kind: "k", Plan: "p", Steps: []ops.StepSpec{{Name: "s", Effect: "unknown", Key: "k1", Spec: "{}", Retry: ops.RetryNever}}}
	s := &messageService{d: &Daemon{ops: e}}
	leaveIntended(t, e, &stop, spec)
	if err := e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	listed, err := e.List()
	if err != nil || len(listed) != 1 {
		t.Fatalf("operations %v: %v", listed, err)
	}
	id := listed[0].ID
	res, err := s.ResolveOperation(ctx, &daemonv1.ResolveOperationRequest{Id: id, Decision: "rerun", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	steps := res.GetPlan().GetSteps()
	if len(steps) != 1 || !strings.Contains(steps[0].GetDescription(), "rerun") {
		t.Fatalf("plan %v", res.GetPlan())
	}
	if got, err := e.Get(id); err != nil || got.State != ops.OpStopped {
		t.Fatalf("a preview changed the operation: %v %v", got, err)
	}
	_, err = s.ResolveOperation(ctx, &daemonv1.ResolveOperationRequest{Id: id, Step: 5, Decision: "done", DryRun: true})
	if got := rpc.FromError(err); got == nil || got.Code != rpc.FailedPrecondition {
		t.Fatalf("a step the operation does not have: %v", got)
	}
}

// unknownEffect cannot tell whether it happened, so its step waits for a person.
type unknownEffect struct{}

func (unknownEffect) Apply(context.Context, ops.Step) error { return nil }

func (unknownEffect) Probe(context.Context, ops.Step) (ops.Verdict, string, error) {
	return ops.Unknown, "", nil
}

// admitSend refuses what send would refuse, before anything is written.
func TestSendAdmissionRefusals(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	ctx := context.Background()
	rt, apiErr := h.d.route("r")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	valid := func() SendRequest { return SendRequest{To: orch.name, Kind: "brief", Body: "b", Key: key()} }
	admit := func(from string, q SendRequest) *APIError {
		return rt.do(ctx, func() *APIError {
			_, _, _, e := rt.admitSend(from, q, false)
			return e
		})
	}
	withReply := valid()
	withReply.ReplyTo = "m000000000099"
	toNobody := valid()
	toNobody.To = "nobody"
	badKind := valid()
	badKind.Kind = "nonsense"
	for name, c := range map[string]struct {
		from string
		q    SendRequest
		want string
	}{
		"invalid request":  {adj.name, badKind, "bad_request"},
		"unapproved party": {"stranger", valid(), "unauthorized"},
		"unknown peer":     {adj.name, toNobody, "bad_request"},
		"unknown reply":    {adj.name, withReply, "bad_request"},
	} {
		t.Run(name, func(t *testing.T) {
			if e := admit(c.from, c.q); e == nil || e.Code != c.want {
				t.Fatalf("got %v, want %s", e, c.want)
			}
		})
	}
	t.Run("sequence exhausted", func(t *testing.T) {
		if e := rt.do(ctx, func() *APIError { rt.lastID = 999999999999; return nil }); e != nil {
			t.Fatal(e)
		}
		if e := admit(adj.name, valid()); e == nil || e.Code != "internal" {
			t.Fatalf("got %v, want internal", e)
		}
	})
}
