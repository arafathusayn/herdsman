package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/sdk/config"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// configRPC serves the configuration contract over the daemon's RPC. In inspect
// mode the daemon refuses Set and Unset before they reach it, and starts no
// process for the reads.
type configRPC struct {
	inspect bool
}

// options says where a request's settings come from. Only a repository the
// caller named is asked for its identity.
func (c *configRPC) options(ctx context.Context, target *configv1.Target) config.Options {
	opts := config.Options{ConfigPath: target.GetConfigPath(), RepoRoot: target.GetRepoRoot()}
	root := target.GetRepoRoot()
	switch {
	case !filepath.IsAbs(root):
	case c.inspect:
		opts.ProjectKey = config.RepositoryKey(recordedOrigin(root))
	default:
		if out, err := (&gitrun.Runner{}).Run(ctx, root, "remote", "get-url", "origin"); err == nil {
			opts.ProjectKey = config.RepositoryKey(string(out.Stdout))
		}
	}
	return opts
}

// recordedOrigin reads the origin address from a repository's own config file,
// following a linked worktree to its common folder. It starts no process, so
// include files and url rewrites are not applied; nothing found is "".
func recordedOrigin(root string) string {
	folder := filepath.Join(root, ".git")
	if link, err := os.ReadFile(folder); err == nil {
		target, linked := strings.CutPrefix(strings.TrimSpace(string(link)), "gitdir: ")
		if !linked {
			return ""
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		folder = target
	}
	if common, err := os.ReadFile(filepath.Join(folder, "commondir")); err == nil {
		target := strings.TrimSpace(string(common))
		if !filepath.IsAbs(target) {
			target = filepath.Join(folder, target)
		}
		folder = target
	}
	data, err := os.ReadFile(filepath.Join(folder, "config"))
	if err != nil {
		return ""
	}
	inOrigin := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if inOrigin && found && strings.EqualFold(strings.TrimSpace(key), "url") {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	return ""
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
	path, err := projectFile(c.options(ctx, r.GetTarget()))
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
	result, err := config.Load(c.options(ctx, r.GetTarget()))
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
	path, err := projectFile(c.options(ctx, r.GetTarget()))
	if err != nil {
		return nil, err
	}
	if err := config.SetFile(path, r.GetKey(), r.GetValue()); err != nil {
		return nil, invalidConfig(err)
	}
	return &configv1.SetResponse{Path: path}, nil
}

func (c *configRPC) Show(ctx context.Context, r *configv1.ShowRequest) (*configv1.ShowResponse, error) {
	opts := c.options(ctx, r.GetTarget())
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
	path, err := projectFile(c.options(ctx, r.GetTarget()))
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
	opts := c.options(ctx, r.GetTarget())
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
