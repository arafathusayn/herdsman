package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/sdk/config"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// configRPC serves the configuration contract over the daemon's RPC. In inspect
// mode the daemon refuses Set and Unset before they reach it.
type configRPC struct{}

// configOptions says where a request's settings come from. Only a repository
// the caller named is asked for its identity.
func configOptions(ctx context.Context, target *configv1.Target) config.Options {
	opts := config.Options{ConfigPath: target.GetConfigPath(), RepoRoot: target.GetRepoRoot()}
	if root := target.GetRepoRoot(); filepath.IsAbs(root) {
		if out, err := (&gitrun.Runner{}).Run(ctx, root, "remote", "get-url", "origin"); err == nil {
			opts.ProjectKey = config.RepositoryKey(string(out.Stdout))
		}
	}
	return opts
}

// invalidConfig reports a request the configuration refuses; the caller's
// file or key is at fault, so that is the code it is served as.
func invalidConfig(err error) error {
	var served *rpc.Error
	if errors.As(err, &served) {
		return served
	}
	return rpc.Errorf(rpc.InvalidArgument, "%s", err.Error())
}

// projectFile is the file a request edits or exports.
func projectFile(opts config.Options) (string, error) {
	path, err := config.ProjectFile(opts)
	if err != nil {
		return "", invalidConfig(err)
	}
	if path == "" {
		return "", rpc.Errorf(rpc.FailedPrecondition, "no configuration file; create one with herdsman config init")
	}
	return path, nil
}

func (c *configRPC) Export(ctx context.Context, r *configv1.ExportRequest) (*configv1.ExportResponse, error) {
	path, err := projectFile(configOptions(ctx, r.GetTarget()))
	if err != nil {
		return nil, err
	}
	data, err := config.ExportFile(path, r.GetFormat())
	if err != nil {
		return nil, invalidConfig(err)
	}
	return &configv1.ExportResponse{Data: data}, nil
}

func (c *configRPC) Get(ctx context.Context, r *configv1.GetRequest) (*configv1.GetResponse, error) {
	result, err := config.Load(configOptions(ctx, r.GetTarget()))
	if err != nil {
		return nil, invalidConfig(err)
	}
	value, err := result.Get(r.GetKey())
	if err != nil {
		return nil, invalidConfig(err)
	}
	_, source, err := result.Typed(r.GetKey())
	if err != nil {
		return nil, invalidConfig(err)
	}
	return &configv1.GetResponse{Value: value, Source: source}, nil
}

func (c *configRPC) Set(ctx context.Context, r *configv1.SetRequest) (*configv1.SetResponse, error) {
	path, err := projectFile(configOptions(ctx, r.GetTarget()))
	if err != nil {
		return nil, err
	}
	if err := config.SetFile(path, r.GetKey(), r.GetValue()); err != nil {
		return nil, invalidConfig(err)
	}
	return &configv1.SetResponse{Path: path}, nil
}

func (c *configRPC) Show(ctx context.Context, r *configv1.ShowRequest) (*configv1.ShowResponse, error) {
	opts := configOptions(ctx, r.GetTarget())
	opts.Strict = r.GetStrict()
	result, err := config.Load(opts)
	if err != nil {
		return nil, invalidConfig(err)
	}
	out := &configv1.ShowResponse{ProjectPath: result.ProjectPath, Warnings: warningLines(result.Warnings)}
	for _, value := range result.Show() {
		out.Values = append(out.Values, &configv1.ShownValue{Key: value.Key, Value: value.Value, Source: value.Source})
	}
	return out, nil
}

func (c *configRPC) Unset(ctx context.Context, r *configv1.UnsetRequest) (*configv1.UnsetResponse, error) {
	path, err := projectFile(configOptions(ctx, r.GetTarget()))
	if err != nil {
		return nil, err
	}
	removed, err := config.UnsetFile(path, r.GetKey())
	if err != nil {
		return nil, invalidConfig(err)
	}
	return &configv1.UnsetResponse{Path: path, Removed: removed}, nil
}

func (c *configRPC) Validate(ctx context.Context, r *configv1.ValidateRequest) (*configv1.ValidateResponse, error) {
	opts := configOptions(ctx, r.GetTarget())
	opts.Strict = r.GetStrict()
	result, err := config.Load(opts)
	if err != nil {
		return nil, invalidConfig(err)
	}
	return &configv1.ValidateResponse{Warnings: warningLines(result.Warnings)}, nil
}

func warningLines(warnings []config.Warning) []string {
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		out = append(out, strings.TrimSpace(warning.String()))
	}
	return out
}
