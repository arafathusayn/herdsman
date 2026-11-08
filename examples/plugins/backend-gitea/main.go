// Command backend-gitea is an example landing backend for Gitea. GITEA_URL,
// GITEA_REPO (owner/name) and GITEA_TOKEN say where it lands.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/sdk/plugin"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

const maxResponse = 4 << 20

type backend struct {
	url, repo, token string
	client           *http.Client
}

func (b backend) call(ctx context.Context, method, path string, in, out any) error {
	if b.url == "" || b.repo == "" {
		return rpc.Errorf(rpc.FailedPrecondition, "GITEA_URL and GITEA_REPO are not set")
	}
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(b.url, "/")+"/api/v1/repos/"+b.repo+path, body)
	if err != nil {
		return rpc.Errorf(rpc.FailedPrecondition, "GITEA_URL: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "token "+b.token)
	response, err := b.client.Do(request)
	if err != nil {
		return rpc.Errorf(rpc.Unavailable, "gitea: %v", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err != nil {
		return rpc.Errorf(rpc.Unavailable, "gitea: %v", err)
	}
	if response.StatusCode >= 300 {
		return rpc.Errorf(rpc.FailedPrecondition, "gitea answered %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return rpc.Errorf(rpc.Internal, "gitea: %v", err)
	}
	return nil
}

type pull struct {
	Number  int64  `json:"number"`
	URL     string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Commit  string `json:"merge_commit_sha"`
	IsDraft bool   `json:"draft"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (b backend) Open(ctx context.Context, r *pluginv1.OpenRequest) (*pluginv1.OpenResponse, error) {
	if r.GetHead() == "" || r.GetBase() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "a head and a base are required")
	}
	var created pull
	in := map[string]any{"head": r.GetHead(), "base": r.GetBase(), "title": r.GetTitle(), "body": r.GetBody()}
	if err := b.call(ctx, http.MethodPost, "/pulls", in, &created); err != nil {
		return nil, err
	}
	return &pluginv1.OpenResponse{Number: created.Number, Url: created.URL, HeadRef: created.Head.Ref, HeadSha: created.Head.SHA, BaseRef: created.Base.Ref, Draft: created.IsDraft}, nil
}

func (b backend) Merge(ctx context.Context, r *pluginv1.MergeRequest) (*pluginv1.MergeResponse, error) {
	if r.GetNumber() <= 0 || r.GetHead() == "" || r.GetBase() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "a pull request, a pinned head and a base are required")
	}
	in := map[string]any{"Do": r.GetMethod(), "MergeTitleField": r.GetTitle(), "MergeMessageField": r.GetMessage(), "head_commit_id": r.GetHead()}
	if err := b.call(ctx, http.MethodPost, "/pulls/"+strconv.FormatInt(r.GetNumber(), 10)+"/merge", in, nil); err != nil {
		return &pluginv1.MergeResponse{State: "failed", Reason: err.Error()}, nil
	}
	return &pluginv1.MergeResponse{State: "merged"}, nil
}

func (b backend) Status(ctx context.Context, r *pluginv1.StatusRequest) (*pluginv1.StatusResponse, error) {
	if r.GetNumber() <= 0 {
		return nil, rpc.Errorf(rpc.InvalidArgument, "a pull request number is required")
	}
	var current pull
	if err := b.call(ctx, http.MethodGet, "/pulls/"+strconv.FormatInt(r.GetNumber(), 10), nil, &current); err != nil {
		return nil, err
	}
	var statuses []struct {
		Context string `json:"context"`
		Status  string `json:"status"`
	}
	if err := b.call(ctx, http.MethodGet, "/commits/"+current.Head.SHA+"/statuses", nil, &statuses); err != nil {
		return nil, err
	}
	state := current.State
	if current.Merged {
		state = "merged"
	}
	response := &pluginv1.StatusResponse{Number: current.Number, State: state, Draft: current.IsDraft, HeadRef: current.Head.Ref, HeadSha: current.Head.SHA, BaseRef: current.Base.Ref, MergeCommit: current.Commit}
	for _, status := range statuses {
		response.Checks = append(response.Checks, &pluginv1.Check{Name: status.Context, State: checkState(status.Status)})
	}
	return response, nil
}

func checkState(status string) string {
	switch status {
	case "success":
		return "passed"
	case "pending":
		return "pending"
	}
	return "failed"
}

func fromEnvironment() backend {
	return backend{url: os.Getenv("GITEA_URL"), repo: os.Getenv("GITEA_REPO"), token: os.Getenv("GITEA_TOKEN"), client: &http.Client{Timeout: 30 * time.Second}}
}

func main() {
	manifest := &pluginv1.Manifest{
		Name: "backend-gitea", Version: "0.1.0", Description: fmt.Sprintf("An example landing backend for Gitea, protocol %s.", plugin.Protocol()),
		ExtensionPoints: []pluginv1.ExtensionPoint{pluginv1.ExtensionPoint_EXTENSION_POINT_LANDING_BACKEND},
	}
	if err := plugin.Serve(context.Background(), manifest, plugin.Extensions{LandingBackend: fromEnvironment()}); err != nil {
		log.Fatal(err)
	}
}
