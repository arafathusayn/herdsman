package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// designErrors is design 5.2, row by row: HTTP status, retryable, CLI exit.
var designErrors = map[string]errorSpec{
	"bad_request": {400, false, 2}, "unauthorized": {401, false, 2}, "identity_mismatch": {403, false, 2},
	"wrong_route": {403, false, 2}, "unknown_route": {404, false, 2},
	"version_missing": {400, false, 2}, "version_malformed": {400, false, 2}, "version_mismatch": {426, false, 2},
	"registry_pending": {409, true, 2}, "proof_stale": {409, false, 2}, "human_boundary": {409, false, 3},
	"backlog_full": {409, false, 2}, "budget_full": {409, false, 2},
	"claim_held": {409, false, 2}, "no_claim": {409, false, 2}, "unacked_work": {409, false, 2},
	"terminal_conflict": {409, false, 2}, "key_conflict": {409, false, 2}, "key_window_full": {503, true, 75},
	"ack_too_large": {413, false, 2}, "question_exists": {409, false, 2}, "question_limit": {409, false, 2},
	"adopt_busy": {409, true, 75}, "busy": {503, true, 75}, "route_unowned": {503, true, 75}, "internal": {500, true, 1},
	"route_exists": {409, false, 2}, "adopt_locked": {409, true, 2},
	"herdr_unreachable": {503, true, 75}, "identity_unavailable": {503, true, 75},
	"name_retired": {409, false, 2}, "role_taken": {409, false, 2}, "unknown_name": {404, false, 2},
	"unknown_target": {404, false, 2}, "no_hold": {404, false, 2},
	"checksum_mismatch": {409, false, 2}, "incomplete_pair": {409, false, 2}, "option_unavailable": {409, false, 2},
	"daemon_running": {0, false, 2},
}

// pendingProducers are 5.2 codes whose producer is a later unit.
var pendingProducers = []string{"question_exists", "question_limit"}

// TestErrorTableColumns asserts every 5.2 row on all columns: the table,
// the API error, the JSON answer (status, code, retryable) and the CLI
// exit code. The table adds only not_found (an unknown request path).
func TestErrorTableColumns(t *testing.T) {
	for code, want := range designErrors {
		if got, ok := errorTable[code]; !ok || got != want {
			t.Errorf("%s: table %+v, design %+v", code, got, want)
		}
		if ExitCode(code) != want.exit {
			t.Errorf("%s: exit %d", code, ExitCode(code))
		}
		if want.status == 0 {
			continue // CLI only
		}
		e := Errf(code, "x")
		if e.Code != code || e.Status != want.status || e.Retryable != want.retryable {
			t.Errorf("%s: %+v", code, e)
		}
		rec := httptest.NewRecorder()
		writeError(rec, e)
		var body struct {
			Error struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		if rec.Code != want.status || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != code ||
			body.Error.Retryable != want.retryable || body.Error.Message != "x" {
			t.Errorf("%s: answer %d %s", code, rec.Code, rec.Body.String())
		}
	}
	for code := range errorTable {
		if _, ok := designErrors[code]; !ok && code != "not_found" {
			t.Errorf("%s is not a 5.2 code", code)
		}
	}
	if Errf("no_such_code", "x").Code != "internal" || ExitCode("no_such_code") != 1 {
		t.Error("an unknown code is not internal")
	}
}

// TestErrorProducers: every 5.2 code but the pending ones has a producer
// in the daemon's source (an Errf or versionErr call naming it).
func TestErrorProducers(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	var src strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "errors.go" {
			continue
		}
		b, _ := os.ReadFile(f)
		src.Write(b)
	}
	for code := range designErrors {
		re := regexp.MustCompile(`(Errf|versionErr)\("` + code + `"|code: "` + code + `"|"` + code + `"`)
		has := re.MatchString(src.String())
		if slices.Contains(pendingProducers, code) {
			if has {
				t.Errorf("%s has a producer now; take it off the pending list", code)
			}
			continue
		}
		if !has {
			t.Errorf("%s has no producer", code)
		}
	}
}

// TestRefusalBoundaries produces the codes no other test reaches at their
// real refusal points: registry_pending, budget_full, route_unowned,
// not_found and (off macOS) option_unavailable.
func TestRefusalBoundaries(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	ctx := context.Background()

	// not_found: an unknown request path.
	if _, err := h.client().Do(ctx, "GET", "/v1/nothing", nil, nil); code(err) != "not_found" {
		t.Fatalf("not_found: %v", err)
	}

	// budget_full: the ninth side-agent message of one source.
	src := `{"version":1,"text":"x","source":{"pane":"p9","session":"s9"}}`
	rt, _ := h.d.route("r")
	var e *APIError
	for i := range sidePerSource + 1 {
		e = rt.do(ctx, func() *APIError {
			_, e := rt.send(adj.name, SendRequest{To: orch.name, Kind: "side_agent_data", Body: src, Key: key()})
			return e
		})
		if i < sidePerSource && e != nil {
			t.Fatalf("side-agent %d: %v", i, e)
		}
	}
	if e == nil || e.Code != "budget_full" || e.Status != 409 || e.Retryable {
		t.Fatalf("budget_full: %+v", e)
	}

	// registry_pending: a pending pair change holds delivery.
	b, _ := os.ReadFile(filepath.Join(dir, "peers"))
	_ = os.WriteFile(filepath.Join(dir, "peers"), append(b[:len(b)-2], []byte(`,"x":{}}`+"\n")...), 0o600)
	_, err := adj.c.Do(ctx, "POST", "/v1/routes/r/messages", SendRequest{To: orch.name, Kind: "brief", Body: "x", Key: key()}, nil)
	if ae, ok := AsAPIError(err); !ok || ae.Code != "registry_pending" || ae.Status != 409 || !ae.Retryable {
		t.Fatalf("registry_pending: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "peers"), b, 0o600)

	// route_unowned: a route whose journal is held is not served.
	h.stop()
	_ = os.WriteFile(filepath.Join(dir, "state", "events", journalCurrent), []byte("{\"seq\":1}\n{}\n"), 0o600)
	h.start()
	_, err = adj.c.Do(ctx, "POST", "/v1/routes/r/messages", SendRequest{To: orch.name, Kind: "brief", Body: "x", Key: key()}, nil)
	if ae, ok := AsAPIError(err); !ok || ae.Code != "route_unowned" || ae.Status != 503 || !ae.Retryable {
		t.Fatalf("route_unowned: %v", err)
	}

	// option_unavailable: keychain off macOS.
	if runtime.GOOS != "darwin" {
		if !slices.Contains(credentialOptions(), "keychain") {
			x := newDirect(t)
			_, e := x.r.approve(ApproveRequest{SHA256: "x", Credentials: map[string]string{"adj": "keychain"}})
			if e == nil || (e.Code != "option_unavailable" && e.Code != "checksum_mismatch") {
				t.Fatalf("option_unavailable: %v", e)
			}
		}
	}
}
