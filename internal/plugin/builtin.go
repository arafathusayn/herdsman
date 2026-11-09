package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/commitmsg"
	"github.com/arafathusayn/herdsman/internal/landing"
	"github.com/arafathusayn/herdsman/internal/ledger"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// The built-ins' names.
const (
	jsonBuiltin    = "json"
	processBuiltin = "process"
	githubBuiltin  = "github"
	commitBuiltin  = "commit"
	branchBuiltin  = "branch"
)

// BuiltinOptions supplies what the built-ins need from the machine. A zero
// field takes the real one, except Backend: without a repository to land on
// there is no GitHub built-in.
type BuiltinOptions struct {
	Read     func(context.Context, ledger.Snapshot) (ledger.Snapshot, error)
	Generate func(ctx context.Context, prompt string) (commitmsg.Message, error)
	Backend  landing.Backend
}

// AddBuiltins registers herdsman's own implementations through the interfaces a
// plugin implements.
func AddBuiltins(registry *Registry, options BuiltinOptions) error {
	read := options.Read
	if read == nil {
		read = ledger.NewReader().Read
	}
	generate := options.Generate
	if generate == nil {
		generate = defaultGenerate(&commitmsg.Generator{})
	}
	if err := registry.AddCodec(jsonBuiltin, jsonCodec{}, "json"); err != nil {
		return err
	}
	if err := registry.AddDetector(processBuiltin, processDetector{read: read}); err != nil {
		return err
	}
	if err := registry.AddGenerator(commitBuiltin, commitGenerator{generate: generate}); err != nil {
		return err
	}
	if err := registry.AddColumn(branchBuiltin, branchColumn{}); err != nil {
		return err
	}
	if options.Backend != nil {
		return registry.AddBackend(githubBuiltin, backendAdapter{options.Backend})
	}
	return nil
}

func defaultGenerate(g *commitmsg.Generator) func(context.Context, string) (commitmsg.Message, error) {
	return func(ctx context.Context, prompt string) (commitmsg.Message, error) {
		return g.Generate(ctx, prompt, commitmsg.Pin{})
	}
}

// jsonCodec is the config language that needs no translation.
type jsonCodec struct{}

func (jsonCodec) Decode(_ context.Context, r *pluginv1.DecodeRequest) (*pluginv1.DecodeResponse, error) {
	if !json.Valid(r.GetSource()) {
		return nil, rpc.Errorf(rpc.InvalidArgument, "%s is not valid JSON", r.GetPath())
	}
	return &pluginv1.DecodeResponse{Json: r.GetSource()}, nil
}

func (jsonCodec) Encode(_ context.Context, r *pluginv1.EncodeRequest) (*pluginv1.EncodeResponse, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, r.GetJson(), "", "  "); err != nil {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the tree is not valid JSON")
	}
	out.WriteByte('\n')
	return &pluginv1.EncodeResponse{Source: out.Bytes()}, nil
}

func (jsonCodec) Set(context.Context, *pluginv1.SetRequest) (*pluginv1.SetResponse, error) {
	return nil, rpc.Errorf(rpc.Unimplemented, "the JSON codec has no format-preserving edit")
}

// processDetector reports the processes running inside a worktree.
type processDetector struct {
	read func(context.Context, ledger.Snapshot) (ledger.Snapshot, error)
}

func (d processDetector) Detect(ctx context.Context, r *pluginv1.DetectRequest) (*pluginv1.DetectResponse, error) {
	if r.GetWorktree() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "no worktree")
	}
	snapshot, err := d.read(ctx, ledger.Snapshot{})
	if err != nil {
		return nil, err
	}
	commands := make(map[int]string, len(snapshot.Processes))
	for _, process := range snapshot.Processes {
		commands[process.Pid] = process.Command
	}
	response := &pluginv1.DetectResponse{}
	inside := r.GetWorktree() + string(filepath.Separator)
	for _, entry := range snapshot.Cwds {
		if entry.Path == r.GetWorktree() || strings.HasPrefix(entry.Path, inside) {
			response.Resources = append(response.Resources, &pluginv1.DetectedResource{
				Kind: "process", Id: strconv.Itoa(entry.Pid), Description: commands[entry.Pid],
			})
		}
	}
	return response, nil
}

// backendAdapter puts a landing backend behind the plugin interface. It refuses
// an incomplete request before anything reaches the hosting service.
type backendAdapter struct{ backend landing.Backend }

func (b backendAdapter) Open(ctx context.Context, r *pluginv1.OpenRequest) (*pluginv1.OpenResponse, error) {
	if r.GetHead() == "" || r.GetBase() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "a head and a base are required")
	}
	opened, err := b.backend.Open(ctx, landing.OpenRequest{Head: r.GetHead(), Base: r.GetBase(), Title: r.GetTitle(), Body: r.GetBody(), Draft: r.GetDraft()})
	if err != nil {
		return nil, err
	}
	return &pluginv1.OpenResponse{Number: int64(opened.Number), Url: opened.URL, HeadRef: opened.HeadRef, HeadSha: opened.HeadSHA, BaseRef: opened.BaseRef, Draft: opened.Draft}, nil
}

func (b backendAdapter) Merge(ctx context.Context, r *pluginv1.MergeRequest) (*pluginv1.MergeResponse, error) {
	if r.GetNumber() <= 0 || r.GetHead() == "" || r.GetBase() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "a pull request, a pinned head and a base are required")
	}
	merged, err := b.backend.Merge(ctx, landing.MergeRequest{Number: int(r.GetNumber()), Head: r.GetHead(), Base: r.GetBase(), Method: r.GetMethod(), Title: r.GetTitle(), Message: r.GetMessage()})
	if err != nil {
		return nil, err
	}
	return &pluginv1.MergeResponse{State: string(merged.State), Commit: merged.Commit, Reason: merged.Reason}, nil
}

func (b backendAdapter) Status(ctx context.Context, r *pluginv1.StatusRequest) (*pluginv1.StatusResponse, error) {
	if r.GetNumber() <= 0 {
		return nil, rpc.Errorf(rpc.InvalidArgument, "a pull request number is required")
	}
	status, err := b.backend.Status(ctx, int(r.GetNumber()))
	if err != nil {
		return nil, err
	}
	response := &pluginv1.StatusResponse{
		Number: int64(status.Number), State: status.State, Draft: status.Draft, HeadRef: status.HeadRef,
		HeadSha: status.HeadSHA, BaseRef: status.BaseRef, MergeCommit: status.MergeCommit,
	}
	for _, check := range status.Checks {
		response.Checks = append(response.Checks, &pluginv1.Check{Name: check.Name, State: string(check.State)})
	}
	return response, nil
}

// commitGenerator writes commit messages through the configured harness.
type commitGenerator struct {
	generate func(context.Context, string) (commitmsg.Message, error)
}

func (g commitGenerator) Generate(ctx context.Context, r *pluginv1.GenerateRequest) (*pluginv1.GenerateResponse, error) {
	if r.GetKind() != "commit" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "this generator writes only commit messages, not %q", r.GetKind())
	}
	message, err := g.generate(ctx, r.GetPrompt())
	if err != nil {
		return nil, err
	}
	return &pluginv1.GenerateResponse{Text: message.String()}, nil
}

// branchColumn shows each row's branch.
type branchColumn struct{}

func (branchColumn) Compute(_ context.Context, r *pluginv1.ComputeRequest) (*pluginv1.ComputeResponse, error) {
	response := &pluginv1.ComputeResponse{Header: "BRANCH"}
	for _, row := range r.GetRows() {
		response.Values = append(response.Values, row.GetBranch())
	}
	return response, nil
}
