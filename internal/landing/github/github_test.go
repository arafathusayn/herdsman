package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/landing"
	"github.com/arafathusayn/herdsman/internal/landing/ghtest"
)

func TestMain(m *testing.M) {
	ghtest.Main()
	os.Exit(m.Run())
}

const repo = "acme/widgets"

var (
	head  = strings.Repeat("a", 40)
	moved = strings.Repeat("b", 40)
)

const mergePath = "/merge-async"

func setup(t *testing.T) (*ghtest.Fake, *Backend) {
	t.Helper()
	f := ghtest.New(t, repo)
	f.AddPR(ghtest.PR{Number: 7, HeadRef: "feat/x", HeadSHA: head, BaseRef: "main", MergeableState: "clean"})
	b, err := New(repo, Options{Bin: f.Bin(), CallTimeout: 10 * time.Second, PollInterval: time.Millisecond, PollTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return f, b
}

func request() landing.MergeRequest {
	return landing.MergeRequest{Number: 7, Head: head, Method: "squash", Title: "feat: x (#7)", Message: "body"}
}

func merge(t *testing.T, b *Backend) landing.MergeResult {
	t.Helper()
	res, err := b.Merge(context.Background(), request())
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return res
}

func wantState(t *testing.T, got landing.MergeResult, want landing.MergeState) {
	t.Helper()
	if got.State != want {
		t.Fatalf("state = %q (%s), want %q", got.State, got.Reason, want)
	}
}

// wantMergeCounts pins the property the backend exists for: requests sent and merges made.
func wantMergeCounts(t *testing.T, f *ghtest.Fake, puts, merges int) {
	t.Helper()
	if got := f.Count("PUT", mergePath); got != puts {
		t.Errorf("merge requests = %d, want %d", got, puts)
	}
	if got := f.Merges(7); got != merges {
		t.Errorf("merges = %d, want %d", got, merges)
	}
}

func TestMergeSendsOnePinnedDirectRequest(t *testing.T) {
	f, b := setup(t)
	res := merge(t, b)
	wantState(t, res, landing.MergeMerged)
	if res.Commit == "" {
		t.Error("no merge commit")
	}
	wantMergeCounts(t, f, 1, 1)
	for _, c := range f.Calls() {
		if c.Method != "PUT" {
			continue
		}
		var sent map[string]any
		if err := json.Unmarshal(c.Body, &sent); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"merge_method": "squash", "merge_action": "direct_merge", "sha": head, "commit_title": "feat: x (#7)", "commit_message": "body"}
		if fmt.Sprint(sent) != fmt.Sprint(want) {
			t.Errorf("sent %v, want %v", sent, want)
		}
	}
}

func TestMergeOmitsEmptyTitleAndMessage(t *testing.T) {
	f, b := setup(t)
	req := request()
	req.Title, req.Message = "", ""
	if _, err := b.Merge(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls() {
		if c.Method == "PUT" && strings.Contains(string(c.Body), "commit_") {
			t.Errorf("sent %s", c.Body)
		}
	}
}

func TestMergeWaitsThroughPending(t *testing.T) {
	f, b := setup(t)
	f.Ticks = 5
	wantState(t, merge(t, b), landing.MergeMerged)
	wantMergeCounts(t, f, 1, 1)
}

func TestMergeBeforeSending(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*ghtest.Fake)
		want    landing.MergeState
	}{
		{"already merged at the head", func(f *ghtest.Fake) { f.MergeNow(7) }, landing.MergeMerged},
		{"merged at another head", func(f *ghtest.Fake) { f.PushHead(7, moved); f.MergeNow(7) }, landing.MergeConflict},
		{"head moved", func(f *ghtest.Fake) { f.PushHead(7, moved) }, landing.MergeHeadMoved},
		{"closed", func(f *ghtest.Fake) { f.Close(7) }, landing.MergeFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := setup(t)
			tt.prepare(f)
			before := f.Merges(7)
			wantState(t, merge(t, b), tt.want)
			wantMergeCounts(t, f, 0, before)
		})
	}
}

func TestMergeHeadMovesAfterThePreRead(t *testing.T) {
	f, b := setup(t)
	f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Apply: true, Before: func() { f.PushHead(7, moved) }})
	wantState(t, merge(t, b), landing.MergeHeadMoved)
	wantMergeCounts(t, f, 1, 0)
}

