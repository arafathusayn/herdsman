package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
)

// suggestPath is where a method of the catalog service is served.
func suggestPath(method string) string {
	return "http://herdsman/rpc/herdsman.suggest.v1.SuggestService/" + method
}

// suggestSide is one encoding's view of the daemon's catalog service.
type suggestSide struct {
	t       *testing.T
	hc      *http.Client
	options []connect.ClientOption
}

func call[Req, Res any](s suggestSide, method string, req *Req) (*Res, error) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := connect.NewClient[Req, Res](s.hc, suggestPath(method), s.options...)
	res, err := client.CallUnary(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// must returns a call's response and fails the test when the call was refused.
// It takes the test first so that a call's two results can follow it.
func must[Res any](t *testing.T) func(*Res, error) *Res {
	return func(res *Res, err error) *Res {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
}

// refused checks that a call failed with the wanted code.
func refused[Res any](t *testing.T, _ *Res, err error, code connect.Code) {
	t.Helper()
	var failure *connect.Error
	if !errors.As(err, &failure) || failure.Code() != code {
		t.Fatalf("error %v, want %v", err, code)
	}
}

// catalogRepo is a remote catalog on this machine, so no scenario uses the
// network. commit adds one entry and returns the new commit id.
func catalogRepo(t *testing.T) (string, func(id string) string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(dir, ".no-config"),
			"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet", "--initial-branch=main")
	if err := os.MkdirAll(filepath.Join(dir, "herdsman-suggest"), 0o755); err != nil {
		t.Fatal(err)
	}
	return "file://" + dir, func(id string) string {
		t.Helper()
		text := "---\nid: " + id + "\nkind: tip\ntitle: Remote " + id + "\ntags: [remote]\nsince: 0.0.1\n---\nA body.\n"
		if err := os.WriteFile(filepath.Join(dir, "herdsman-suggest", id+".md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "--quiet", "-m", "add "+id)
		return git("rev-parse", "HEAD")
	}
}

func TestSuggestServiceConformance(t *testing.T) {
	socket := startDaemon(t)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	encodings := []struct {
		name    string
		options []connect.ClientOption
	}{{"proto", nil}, {"json", []connect.ClientOption{connect.WithProtoJSON()}}}
	for _, encoding := range encodings {
		side := suggestSide{hc: &http.Client{Transport: transport}, options: encoding.options}
		// An outside daemon keeps its catalog between runs, so ids are fresh.
		id := fmt.Sprintf("conformance-%s-%d", encoding.name, time.Now().UnixNano())
		t.Run(encoding.name+"/entries", func(t *testing.T) { side.t = t; entryScenarios(t, side, id) })
		t.Run(encoding.name+"/recipes", func(t *testing.T) { side.t = t; recipeScenarios(t, side, id) })
		t.Run(encoding.name+"/sources", func(t *testing.T) { side.t = t; sourceScenarios(t, side) })
		t.Run(encoding.name+"/no method runs a recipe", func(t *testing.T) {
			side.t = t
			res, err := call[suggestv1.PlanRequest, suggestv1.PlanResponse](side, "Run", &suggestv1.PlanRequest{Id: id})
			refused(t, res, err, connect.CodeUnimplemented)
		})
	}
}

func entryScenarios(t *testing.T, s suggestSide, id string) {
	add := &suggestv1.AddRequest{Id: id, Kind: "tip", Title: "Entry " + id, Tags: []string{"conformance"}, Body: "The body text."}
	added := must[suggestv1.AddResponse](t)(call[suggestv1.AddRequest, suggestv1.AddResponse](s, "Add", add))
	if added.GetId() != id || added.GetPath() == "" {
		t.Fatalf("add = %v", added)
	}
	res, err := call[suggestv1.AddRequest, suggestv1.AddResponse](s, "Add", add)
	refused(t, res, err, connect.CodeAlreadyExists)
	res, err = call[suggestv1.AddRequest, suggestv1.AddResponse](s, "Add", &suggestv1.AddRequest{Id: "Not/Valid", Kind: "tip", Title: "T", Tags: []string{"x"}, Body: "B"})
	refused(t, res, err, connect.CodeInvalidArgument)
	res, err = call[suggestv1.AddRequest, suggestv1.AddResponse](s, "Add", &suggestv1.AddRequest{Project: true, Id: id + "-p", Kind: "tip", Title: "T", Tags: []string{"x"}, Body: "B"})
	refused(t, res, err, connect.CodeFailedPrecondition)

	got := must[suggestv1.GetResponse](t)(call[suggestv1.GetRequest, suggestv1.GetResponse](s, "Get", &suggestv1.GetRequest{Id: id}))
	entry := got.GetEntry()
	if entry.GetId() != id || entry.GetKind() != "tip" || entry.GetBody() != "The body text." || entry.GetSource().GetKind() != "user" || !slices.Equal(entry.GetTags(), []string{"conformance"}) {
		t.Fatalf("get = %v", got)
	}
	missing, err := call[suggestv1.GetRequest, suggestv1.GetResponse](s, "Get", &suggestv1.GetRequest{Id: "no-such-entry"})
	refused(t, missing, err, connect.CodeNotFound)
	missing, err = call[suggestv1.GetRequest, suggestv1.GetResponse](s, "Get", &suggestv1.GetRequest{})
	refused(t, missing, err, connect.CodeInvalidArgument)

	found := must[suggestv1.SearchResponse](t)(call[suggestv1.SearchRequest, suggestv1.SearchResponse](s, "Search", &suggestv1.SearchRequest{Text: id}))
	if len(found.GetItems()) != 1 || found.GetItems()[0].GetEntry().GetId() != id {
		t.Fatalf("search = %v", found)
	}
	item := found.GetItems()[0]
	if len(item.GetReasons()) == 0 {
		t.Fatalf("the item has no reasons: %v", item)
	}
	sum := int32(0)
	for _, reason := range item.GetReasons() {
		if reason.GetCode() == "" {
			t.Fatalf("a reason has no code: %v", item)
		}
		sum += reason.GetPoints()
	}
	if sum != item.GetScore() {
		t.Fatalf("score %d is not the sum %d of its reasons", item.GetScore(), sum)
	}
	none := must[suggestv1.SearchResponse](t)(call[suggestv1.SearchRequest, suggestv1.SearchResponse](s, "Search", &suggestv1.SearchRequest{Text: id, Kind: "mcp"}))
	if len(none.GetItems()) != 0 {
		t.Fatalf("the kind filter kept %v", none.GetItems())
	}
}

func recipeScenarios(t *testing.T, s suggestSide, id string) {
	plan := must[suggestv1.PlanResponse](t)(call[suggestv1.PlanRequest, suggestv1.PlanResponse](s, "Plan", &suggestv1.PlanRequest{Id: "herdsman-skill"}))
	recipe := plan.GetRecipe()
	if recipe.GetId() != "herdsman-skill" || recipe.GetKind() != "skill_repo" || !strings.HasPrefix(recipe.GetChecksum(), "sha256:") || len(recipe.GetTouches()) == 0 || recipe.GetSource().GetKind() != "bundled" {
		t.Fatalf("plan = %v", plan)
	}
	res, err := call[suggestv1.PlanRequest, suggestv1.PlanResponse](s, "Plan", &suggestv1.PlanRequest{Id: id})
	refused(t, res, err, connect.CodeFailedPrecondition)
	res, err = call[suggestv1.PlanRequest, suggestv1.PlanResponse](s, "Plan", &suggestv1.PlanRequest{Id: "no-such-entry"})
	refused(t, res, err, connect.CodeNotFound)
	res, err = call[suggestv1.PlanRequest, suggestv1.PlanResponse](s, "Plan", &suggestv1.PlanRequest{})
	refused(t, res, err, connect.CodeInvalidArgument)
}

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

func sourceScenarios(t *testing.T, s suggestSide) {
	url, commit := catalogRepo(t)
	first := commit("remote-first")

	added := must[suggestv1.SourceAddResponse](t)(call[suggestv1.SourceAddRequest, suggestv1.SourceAddResponse](s, "SourceAdd", &suggestv1.SourceAddRequest{Url: url}))
	if added.GetPin().GetUrl() != url || added.GetPin().GetCommit() != first || !fullCommit.MatchString(added.GetPin().GetCommit()) {
		t.Fatalf("add = %v, want %s", added, first)
	}
	again, err := call[suggestv1.SourceAddRequest, suggestv1.SourceAddResponse](s, "SourceAdd", &suggestv1.SourceAddRequest{Url: url})
	refused(t, again, err, connect.CodeAlreadyExists)
	again, err = call[suggestv1.SourceAddRequest, suggestv1.SourceAddResponse](s, "SourceAdd", &suggestv1.SourceAddRequest{Url: "ext::sh -c id"})
	refused(t, again, err, connect.CodeInvalidArgument)
	again, err = call[suggestv1.SourceAddRequest, suggestv1.SourceAddResponse](s, "SourceAdd", &suggestv1.SourceAddRequest{Url: url + "-other", Ref: "-bad"})
	refused(t, again, err, connect.CodeInvalidArgument)

	listed := must[suggestv1.SourcesResponse](t)(call[suggestv1.SourcesRequest, suggestv1.SourcesResponse](s, "Sources", &suggestv1.SourcesRequest{}))
	if !slices.ContainsFunc(listed.GetSources(), func(p *suggestv1.Pin) bool { return p.GetUrl() == url && p.GetCommit() == first }) {
		t.Fatalf("sources = %v", listed)
	}

	second := commit("remote-second")
	preview := must[suggestv1.SourcePreviewResponse](t)(call[suggestv1.SourcePreviewRequest, suggestv1.SourcePreviewResponse](s, "SourcePreview", &suggestv1.SourcePreviewRequest{Url: url}))
	if preview.GetFrom().GetCommit() != first || preview.GetTo().GetCommit() != second || len(preview.GetChanges()) != 1 || preview.GetChanges()[0].GetId() != "remote-second" || preview.GetChanges()[0].GetAction() == "" {
		t.Fatalf("preview = %v", preview)
	}
	unknown, err := call[suggestv1.SourcePreviewRequest, suggestv1.SourcePreviewResponse](s, "SourcePreview", &suggestv1.SourcePreviewRequest{Url: url + "-none"})
	refused(t, unknown, err, connect.CodeNotFound)

	moved, err := call[suggestv1.SourcePinRequest, suggestv1.SourcePinResponse](s, "SourcePin", &suggestv1.SourcePinRequest{Url: url, From: second, To: second})
	refused(t, moved, err, connect.CodeAborted)
	moved, err = call[suggestv1.SourcePinRequest, suggestv1.SourcePinResponse](s, "SourcePin", &suggestv1.SourcePinRequest{Url: url, From: first, To: "main"})
	refused(t, moved, err, connect.CodeInvalidArgument)
	pinned := must[suggestv1.SourcePinResponse](t)(call[suggestv1.SourcePinRequest, suggestv1.SourcePinResponse](s, "SourcePin", &suggestv1.SourcePinRequest{Url: url, From: first, To: second}))
	if pinned.GetPin().GetCommit() != second {
		t.Fatalf("pin = %v", pinned)
	}

	removed := must[suggestv1.SourceRemoveResponse](t)(call[suggestv1.SourceRemoveRequest, suggestv1.SourceRemoveResponse](s, "SourceRemove", &suggestv1.SourceRemoveRequest{Url: url}))
	if removed.GetUrl() != url {
		t.Fatalf("remove = %v", removed)
	}
	gone, err := call[suggestv1.SourceRemoveRequest, suggestv1.SourceRemoveResponse](s, "SourceRemove", &suggestv1.SourceRemoveRequest{Url: url})
	refused(t, gone, err, connect.CodeNotFound)
}
