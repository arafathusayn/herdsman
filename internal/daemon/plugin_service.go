package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/plugin"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	sdkplugin "github.com/arafathusayn/herdsman/sdk/plugin"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// PluginService manages installed plugins. Approve is deliberately not one of
// its RPC methods: a caller of the socket must never be able to accept a plugin.
type PluginService struct {
	Host     *plugin.Host
	Registry *plugin.Registry
}

var _ daemonv1.PluginService = (*PluginService)(nil)

// NewPluginService uses this machine's plugin folders and the built-ins.
func NewPluginService() (*PluginService, error) {
	registry := plugin.NewRegistry()
	if err := plugin.AddBuiltins(registry, plugin.BuiltinOptions{}); err != nil {
		return nil, err
	}
	return &PluginService{Host: plugin.DefaultHost(), Registry: registry}, nil
}

// Procedures lists the service's RPC methods.
func (s *PluginService) Procedures() []rpc.Procedure {
	return []rpc.Procedure{
		rpc.Unary(daemonv1.PluginServiceName, "List", s.List),
		rpc.Unary(daemonv1.PluginServiceName, "Describe", s.Describe),
		rpc.Unary(daemonv1.PluginServiceName, "Install", s.Install),
		rpc.Unary(daemonv1.PluginServiceName, "Remove", s.Remove),
		rpc.Unary(daemonv1.PluginServiceName, "Verify", s.Verify),
	}
}

func builtinStatus(manifest *pluginv1.Manifest) *daemonv1.PluginStatus {
	return &daemonv1.PluginStatus{Name: manifest.GetName(), Approved: true, Version: manifest.GetVersion(), ExtensionPoints: manifest.GetExtensionPoints()}
}

// status reads one plugin. A plugin that cannot start is described, not an error.
func (s *PluginService) status(ctx context.Context, found plugin.Found) (*daemonv1.PluginStatus, error) {
	out := &daemonv1.PluginStatus{Name: found.Name, Path: found.Path, Checksum: found.Checksum}
	approved, err := s.Host.Approvals().Approved(found.Name, found.Checksum)
	if err != nil {
		return nil, err
	}
	out.Approved = approved
	process, err := s.Host.Start(ctx, found)
	if err != nil {
		out.Skipped = err.Error()
		return out, nil
	}
	defer process.Close()
	out.Version = process.Manifest.GetVersion()
	out.ExtensionPoints = process.Manifest.GetExtensionPoints()
	return out, nil
}

// List implements daemonv1.PluginService.
func (s *PluginService) List(ctx context.Context, _ *daemonv1.PluginServiceListRequest) (*daemonv1.PluginServiceListResponse, error) {
	found, err := s.Host.Discover()
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	response := &daemonv1.PluginServiceListResponse{Protocol: sdkplugin.Protocol()}
	replaced := map[string]bool{}
	var discovered []*daemonv1.PluginStatus
	for _, candidate := range found {
		status, err := s.status(ctx, candidate)
		if err != nil {
			return nil, rpc.Errorf(rpc.Internal, "%v", err)
		}
		replaced[status.GetName()] = status.GetSkipped() == ""
		discovered = append(discovered, status)
	}
	for _, name := range s.Registry.Builtins() {
		if !replaced[name] {
			manifest, _, _ := s.Registry.Builtin(name)
			response.Plugins = append(response.Plugins, builtinStatus(manifest))
		}
	}
	response.Plugins = append(response.Plugins, discovered...)
	return response, nil
}

// started returns a running approved plugin; the caller closes it.
func (s *PluginService) started(ctx context.Context, name string) (*sdkplugin.Process, plugin.Found, error) {
	found, ok, err := s.Host.Find(name)
	if err != nil {
		return nil, found, rpc.Errorf(rpc.Internal, "%v", err)
	}
	if !ok {
		return nil, found, rpc.Errorf(rpc.NotFound, "no plugin named %q", name)
	}
	process, err := s.Host.Start(ctx, found)
	switch {
	case errors.Is(err, plugin.ErrNotApproved):
		return nil, found, rpc.Errorf(rpc.PermissionDenied, "%v", err)
	case err != nil:
		return nil, found, rpc.Errorf(rpc.FailedPrecondition, "%v", err)
	}
	return process, found, nil
}

// Describe implements daemonv1.PluginService.
func (s *PluginService) Describe(ctx context.Context, r *daemonv1.PluginServiceDescribeRequest) (*daemonv1.PluginServiceDescribeResponse, error) {
	if manifest, _, ok := s.Registry.Builtin(r.GetName()); ok {
		return &daemonv1.PluginServiceDescribeResponse{Manifest: manifest, Status: builtinStatus(manifest), Protocol: sdkplugin.Protocol()}, nil
	}
	process, found, err := s.started(ctx, r.GetName())
	if err != nil {
		return nil, err
	}
	defer process.Close()
	status := &daemonv1.PluginStatus{
		Name: found.Name, Path: found.Path, Checksum: found.Checksum, Approved: true,
		Version: process.Manifest.GetVersion(), ExtensionPoints: process.Manifest.GetExtensionPoints(),
	}
	return &daemonv1.PluginServiceDescribeResponse{Manifest: process.Manifest, Status: status, Protocol: sdkplugin.Protocol()}, nil
}