func TestMergeHeadMovesWhileAccepted(t *testing.T) {
	f, b := setup(t)
	f.Hold = true
	f.Inject(ghtest.Fault{Method: "GET", Path: "/merge-async/", Apply: true, Before: func() { f.PushHead(7, moved); f.Release() }})
	wantState(t, merge(t, b), landing.MergeHeadMoved)
	wantMergeCounts(t, f, 1, 0)
}

func TestMergeLostResponseAfterTheServiceActed(t *testing.T) {
	f, b := setup(t)
	f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Apply: true, Lost: true})
	wantState(t, merge(t, b), landing.MergeMerged)
	wantMergeCounts(t, f, 1, 1)
}

func TestMergeLostResponseAfterNothingHappened(t *testing.T) {
	f, b := setup(t)
	f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Lost: true})
	wantState(t, merge(t, b), landing.MergeUnknown)
	wantMergeCounts(t, f, 1, 0)
}

func TestMergeSlowBodyKilledAtTheTimeout(t *testing.T) {
	f := ghtest.New(t, repo)
	f.AddPR(ghtest.PR{Number: 7, HeadRef: "feat/x", HeadSHA: head, BaseRef: "main"})
	f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Apply: true, Hang: true})
	b, err := New(repo, Options{Bin: f.Bin(), CallTimeout: time.Second, PollInterval: time.Millisecond, PollTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, merge(t, b), landing.MergeMerged)
	wantMergeCounts(t, f, 1, 1)
}

func TestMergeAnswersThatProveNothingReconcile(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"garbage", 200, "not json"},
		{"accepted without a uuid", 202, `{"status":"pending"}`},
		{"server error", 502, `{"message":"bad gateway"}`},
		{"timeout", 408, `{}`},
		{"rate limit", 429, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := setup(t)
			f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Apply: true, Status: tt.status, Body: tt.body})
			wantState(t, merge(t, b), landing.MergeMerged)
			wantMergeCounts(t, f, 1, 1)
		})
	}
}

func TestMergeRefused(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{"failed", 400, `{"status":"failed","details":{"message":"draft"}}`, "draft"},
		{"forbidden", 403, `{"message":"no access"}`, "no access"},
		{"unreadable", 404, `nope`, "HTTP 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := setup(t)
			f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Status: tt.status, Body: tt.body})
			res := merge(t, b)
			wantState(t, res, landing.MergeFailed)
			if !strings.Contains(res.Reason, tt.reason) {
				t.Errorf("reason %q lacks %q", res.Reason, tt.reason)
			}
			wantMergeCounts(t, f, 1, 0)
		})
	}
}

func TestMergeAdoptsAMatchingPendingRequest(t *testing.T) {
	f, b := setup(t)
	f.Hold = true
	f.Pending(7, "other-uuid", head, "squash", "direct_merge", false)
	f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Apply: true, Before: f.Release})
	res := merge(t, b)
	wantState(t, res, landing.MergeMerged)
	wantMergeCounts(t, f, 1, 1)
}

func TestMergeRefusesAnotherPendingRequest(t *testing.T) {
	tests := []struct {
		name   string
		head   string
		method string
		action string
		bypass bool
	}{
		{"other head", moved, "squash", "direct_merge", false},
		{"other method", head, "rebase", "direct_merge", false},
		{"queue", head, "squash", "merge_queue", false},
		{"bypass", head, "squash", "direct_merge", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := setup(t)
			f.Hold = true
			f.Pending(7, "other-uuid", tt.head, tt.method, tt.action, tt.bypass)
			wantState(t, merge(t, b), landing.MergeConflict)
			wantMergeCounts(t, f, 1, 0)
		})
	}
}

func TestMergeUnreadableConflictIsNotAdopted(t *testing.T) {
	f, b := setup(t)
	f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Status: 409, Body: "oops"})
	wantState(t, merge(t, b), landing.MergeConflict)
	wantMergeCounts(t, f, 1, 0)
}

func TestMergeAlreadyEnqueuedIsAConflict(t *testing.T) {
	f, b := setup(t)
	f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Status: 200, Body: `{"status":"enqueued"}`})
	wantState(t, merge(t, b), landing.MergeConflict)
	wantMergeCounts(t, f, 1, 0)
}

func TestMergeFailedInTheBackground(t *testing.T) {
	f, b := setup(t)
	f.FailMerge = "required checks failed"
	res := merge(t, b)
	wantState(t, res, landing.MergeFailed)
	if res.Reason != "required checks failed" {
		t.Errorf("reason = %q", res.Reason)
	}
	wantMergeCounts(t, f, 1, 0)
}

