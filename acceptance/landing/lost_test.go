//go:build acceptance

// Package landing proves how a merge behaves when its answer never arrives. No
// verb lands a change yet, so the gate drives the GitHub backend directly,
// against a fake service reached through a fake gh.
package landing

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/landing"
	"github.com/arafathusayn/herdsman/internal/landing/ghtest"
	landinggithub "github.com/arafathusayn/herdsman/internal/landing/github"
)

const (
	repo      = "owner/repo"
	head      = "1111111111111111111111111111111111111111"
	mergeCall = "/merge-async"
)

// TestMain doubles as the fake gh the backend runs.
func TestMain(m *testing.M) {
	ghtest.Main()
	os.Exit(m.Run())
}

func TestLostResponseLandsOnce(t *testing.T) {
	for _, c := range []struct {
		name   string
		acted  bool
		merges int
		want   landing.MergeState
	}{
		{name: "the service acted", acted: true, merges: 1, want: landing.MergeMerged},
		{name: "the service did nothing", acted: false, merges: 0, want: landing.MergeUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := ghtest.New(t, repo)
			fake.AddPR(ghtest.PR{Number: 7, HeadRef: "feat/x", HeadSHA: head, BaseRef: "main", MergeableState: "clean"})
			backend, err := landinggithub.New(repo, landinggithub.Options{
				Bin: fake.Bin(), CallTimeout: 30 * time.Second, PollInterval: time.Millisecond, PollTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			fake.Inject(ghtest.Fault{Method: "PUT", Path: mergeCall, Apply: c.acted, Lost: true})

			result, err := backend.Merge(context.Background(), landing.MergeRequest{
				Number: 7, Head: head, Base: "main", Method: "squash", Title: "feat: x (#7)", Message: "body",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.State != c.want {
				t.Fatalf("state = %q (%s), want %q", result.State, result.Reason, c.want)
			}
			if n := fake.Merges(7); n != c.merges {
				t.Fatalf("merged %d times, want %d", n, c.merges)
			}
			sent := 0
			for _, call := range fake.Calls() {
				if call.Method == "PUT" && strings.Contains(call.Path, mergeCall) {
					sent++
				}
			}
			if sent != 1 {
				t.Fatalf("the merge was requested %d times", sent)
			}
		})
	}
}
