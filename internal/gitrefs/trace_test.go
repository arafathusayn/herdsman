package gitrefs

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/arafathusayn/herdsman/internal/gitrun"
)

type recordingTrace struct {
	mu      sync.Mutex
	entries []gitrun.Entry
}

func (r *recordingTrace) Trace(e gitrun.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

// A fallback call is a git call like any other, so it reaches the trace.
func TestFallbackCallsReachTheTrace(t *testing.T) {
	reader := openRepo(t, newRepo(t, "sha1"))
	trace := &recordingTrace{}
	reader.SetRunner(&gitrun.Runner{Tracer: trace})
	if _, err := reader.AheadBehind(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	if len(trace.entries) != 1 || !slices.Contains(trace.entries[0].Args, "for-each-ref") {
		t.Fatalf("trace %+v", trace.entries)
	}
}

func TestFallbackFailureCarriesGitsMessage(t *testing.T) {
	reader := openRepo(t, newRepo(t, "sha1"))
	_, err := reader.AheadBehind(context.Background(), "no-such-branch")
	if err == nil || !strings.Contains(err.Error(), "gitrefs: git") || !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("error %v", err)
	}
}

func TestFallbackIgnoresAnInheritedNamespace(t *testing.T) {
	dir := newRepo(t, "sha1")
	want, err := openRepo(t, dir).AheadBehind(context.Background(), "main")
	if err != nil || len(want) == 0 {
		t.Fatalf("%v %v", want, err)
	}
	t.Setenv("GIT_NAMESPACE", "elsewhere")
	got, err := openRepo(t, dir).AheadBehind(context.Background(), "main")
	if err != nil || len(got) != len(want) {
		t.Fatalf("got %v, want %v, error %v", got, want, err)
	}
}