func TestMergeSurvivesAFailedPoll(t *testing.T) {
	f, b := setup(t)
	f.Inject(ghtest.Fault{Method: "PUT", Path: mergePath, Apply: true, Before: func() {
		f.Inject(ghtest.Fault{Method: "GET", Path: "/merge-async/", Status: 500, Body: "{}"})
		f.Inject(ghtest.Fault{Method: "GET", Path: "/pulls/7", Lost: true})
	}})
	wantState(t, merge(t, b), landing.MergeMerged)
	wantMergeCounts(t, f, 1, 1)
}

func TestMergePendingAtTheDeadlineThenReconciled(t *testing.T) {
	f, b := setup(t)
	f.Hold = true
	res := merge(t, b)
	wantState(t, res, landing.MergePending)
	if res.UUID == "" {
		t.Fatal("no uuid to reconcile with")
	}
	f.Release()
	again, err := b.Reconcile(context.Background(), landing.MergeRef{Number: 7, Head: head, UUID: res.UUID})
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, again, landing.MergeMerged)
	wantMergeCounts(t, f, 1, 1)
}

func TestMergeCancelledWhileWaiting(t *testing.T) {
	f, b := setup(t)
	f.Hold = true
	b.opt.PollInterval = time.Hour
	b.opt.PollTimeout = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := b.Merge(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	wantState(t, res, landing.MergePending)
	wantMergeCounts(t, f, 1, 0)
}

func TestMergeSendsNothingWhenThePreReadFails(t *testing.T) {
	bad := []string{"Lost", `{}`, `{"number":7,"head":{"sha":"x"},"state":"closed","merged":true}`, "not json"}
	for _, body := range bad {
		t.Run(body, func(t *testing.T) {
			f, b := setup(t)
			fault := ghtest.Fault{Method: "GET", Path: "/pulls/7", Status: 200, Body: body}
			if body == "Lost" {
				fault = ghtest.Fault{Method: "GET", Path: "/pulls/7", Lost: true}
			}
			f.Inject(fault)
			if _, err := b.Merge(context.Background(), request()); err == nil {
				t.Fatal("no error")
			}
			wantMergeCounts(t, f, 0, 0)
		})
	}
}

func TestMergeRejectsAnInvalidRequest(t *testing.T) {
	f, b := setup(t)
	for name, mutate := range map[string]func(*landing.MergeRequest){
		"number":     func(r *landing.MergeRequest) { r.Number = 0 },
		"short head": func(r *landing.MergeRequest) { r.Head = "abc123" },
		"bad method": func(r *landing.MergeRequest) { r.Method = "fast" },
		"upper head": func(r *landing.MergeRequest) { r.Head = strings.Repeat("A", 40) },
		"empty head": func(r *landing.MergeRequest) { r.Head = "" },
		"no method":  func(r *landing.MergeRequest) { r.Method = "" },
	} {
		t.Run(name, func(t *testing.T) {
			req := request()
			mutate(&req)
			if _, err := b.Merge(context.Background(), req); err == nil {
				t.Fatal("no error")
			}
		})
	}
	if len(f.Calls()) != 0 {
		t.Errorf("calls = %v", f.Calls())
	}
}

func TestReconcile(t *testing.T) {
	uuidReply := func(body string) ghtest.Fault {
		return ghtest.Fault{Method: "GET", Path: "/merge-async/", Always: true, Status: 200, Body: body}
	}
	tests := []struct {
		name   string
		uuid   string
		faults []ghtest.Fault
		want   landing.MergeState
		commit string
	}{
		{"no uuid and open", "", nil, landing.MergeUnknown, ""},
		{"pr unreadable", "u", []ghtest.Fault{{Method: "GET", Path: "/pulls/7", Always: true, Lost: true}}, landing.MergeUnknown, ""},
		{"uuid pending", "u", []ghtest.Fault{uuidReply(`{"status":"pending"}`)}, landing.MergePending, ""},
		{"uuid merged before the pr shows it", "u", []ghtest.Fault{uuidReply(`{"status":"merged","details":{"sha":"c0"}}`)}, landing.MergeMerged, "c0"},
		{"uuid merged without a commit", "u", []ghtest.Fault{uuidReply(`{"status":"merged"}`)}, landing.MergePending, ""},
		{"uuid enqueued", "u", []ghtest.Fault{uuidReply(`{"status":"enqueued"}`)}, landing.MergeConflict, ""},
		{"uuid failed", "u", []ghtest.Fault{uuidReply(`{"status":"failed","details":{"message":"no"}}`)}, landing.MergeFailed, ""},
		{"uuid unknown status", "u", []ghtest.Fault{uuidReply(`{"status":"odd"}`)}, landing.MergePending, ""},
		{"uuid garbage", "u", []ghtest.Fault{uuidReply(`nope`)}, landing.MergePending, ""},
		{"uuid gone", "u", []ghtest.Fault{{Method: "GET", Path: "/merge-async/", Always: true, Status: 404, Body: "{}"}}, landing.MergeUnknown, ""},
		{"uuid server error", "u", []ghtest.Fault{{Method: "GET", Path: "/merge-async/", Always: true, Status: 500, Body: "{}"}}, landing.MergePending, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := setup(t)
			for _, fault := range tt.faults {
				f.Inject(fault)
			}
			res, err := b.Reconcile(context.Background(), landing.MergeRef{Number: 7, Head: head, UUID: tt.uuid})
			if err != nil {
				t.Fatal(err)
			}
			wantState(t, res, tt.want)
			if res.Commit != tt.commit {
				t.Errorf("commit = %q", res.Commit)
			}
			if res.UUID != tt.uuid {
				t.Errorf("uuid = %q", res.UUID)
			}
			wantMergeCounts(t, f, 0, 0)
		})
	}
}

