package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
)

const pullJSON = `{"number":3,"html_url":"u","state":"open","merged":false,"head":{"ref":"h","sha":"abc"},"base":{"ref":"main"}}`

func fake(t *testing.T, handler http.HandlerFunc) backend {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return backend{url: server.URL, repo: "o/r", token: "t", client: server.Client()}
}

func TestOpenCreatesAPull(t *testing.T) {
	b := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/o/r/pulls" || r.Header.Get("Authorization") != "token t" {
			t.Errorf("%s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.Method == http.MethodGet {
			w.Write([]byte("[]"))
			return
		}
		w.Write([]byte(pullJSON))
	})
	got, err := b.Open(context.Background(), &pluginv1.OpenRequest{Head: "h", Base: "main"})
	if err != nil || got.GetNumber() != 3 || got.GetHeadSha() != "abc" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestOpenReturnsTheOpenPullWithoutCreatingOne(t *testing.T) {
	other := strings.Replace(pullJSON, `"ref":"h"`, `"ref":"other"`, 1)
	otherBase := strings.Replace(pullJSON, `"ref":"main"`, `"ref":"dev"`, 1)
	b := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("sent %s", r.Method)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Write([]byte("[" + other + "," + otherBase + "]"))
		case "2":
			w.Write([]byte("[" + pullJSON + "]"))
		default:
			w.Write([]byte("[]"))
		}
	})
	for range 2 {
		got, err := b.Open(context.Background(), &pluginv1.OpenRequest{Head: "h", Base: "main"})
		if err != nil || got.GetNumber() != 3 {
			t.Fatalf("%v %v", got, err)
		}
	}
}

func TestOpenReportsAFailedLookup(t *testing.T) {
	b := fake(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", http.StatusBadGateway) })
	if _, err := b.Open(context.Background(), &pluginv1.OpenRequest{Head: "h", Base: "main"}); err == nil {
		t.Fatal("no error")
	}
}

func TestMergePinsTheHead(t *testing.T) {
	var sent map[string]any
	b := fake(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sent)
	})
	got, err := b.Merge(context.Background(), &pluginv1.MergeRequest{Number: 3, Head: "abc", Base: "main", Method: "squash"})
	if err != nil || got.GetState() != "merged" || sent["head_commit_id"] != "abc" {
		t.Fatalf("%v %v %v", got, err, sent)
	}
}

func TestMergeReportsARefusal(t *testing.T) {
	b := fake(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "conflict", http.StatusConflict) })
	got, err := b.Merge(context.Background(), &pluginv1.MergeRequest{Number: 3, Head: "abc", Base: "main"})
	if err != nil || got.GetState() != "failed" || !strings.Contains(got.GetReason(), "409") {
		t.Fatalf("%v %v", got, err)
	}
}

func TestStatusReadsChecks(t *testing.T) {
	b := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/statuses") {
			w.Write([]byte(`[{"context":"a","status":"success"},{"context":"b","status":"pending"},{"context":"c","status":"error"}]`))
			return
		}
		w.Write([]byte(strings.Replace(pullJSON, `"merged":false`, `"merged":true`, 1)))
	})
	got, err := b.Status(context.Background(), &pluginv1.StatusRequest{Number: 3})
	if err != nil || got.GetState() != "merged" {
		t.Fatalf("%v %v", got, err)
	}
	var states []string
	for _, check := range got.GetChecks() {
		states = append(states, check.GetState())
	}
	if strings.Join(states, ",") != "passed,pending,failed" {
		t.Fatal(states)
	}
}

func TestStatusReportsAFailedStatusRead(t *testing.T) {
	b := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/statuses") {
			http.Error(w, "x", http.StatusBadGateway)
			return
		}
		w.Write([]byte(pullJSON))
	})
	if _, err := b.Status(context.Background(), &pluginv1.StatusRequest{Number: 3}); err == nil {
		t.Fatal("no error")
	}
}

func TestRequestsAreValidatedBeforeAnyCall(t *testing.T) {
	b := backend{}
	ctx := context.Background()
	if _, err := b.Open(ctx, &pluginv1.OpenRequest{}); err == nil || !strings.Contains(err.Error(), "invalid_argument") {
		t.Error(err)
	}
	if _, err := b.Merge(ctx, &pluginv1.MergeRequest{Number: 1}); err == nil || !strings.Contains(err.Error(), "invalid_argument") {
		t.Error(err)
	}
	if _, err := b.Status(ctx, &pluginv1.StatusRequest{}); err == nil || !strings.Contains(err.Error(), "invalid_argument") {
		t.Error(err)
	}
}

func TestCallFailures(t *testing.T) {
	ctx := context.Background()
	if err := (backend{}).call(ctx, "GET", "/x", nil, nil); err == nil || !strings.Contains(err.Error(), "failed_precondition") {
		t.Error(err)
	}
	if err := (backend{url: "http://[bad", repo: "o/r"}).call(ctx, "GET", "/x", nil, nil); err == nil {
		t.Error("bad url")
	}
	if err := (backend{url: "http://127.0.0.1:1", repo: "o/r", client: http.DefaultClient}).call(ctx, "GET", "/x", nil, nil); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Error(err)
	}
	b := fake(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not json")) })
	var out pull
	if err := b.call(ctx, "GET", "/x", nil, &out); err == nil || !strings.Contains(err.Error(), "internal") {
		t.Error(err)
	}
	if err := b.call(ctx, "POST", "/x", make(chan int), nil); err == nil {
		t.Error("unencodable body")
	}
}

func TestFromEnvironment(t *testing.T) {
	t.Setenv("GITEA_URL", "http://g")
	t.Setenv("GITEA_REPO", "o/r")
	t.Setenv("GITEA_TOKEN", "x")
	if b := fromEnvironment(); b.url != "http://g" || b.repo != "o/r" || b.token != "x" {
		t.Fatalf("%+v", b)
	}
}

func TestOpenStopsAfterTooManyPages(t *testing.T) {
	other := strings.Replace(pullJSON, `"ref":"h"`, `"ref":"other"`, 1)
	b := fake(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[" + other + "]")) })
	if _, err := b.Open(context.Background(), &pluginv1.OpenRequest{Head: "h", Base: "main"}); err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatal(err)
	}
}