// resolveSource turns what the user named into the file to copy and the name
// the plugin gets.
func (s *PluginService) resolveSource(r *daemonv1.PluginServiceInstallRequest) (string, string, error) {
	source := r.GetSource()
	name := r.GetName()
	if strings.Contains(source, "://") {
		return "", "", rpc.Errorf(rpc.InvalidArgument, "installing from a URL is not supported: download the file and name its path")
	}
	if !strings.ContainsRune(source, filepath.Separator) {
		found, ok, err := s.Host.Find(source)
		if err != nil {
			return "", "", rpc.Errorf(rpc.Internal, "%v", err)
		}
		if !ok {
			return "", "", rpc.Errorf(rpc.NotFound, "no plugin named %q on PATH", source)
		}
		source = found.Path
	}
	if !filepath.IsAbs(source) {
		return "", "", rpc.Errorf(rpc.InvalidArgument, "the source path %q is not absolute", source)
	}
	if name == "" {
		name = strings.TrimPrefix(filepath.Base(source), plugin.ExecutablePrefix)
	}
	if !sdkplugin.ValidName(name) {
		return "", "", rpc.Errorf(rpc.InvalidArgument, "invalid plugin name %q", name)
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", "", rpc.Errorf(rpc.NotFound, "%v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", rpc.Errorf(rpc.InvalidArgument, "%s is not an executable file", source)
	}
	return source, name, nil
}

// copyExecutable writes the file in one step, so a failed install leaves any
// earlier version whole.
func copyExecutable(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	temp, err := os.CreateTemp(filepath.Dir(target), ".install-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = io.Copy(temp, in)
	if err == nil {
		err = temp.Chmod(0o755)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), target)
	}
	return err
}

// Install implements daemonv1.PluginService. The copy starts unapproved unless
// these exact bytes were approved before.
func (s *PluginService) Install(ctx context.Context, r *daemonv1.PluginServiceInstallRequest) (*daemonv1.PluginServiceInstallResponse, error) {
	source, name, err := s.resolveSource(r)
	if err != nil {
		return nil, err
	}
	if err := copyExecutable(source, s.Host.InstalledPath(name)); err != nil {
		return nil, rpc.Errorf(rpc.Internal, "install %s: %v", name, err)
	}
	found, _, err := s.Host.Find(name)
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	status, err := s.status(ctx, found)
	if err != nil {
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	return &daemonv1.PluginServiceInstallResponse{Status: status, Protocol: sdkplugin.Protocol()}, nil
}

// Remove implements daemonv1.PluginService.
func (s *PluginService) Remove(_ context.Context, r *daemonv1.PluginServiceRemoveRequest) (*daemonv1.PluginServiceRemoveResponse, error) {
	name := r.GetName()
	if !sdkplugin.ValidName(name) {
		return nil, rpc.Errorf(rpc.InvalidArgument, "invalid plugin name %q", name)
	}
	if _, _, builtin := s.Registry.Builtin(name); builtin {
		return nil, rpc.Errorf(rpc.FailedPrecondition, "%s is built in", name)
	}
	if _, err := os.Stat(s.Host.InstalledPath(name)); err != nil {
		if _, onPath, findErr := s.Host.Find(name); findErr == nil && onPath {
			return nil, rpc.Errorf(rpc.FailedPrecondition, "%s is on PATH, not installed by herdsman: delete the file yourself", name)
		}
		return nil, rpc.Errorf(rpc.NotFound, "no installed plugin named %q", name)
	}
	if err := os.RemoveAll(filepath.Dir(s.Host.InstalledPath(name))); err != nil {
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	if err := s.Host.Approvals().Revoke(name); err != nil {
		return nil, rpc.Errorf(rpc.Internal, "%v", err)
	}
	return &daemonv1.PluginServiceRemoveResponse{Protocol: sdkplugin.Protocol()}, nil
}

// Verify implements daemonv1.PluginService.
func (s *PluginService) Verify(ctx context.Context, r *daemonv1.PluginServiceVerifyRequest) (*daemonv1.PluginServiceVerifyResponse, error) {
	var checks []sdkplugin.Check
	if manifest, clients, ok := s.Registry.Builtin(r.GetName()); ok {
		checks = sdkplugin.Verify(ctx, manifest, clients)
	} else {
		process, _, err := s.started(ctx, r.GetName())
		if err != nil {
			return nil, err
		}
		defer process.Close()
		checks = sdkplugin.Verify(ctx, process.Manifest, process.Clients)
	}
	response := &daemonv1.PluginServiceVerifyResponse{Name: r.GetName(), Passed: true, Protocol: sdkplugin.Protocol()}
	for _, check := range checks {
		response.Checks = append(response.Checks, &daemonv1.VerifyCheck{Name: check.Name, Passed: check.Passed, Detail: check.Detail})
		response.Passed = response.Passed && check.Passed
	}
	return response, nil
}

// Approve accepts a plugin's current checksum after asking the person at the
// terminal. Only the command line reaches it.
func (s *PluginService) Approve(_ context.Context, name string, in io.Reader, out io.Writer, isTerminal bool) error {
	if !isTerminal {
		return errors.New("approving a plugin needs a terminal")
	}
	found, ok, err := s.Host.Find(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no plugin named %q", name)
	}
	fmt.Fprintf(out, "%s\npath: %s\nchecksum: %s\nA plugin runs with your rights. Approve it? Type yes: ", found.Name, found.Path, found.Checksum)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(line) != "yes" {
		return errors.New("not approved")
	}
	return s.Host.Approvals().Approve(found.Name, found.Checksum)
}