func TestReconcileRejectsAnInvalidRef(t *testing.T) {
	_, b := setup(t)
	if _, err := b.Reconcile(context.Background(), landing.MergeRef{Number: 7, Head: "abc"}); err == nil {
		t.Fatal("no error")
	}
}

func strict(on bool) map[string]any {
	return map[string]any{"type": "required_status_checks", "parameters": map[string]any{"strict_required_status_checks_policy": on}}
}

var queueRule = map[string]any{"type": "merge_queue", "parameters": map[string]any{"merge_method": "SQUASH"}}

func filler(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = map[string]any{"type": "deletion"}
	}
	return out
}

func TestRules(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name       string
		rulesets   []map[string]any
		protection *bool
		want       landing.Rules
	}{
		{"no rule anywhere", nil, nil, landing.Rules{Contract: landing.ContractDelegated}},
		{"ruleset strict", []map[string]any{strict(true)}, nil, landing.Rules{Contract: landing.ContractExact, UpToDate: true}},
		{"ruleset not strict", []map[string]any{strict(false)}, nil, landing.Rules{Contract: landing.ContractDelegated}},
		{"protection strict", nil, &yes, landing.Rules{Contract: landing.ContractExact, UpToDate: true}},
		{"protection not strict", nil, &no, landing.Rules{Contract: landing.ContractDelegated}},
		{"queue beats strict", []map[string]any{strict(true), queueRule}, nil, landing.Rules{Contract: landing.ContractDelegated, UpToDate: true, MergeQueue: true}},
		{"strict on the second page", append(filler(100), strict(true)), nil, landing.Rules{Contract: landing.ContractExact, UpToDate: true}},
		{"queue on the second page", append(append(filler(99), strict(true)), queueRule), nil, landing.Rules{Contract: landing.ContractDelegated, UpToDate: true, MergeQueue: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, b := setup(t)
			f.Rulesets["feat/base"] = tt.rulesets
			if tt.protection != nil {
				f.Protection["feat/base"] = *tt.protection
			}
			got, err := b.Rules(context.Background(), "feat/base")
			if err != nil {
				t.Fatal(err)
			}
			if got.Contract != tt.want.Contract || got.UpToDate != tt.want.UpToDate || got.MergeQueue != tt.want.MergeQueue {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if got.Contract == landing.ContractDelegated && got.Reason == "" {
				t.Error("delegated without a reason")
			}
		})
	}
}

