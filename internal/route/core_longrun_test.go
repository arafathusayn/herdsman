package route_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/checkpoint/oncore"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// worldSource is the checkpointer's view of a core route in the fake world:
// the orchestrator's live execution and the world's notice service.
type worldSource struct {
	r   *coreRig
	svc *notice.Service
}

func (s worldSource) Current(ctx context.Context) (runtime.Execution, bool, error) {
	all, err := s.r.w.Records.List(ctx, s.r.repo, "harbor")
	if err != nil {
		return runtime.Execution{}, false, err
	}
	var out runtime.Execution
	for _, x := range all {
		if x.Role == "orchestrator" && x.Desired == runtime.DesiredRunning && x.Generation >= out.Generation {
			out = x
		}
	}
	return out, out.ID != "", nil
}

func (s worldSource) Issue(ctx context.Context, key string, e runtime.Execution, text string) (notice.Notice, error) {
	return s.svc.Issue(ctx, notice.IssueRequest{Key: key, Kind: notice.KindCheckpoint, Execution: e.ID, Content: text, Issuer: "checkpoint"})
}

func (s worldSource) ByKey(key string) (notice.Notice, bool, error) { return s.svc.ByKey(key) }

func (s worldSource) Emit(ev notice.Event) { s.svc.Emit(ev) }

// claudeTranscript appends a Claude Code transcript line whose usage is
// tokens, where the adapter's conversation evidence points.
func claudeTranscript(t *testing.T, home, cwd, sid string, tokens int64) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, sid+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, `{"type":"assistant","message":{"usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`+"\n", tokens)
}

// agent is an unattended agent's loop on core: its background notice wait
// wakes it, it claims the notice (a turn), works, and acknowledges. turn
// runs while the claim is open, so a test can act in the middle of a turn.
type agent struct {
	mu      sync.Mutex
	handled []string
}

func (a *agent) run(ctx context.Context, svc *notice.Service, e runtime.Execution, turn func(notice.Notice)) {
	var gen int64
	after := ""
	for ctx.Err() == nil {
		w, err := svc.Wait(ctx, e, notice.WaitRequest{WaitID: "bg-wait", Generation: gen, After: after})
		if err != nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if w.Answer == notice.AnswerReplaced || w.Answer == notice.AnswerStopped {
			return
		}
		gen = w.Generation
		if w.Answer != notice.AnswerNotice && w.Answer != notice.AnswerRecovery {
			continue
		}
		after = w.Notice.ID
		got, err := svc.Read(ctx, e, w.Notice.ID)
		if err != nil || got.Token == "" {
			continue
		}
		if turn != nil {
			turn(got.Notice)
		}
		if _, err := svc.Ack(ctx, e, w.Notice.ID, got.Token, notice.OutcomeCompleted, "done"); err == nil {
			a.mu.Lock()
			a.handled = append(a.handled, w.Notice.Kind)
			a.mu.Unlock()
		}
	}
}

func (a *agent) kinds() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.handled...)
}

// Long run with no person, in the fake world: an unattended Claude
// orchestrator handles its brief, gets an early checkpoint below its own
// compaction level as a notice, compacts by itself in the middle of that
// turn, is woken again by its background wait and claims its next notice;
// nothing is typed or keyed into core at any point.
func TestCoreLongRunWithNoPerson(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	q := r.request()
	q.NoAdjutant = true
	startCore(t, r, e, q)
	svc := r.w.Notices
	orch := r.execByRole(t, "orchestrator")
	// The adapter's conversation evidence, as the launch records it.
	orch.Conversation = &runtime.Conversation{Value: "sid-long", Provenance: "adapter"}
	if orch.Cwd == "" {
		orch.Cwd = r.repo
	}
	if _, err := r.w.Records.Put(context.Background(), orch); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	var clock sync.Mutex
	cp := &oncore.Checkpointer{Config: oncore.Config{Interval: 30 * time.Minute, Limit: 1_000_000, Kind: "claude", Home: home},
		Source: worldSource{r, svc}, Now: func() time.Time { clock.Lock(); defer clock.Unlock(); return now }}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	level := int64(200000-33000) - oncore.DefaultEarlyMargin
	compacted := make(chan struct{})
	var a agent
	go a.run(ctx, svc, orch, func(n notice.Notice) {
		if n.Kind == notice.KindCheckpoint {
			// The harness compacts by itself in the middle of this turn.
			claudeTranscript(t, home, orch.Cwd, "sid-long", 40000)
			close(compacted)
		}
	})
	waitUntil(t, "the brief to be handled", func() bool { return len(a.kinds()) == 1 })

	claudeTranscript(t, home, orch.Cwd, "sid-long", level)
	p, err := cp.Step(ctx)
	if err != nil || !p.Has(oncore.PassEarly) {
		t.Fatalf("the early checkpoint: %+v %v", p, err)
	}
	<-compacted
	waitUntil(t, "the checkpoint to be handled", func() bool { return len(a.kinds()) == 2 })
	if p, err = cp.Step(ctx); err != nil || !p.Has(oncore.PassDone) {
		t.Fatalf("the checkpoint's report: %+v %v", p, err)
	}
	if len(r.w.EventsOf(notice.EventNeedsAttention)) != 0 {
		for _, ev := range r.w.EventsOf(notice.EventNeedsAttention) {
			if ev.Reason == "compaction_due" {
				t.Errorf("a harness that compacts by itself raised %+v", ev)
			}
		}
	}

	// Woken again with nothing typed: the next notice reaches it through
	// its background wait.
	if _, err := svc.Issue(ctx, notice.IssueRequest{Key: "dispatch:long-1", Kind: notice.KindDispatch, Execution: orch.ID, Content: "carry on with the next task", Issuer: "test"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the next notice to be claimed", func() bool { return len(a.kinds()) == 3 })
	if got := a.kinds(); got[0] != notice.KindBrief || got[1] != notice.KindCheckpoint || got[2] != notice.KindDispatch {
		t.Fatalf("the agent handled %v", got)
	}
	for _, m := range []string{"terminal.send", "terminal.keys"} {
		if n := r.w.Core.Calls(m); n != 0 {
			t.Errorf("%s called %d times in a run with no person", m, n)
		}
	}
}