func TestRulesUnreadableProtectionIsDelegated(t *testing.T) {
	f, b := setup(t)
	f.Inject(ghtest.Fault{Path: "/protection/", Status: 403, Body: `{"message":"admin rights"}`})
	got, err := b.Rules(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Contract != landing.ContractDelegated || !strings.Contains(got.Reason, "admin rights") {
		t.Errorf("got %+v", got)
	}
}

func TestRulesPartialReadIsAnError(t *testing.T) {
	for name, fault := range map[string]ghtest.Fault{
		"later page fails":   {Path: "/rules/branches/", Page: 2, Status: 502, Body: "{}"},
		"later page lost":    {Path: "/rules/branches/", Page: 2, Lost: true},
		"later page garbage": {Path: "/rules/branches/", Page: 2, Status: 200, Body: "nope"},
		"null page":          {Path: "/rules/branches/", Page: 1, Status: 200, Body: "null"},
		"protection 500":     {Path: "/protection/", Status: 500, Body: "{}"},
		"protection garbage": {Path: "/protection/", Status: 200, Body: "nope"},
		"protection no flag": {Path: "/protection/", Status: 200, Body: "{}"},
	} {
		t.Run(name, func(t *testing.T) {
			f, b := setup(t)
			f.Rulesets["main"] = append(filler(100), strict(false))
			f.Inject(fault)
			if got, err := b.Rules(context.Background(), "main"); err == nil {
				t.Fatalf("no error, got %+v", got)
			}
		})
	}
}

func TestRulesStopsAtThePageLimit(t *testing.T) {
	f, b := setup(t)
	defer func(n int) { maxPages = n }(maxPages)
	maxPages = 2
	f.Rulesets["main"] = filler(250)
	if _, err := b.Rules(context.Background(), "main"); err == nil {
		t.Fatal("no error")
	}
}

func TestRulesRejectsAnEmptyBase(t *testing.T) {
	_, b := setup(t)
	if _, err := b.Rules(context.Background(), ""); err == nil {
		t.Fatal("no error")
	}
}

func TestRulesEscapesTheBranch(t *testing.T) {
	f, b := setup(t)
	if _, err := b.Rules(context.Background(), "a/{owner}"); err != nil {
		t.Fatal(err)
	}
	if got := f.Count("GET", "/rules/branches/a%2F%7Bowner%7D"); got != 1 {
		t.Errorf("escaped path calls = %d: %v", got, f.Calls())
	}
}

func run(name, status, conclusion string) map[string]any {
	return map[string]any{"name": name, "status": status, "conclusion": conclusion}
}

func statusEntry(context, state string) map[string]any {
	return map[string]any{"context": context, "state": state}
}

func TestStatusReadsEveryCheck(t *testing.T) {
	f, b := setup(t)
	runs := make([]map[string]any, 0, 106)
	for i := range 100 {
		runs = append(runs, run(fmt.Sprintf("job-%d", i), "completed", "success"))
	}
	runs = append(runs, run("neutral", "completed", "neutral"), run("skipped", "completed", "skipped"),
		run("queued", "in_progress", ""), run("broken", "completed", "failure"))
	f.CheckRuns[head] = runs
	f.Statuses[head] = []map[string]any{statusEntry("ci/a", "success"), statusEntry("ci/b", "pending"), statusEntry("ci/c", "error")}
	got, err := b.Status(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "open" || got.HeadRef != "feat/x" || got.HeadSHA != head || got.BaseRef != "main" || got.MergeableState != "clean" || got.MergeCommit != "" {
		t.Errorf("got %+v", got)
	}
	if len(got.Checks) != 107 {
		t.Fatalf("checks = %d", len(got.Checks))
	}
	want := map[string]landing.CheckState{
		"job-0": landing.CheckPassed, "neutral": landing.CheckPassed, "skipped": landing.CheckPassed, "queued": landing.CheckPending,
		"broken": landing.CheckFailed, "ci/a": landing.CheckPassed, "ci/b": landing.CheckPending, "ci/c": landing.CheckFailed,
	}
	for _, c := range got.Checks {
		if state, ok := want[c.Name]; ok && state != c.State {
			t.Errorf("%s = %s, want %s", c.Name, c.State, state)
		}
	}
}

func TestStatusMerged(t *testing.T) {
	f, b := setup(t)
	f.MergeNow(7)
	got, err := b.Status(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "merged" || got.MergeCommit == "" {
		t.Errorf("got %+v", got)
	}
}

func TestStatusPartialReadIsAnError(t *testing.T) {
	for name, fault := range map[string]ghtest.Fault{
		"pr":                {Path: "/pulls/7", Lost: true},
		"check page 2":      {Path: "/check-runs", Page: 2, Status: 500, Body: "{}"},
		"status page 1":     {Path: "/status", Page: 1, Status: 500, Body: "{}"},
		"checks no field":   {Path: "/check-runs", Page: 1, Status: 200, Body: "{}"},
		"checks garbage":    {Path: "/check-runs", Page: 1, Status: 200, Body: "nope"},
		"statuses no field": {Path: "/status", Page: 1, Status: 200, Body: "{}"},
		"statuses garbage":  {Path: "/status", Page: 1, Status: 200, Body: "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			f, b := setup(t)
			f.CheckRuns[head] = make([]map[string]any, 150)
			for i := range f.CheckRuns[head] {
				f.CheckRuns[head][i] = run("j", "completed", "success")
			}
			f.Inject(fault)
			if got, err := b.Status(context.Background(), 7); err == nil {
				t.Fatalf("no error, got %+v", got)
			}
		})
	}
}

func TestStatusRejectsAnInvalidNumber(t *testing.T) {
	_, b := setup(t)
	if _, err := b.Status(context.Background(), 0); err == nil {
		t.Fatal("no error")
	}
}

func openRequest() landing.OpenRequest {
	return landing.OpenRequest{Head: "feat/new", Base: "main", Title: "feat: new", Body: "why", Draft: true}
}

func TestOpenCreates(t *testing.T) {
	f, b := setup(t)
	pr, err := b.Open(context.Background(), openRequest())
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number == 0 || pr.HeadRef != "feat/new" || pr.BaseRef != "main" || !pr.Draft || pr.URL == "" || pr.HeadSHA == "" {
		t.Errorf("got %+v", pr)
	}
	if got := f.Count("POST", "/pulls"); got != 1 {
		t.Errorf("creates = %d", got)
	}
}

func TestOpenReturnsTheExistingPullRequest(t *testing.T) {
	f, b := setup(t)
	req := openRequest()
	req.Head = "feat/x"
	pr, err := b.Open(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 {
		t.Errorf("got %+v", pr)
	}
	if got := f.Count("POST", "/pulls"); got != 0 {
		t.Errorf("creates = %d", got)
	}
}

func TestOpenLostResponseFindsTheOneItMade(t *testing.T) {
	for name, fault := range map[string]ghtest.Fault{
		"lost":    {Method: "POST", Path: "/pulls", Apply: true, Lost: true},
		"garbage": {Method: "POST", Path: "/pulls", Apply: true, Status: 201, Body: "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			f, b := setup(t)
			f.Inject(fault)
			pr, err := b.Open(context.Background(), openRequest())
			if err != nil {
				t.Fatal(err)
			}
			if pr.HeadRef != "feat/new" {
				t.Errorf("got %+v", pr)
			}
			if got := f.Count("POST", "/pulls"); got != 1 {
				t.Errorf("creates = %d", got)
			}
		})
	}
}

func TestOpenRaceWithAnotherClient(t *testing.T) {
	f, b := setup(t)
	f.Inject(ghtest.Fault{Method: "POST", Path: "/pulls", Apply: true, Before: func() {
		f.AddPR(ghtest.PR{Number: 9, HeadRef: "feat/new", BaseRef: "main", HeadSHA: head})
	}})
	pr, err := b.Open(context.Background(), openRequest())
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 9 {
		t.Errorf("got %+v", pr)
	}
}

func TestOpenFailures(t *testing.T) {
	list := func(f ghtest.Fault) ghtest.Fault { f.Method, f.Path = "GET", "/pulls"; return f }
	post := func(f ghtest.Fault) ghtest.Fault { f.Method, f.Path = "POST", "/pulls"; return f }
	for name, faults := range map[string][]ghtest.Fault{
		"list fails":           {list(ghtest.Fault{Lost: true})},
		"list garbage":         {list(ghtest.Fault{Status: 200, Body: "nope"})},
		"list bad item":        {list(ghtest.Fault{Status: 200, Body: "[{}]"})},
		"refused":              {post(ghtest.Fault{Status: 422, Body: `{"message":"no commits"}`})},
		"lost and not created": {post(ghtest.Fault{Lost: true})},
		"lookup after create":  {post(ghtest.Fault{Lost: true}), {Method: "GET", Path: "/pulls", Page: 0, Status: 200, Body: "[]"}, list(ghtest.Fault{Lost: true})},
	} {
		t.Run(name, func(t *testing.T) {
			f, b := setup(t)
			for _, fault := range faults {
				f.Inject(fault)
			}
			if pr, err := b.Open(context.Background(), openRequest()); err == nil {
				t.Fatalf("no error, got %+v", pr)
			}
			if name != "list fails" && name != "list garbage" && name != "list bad item" && f.Count("POST", "/pulls") != 1 {
				t.Errorf("creates = %d, want 1", f.Count("POST", "/pulls"))
			}
		})
	}
}

func TestOpenRejectsAnInvalidRequest(t *testing.T) {
	f, b := setup(t)
	for name, mutate := range map[string]func(*landing.OpenRequest){
		"head": func(r *landing.OpenRequest) { r.Head = "" }, "base": func(r *landing.OpenRequest) { r.Base = "" }, "title": func(r *landing.OpenRequest) { r.Title = "" },
	} {
		t.Run(name, func(t *testing.T) {
			req := openRequest()
			mutate(&req)
			if _, err := b.Open(context.Background(), req); err == nil {
				t.Fatal("no error")
			}
		})
	}
	if len(f.Calls()) != 0 {
		t.Errorf("calls = %v", f.Calls())
	}
}

func TestNew(t *testing.T) {
	for _, bad := range []string{"", "acme", "a/b/c", "a b/c", "../x"} {
		if _, err := New(bad, Options{}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	b, err := New(repo, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.opt.Bin != "gh" || b.opt.CallTimeout <= 0 || b.opt.PollInterval <= 0 || b.opt.PollTimeout <= 0 {
		t.Errorf("defaults = %+v", b.opt)
	}
}

func TestAccountTokenIsFetchedOnceAndSent(t *testing.T) {
	f := ghtest.New(t, repo)
	f.AddPR(ghtest.PR{Number: 7, HeadRef: "feat/x", HeadSHA: head, BaseRef: "main"})
	b, err := New(repo, Options{Bin: f.Bin(), Account: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := b.Status(context.Background(), 7); err != nil {
			t.Fatal(err)
		}
	}
	auth := 0
	for _, c := range f.Calls() {
		switch c.Args[0] {
		case "auth":
			auth++
			if strings.Join(c.Args, " ") != "auth token --user bot" {
				t.Errorf("args = %v", c.Args)
			}
		default:
			if c.Token != "fake-token" {
				t.Errorf("token = %q on %v", c.Token, c.Args)
			}
		}
	}
	if auth != 1 {
		t.Errorf("token fetched %d times", auth)
	}
}

func TestMissingAccountTokenSendsNothing(t *testing.T) {
	f := ghtest.New(t, repo)
	f.Token = ""
	b, err := New(repo, Options{Bin: f.Bin(), Account: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Merge(context.Background(), request()); err == nil {
		t.Fatal("no error")
	}
	if f.Count("PUT", mergePath) != 0 {
		t.Error("merge sent")
	}
}

func TestMissingBinaryIsAnError(t *testing.T) {
	b, err := New(repo, Options{Bin: "/nonexistent/gh"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Status(context.Background(), 7); err == nil {
		t.Fatal("no error")
	}
}

func TestFailurePrefersStderrThenTheError(t *testing.T) {
	for _, tt := range []struct {
		err    error
		stderr string
		want   string
	}{
		{errors.New("e"), " text\n", "text"},
		{errors.New("e"), "", "e"},
		{nil, "", "fallback"},
	} {
		if got := failure(tt.err, tt.stderr, "fallback"); got != tt.want {
			t.Errorf("failure(%v, %q) = %q", tt.err, tt.stderr, got)
		}
	}
}

func TestParseHTTP(t *testing.T) {
	tests := []struct {
		in     string
		status int
		body   string
		ok     bool
	}{
		{"HTTP/2.0 200 OK\r\nA: b\r\n\r\n{}", 200, "{}", true},
		{"HTTP/1.1 404 Not Found\nA: b\n\nbody", 404, "body", true},
		{"HTTP/2.0 204 No Content\r\n\r\n", 204, "", true},
		{"", 0, "", false},
		{"HTTP/2.0 200 OK\r\nA: b", 0, "", false},
		{"HTTP/2.0 abc OK\r\n\r\n", 0, "", false},
		{"nonsense\r\n\r\n", 0, "", false},
	}
	for _, tt := range tests {
		status, body, ok := parseHTTP([]byte(tt.in))
		if status != tt.status || string(body) != tt.body || ok != tt.ok {
			t.Errorf("parseHTTP(%q) = %d %q %v", tt.in, status, body, ok)
		}
	}
}
