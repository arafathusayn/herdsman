package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/commitmsg"
	"github.com/arafathusayn/herdsman/internal/landing"
	"github.com/arafathusayn/herdsman/internal/ledger"
	sdkplugin "github.com/arafathusayn/herdsman/sdk/plugin"
	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

const timeoutForTests = 5 * time.Second

// A shell plugin can fail a handshake; only real Go plugins can pass one, and
// those are proved in the daemon and conformance tests.
func writeExecutable(t *testing.T, path, script string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newHost(t *testing.T) *Host {
	t.Helper()
	dir := t.TempDir()
	return &Host{
		ConfigDir:  filepath.Join(dir, "config"),
		SearchPath: filepath.Join(dir, "bin"),
		Timeout:    timeoutForTests,
	}
}

func TestApprovalIsPerChecksum(t *testing.T) {
	a := Approvals{Path: filepath.Join(t.TempDir(), "sub", "approvals.json")}
	if ok, err := a.Approved("p", "one"); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if err := a.Approve("p", "one"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Approved("p", "one"); !ok {
		t.Fatal("not approved")
	}
	if ok, _ := a.Approved("p", "two"); ok {
		t.Fatal("another checksum counted")
	}
	if err := a.Approve("p", "two"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Approved("p", "one"); ok {
		t.Fatal("the old checksum stayed approved")
	}
	if err := a.Revoke("p"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.Approved("p", "two"); ok {
		t.Fatal("revoked approval counted")
	}
	if err := a.Revoke("absent"); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalsReportACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.json")
	os.WriteFile(path, []byte("{"), 0o600)
	a := Approvals{Path: path}
	if _, err := a.Approved("p", "x"); err == nil {
		t.Fatal("read a corrupt file")
	}
	if err := a.Approve("p", "x"); err == nil {
		t.Fatal("wrote over a corrupt file")
	}
	if err := a.Revoke("p"); err == nil {
		t.Fatal("revoked in a corrupt file")
	}
}

func TestApprovalsReportAnUnwritableFolder(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o600)
	a := Approvals{Path: filepath.Join(blocker, "sub", "approvals.json")}
	if err := a.Approve("p", "x"); err == nil {
		t.Fatal("wrote under a file")
	}
}

func TestDiscoverFindsFolderThenPath(t *testing.T) {
	h := newHost(t)
	folder := writeExecutable(t, filepath.Join(h.PluginsDir(), "shared", "herdsman-plugin-shared"), "exit 0")
	writeExecutable(t, filepath.Join(h.SearchPath, "herdsman-plugin-shared"), "exit 1")
	onPath := writeExecutable(t, filepath.Join(h.SearchPath, "herdsman-plugin-other"), "exit 0")
	os.WriteFile(filepath.Join(h.SearchPath, "herdsman-plugin-noexec"), nil, 0o644)
	os.MkdirAll(filepath.Join(h.SearchPath, "herdsman-plugin-dir"), 0o755)
	os.WriteFile(filepath.Join(h.SearchPath, "unrelated"), nil, 0o755)
	os.MkdirAll(filepath.Join(h.PluginsDir(), "empty"), 0o755)
	writeExecutable(t, filepath.Join(h.SearchPath, "herdsman-plugin-Bad_Name"), "exit 0")

	found, err := h.Discover()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range found {
		got[f.Name] = f.Path
		if f.Checksum == "" {
			t.Errorf("%s has no checksum", f.Name)
		}
	}
	if len(got) != 2 || got["shared"] != folder || got["other"] != onPath {
		t.Fatalf("%v", got)
	}
}

func TestDiscoverIgnoresAMissingFolderAndEmptyPathEntry(t *testing.T) {
	h := newHost(t)
	h.SearchPath = string(os.PathListSeparator) + filepath.Join(t.TempDir(), "absent")
	found, err := h.Discover()
	if err != nil || len(found) != 0 {
		t.Fatalf("%v %v", found, err)
	}
}

func TestDiscoverReportsAnUnreadablePluginFolder(t *testing.T) {
	h := newHost(t)
	os.MkdirAll(filepath.Dir(h.PluginsDir()), 0o700)
	os.WriteFile(h.PluginsDir(), nil, 0o600)
	if _, err := h.Discover(); err == nil {
		t.Fatal("no error for a plugin folder that is a file")
	}
}

func TestLoadSkipsByNameAndKeepsGoing(t *testing.T) {
	h := newHost(t)
	unapproved := writeExecutable(t, filepath.Join(h.SearchPath, "herdsman-plugin-unapproved"), "exit 0")
	broken := writeExecutable(t, filepath.Join(h.SearchPath, "herdsman-plugin-broken"), "echo not a handshake")
	_ = unapproved
	sum, err := checksumFile(broken)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Approvals().Approve("broken", sum); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	if err := h.Load(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	skipped := map[string]string{}
	for _, s := range registry.Skipped() {
		skipped[s.Name] = s.Reason
	}
	if !strings.Contains(skipped["unapproved"], "not approved") {
		t.Errorf("unapproved: %q", skipped["unapproved"])
	}
	if !strings.Contains(skipped["broken"], "handshake") {
		t.Errorf("broken: %q", skipped["broken"])
	}
}

func TestLoadReportsDiscoveryFailure(t *testing.T) {
	h := newHost(t)
	os.MkdirAll(filepath.Dir(h.PluginsDir()), 0o700)
	os.WriteFile(h.PluginsDir(), nil, 0o600)
	if err := h.Load(context.Background(), NewRegistry()); err == nil {
		t.Fatal("no error")
	}
}

func TestStartRefusesAnUnapprovedPlugin(t *testing.T) {
	h := newHost(t)
	path := writeExecutable(t, filepath.Join(h.SearchPath, "herdsman-plugin-x"), "exit 0")
	found := Found{Name: "x", Path: path, Checksum: "abc"}
	if _, err := h.Start(context.Background(), found); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("%v", err)
	}
}

func TestStartReportsACorruptApprovalFile(t *testing.T) {
	h := newHost(t)
	os.MkdirAll(h.ConfigDir, 0o700)
	os.WriteFile(h.Approvals().Path, []byte("{"), 0o600)
	if _, err := h.Start(context.Background(), Found{Name: "x"}); err == nil || errors.Is(err, ErrNotApproved) {
		t.Fatalf("%v", err)
	}
}

func TestChecksumFileReportsAMissingFile(t *testing.T) {
	if _, err := checksumFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("no error")
	}
}

func TestHostDefaultsComeFromTheEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	t.Setenv("PATH", "/some/bin")
	h := DefaultHost()
	if h.ConfigDir != "/cfg/herdsman" || h.SearchPath != "/some/bin" {
		t.Fatalf("%+v", h)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if h := DefaultHost(); h.ConfigDir != "/home/u/.config/herdsman" {
		t.Fatalf("%+v", h)
	}
}

type fakeCodec struct{ pluginv1.ConfigCodecService }
type fakeDetector struct {
	pluginv1.ResourceDetectorService
}
type fakeBackend struct{ pluginv1.LandingBackendService }
type fakeGenerator struct {
	pluginv1.MessageGeneratorService
}
type fakeColumn struct{ pluginv1.ListColumnService }

func TestRegistryRefusesTwoBuiltinsOfOneName(t *testing.T) {
	r := NewRegistry()
	for name, register := range map[string]func() error{
		"codec":     func() error { return r.AddCodec("a", fakeCodec{}, "x") },
		"detector":  func() error { return r.AddDetector("a", fakeDetector{}) },
		"backend":   func() error { return r.AddBackend("a", fakeBackend{}) },
		"generator": func() error { return r.AddGenerator("a", fakeGenerator{}) },
		"column":    func() error { return r.AddColumn("a", fakeColumn{}) },
	} {
		if err := register(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := register(); err == nil {
			t.Errorf("%s: registered twice", name)
		}
	}
	if _, ok := r.CodecFor("x"); !ok {
		t.Error("no codec for the extension")
	}
	if _, ok := r.CodecFor("y"); ok {
		t.Error("a codec for an unknown extension")
	}
	if r.Codec("a") == nil || r.Detector("a") == nil || r.Backend("a") == nil || r.Generator("a") == nil || r.Column("a") == nil {
		t.Error("a lookup failed")
	}
	if r.Codec("b") != nil {
		t.Error("found an absent codec")
	}
	if got := r.Builtins(); len(got) != 1 || got[0] != "a" {
		t.Errorf("%v", got)
	}
}

func TestPluginReplacesABuiltinBehindTheSameInterface(t *testing.T) {
	r := NewRegistry()
	r.AddCodec("json", fakeCodec{}, "json")
	r.AddDetector("d", fakeDetector{})
	r.AddBackend("b", fakeBackend{})
	r.AddGenerator("g", fakeGenerator{})
	r.AddColumn("c", fakeColumn{})
	replace := func(name string, point pluginv1.ExtensionPoint, exts ...string) {
		r.addProcess(&sdkplugin.Process{Manifest: &pluginv1.Manifest{Name: name, ExtensionPoints: []pluginv1.ExtensionPoint{point}, FileExtensions: exts}})
	}
	replace("json", pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC, "json", "json5")
	replace("d", pluginv1.ExtensionPoint_EXTENSION_POINT_RESOURCE_DETECTOR)
	replace("b", pluginv1.ExtensionPoint_EXTENSION_POINT_LANDING_BACKEND)
	replace("g", pluginv1.ExtensionPoint_EXTENSION_POINT_MESSAGE_GENERATOR)
	replace("c", pluginv1.ExtensionPoint_EXTENSION_POINT_LIST_COLUMN)
	if _, isFake := r.Codec("json").(fakeCodec); isFake {
		t.Error("the built-in codec stayed")
	}
	if _, ok := r.CodecFor("json5"); !ok {
		t.Error("the plugin's extension is missing")
	}
	if _, isFake := r.Detector("d").(fakeDetector); isFake {
		t.Error("detector stayed")
	}
	if _, isFake := r.Backend("b").(fakeBackend); isFake {
		t.Error("backend stayed")
	}
	if _, isFake := r.Generator("g").(fakeGenerator); isFake {
		t.Error("generator stayed")
	}
	if _, isFake := r.Column("c").(fakeColumn); isFake {
		t.Error("column stayed")
	}
}

func invalid(t *testing.T, err error) {
	t.Helper()
	if e, ok := errors.AsType[*rpc.Error](err); !ok || e.Code != rpc.InvalidArgument {
		t.Fatalf("want invalid_argument: %v", err)
	}
}

func TestBuiltinJSONCodec(t *testing.T) {
	ctx := context.Background()
	c := jsonCodec{}
	out, err := c.Encode(ctx, &pluginv1.EncodeRequest{Json: []byte(`{"a":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	back, err := c.Decode(ctx, &pluginv1.DecodeRequest{Source: out.GetSource()})
	if err != nil || !json.Valid(back.GetJson()) {
		t.Fatalf("%s %v", back.GetJson(), err)
	}
	_, err = c.Decode(ctx, &pluginv1.DecodeRequest{Source: []byte("nope")})
	invalid(t, err)
	_, err = c.Encode(ctx, &pluginv1.EncodeRequest{Json: []byte("nope")})
	invalid(t, err)
	_, err = c.Set(ctx, &pluginv1.SetRequest{})
	if e, ok := errors.AsType[*rpc.Error](err); !ok || e.Code != rpc.Unimplemented {
		t.Fatalf("%v", err)
	}
}

func TestBuiltinDetectorReportsProcessesInTheWorktree(t *testing.T) {
	d := processDetector{read: func(context.Context, ledger.Snapshot) (ledger.Snapshot, error) {
		return ledger.Snapshot{
			Cwds:      []ledger.CwdEntry{{Pid: 1, Path: "/w"}, {Pid: 2, Path: "/w/sub"}, {Pid: 3, Path: "/w2"}, {Pid: 4, Path: "/other"}},
			Processes: []ledger.Process{{Pid: 2, Command: "server"}},
		}, nil
	}}
	got, err := d.Detect(context.Background(), &pluginv1.DetectRequest{Worktree: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetResources()) != 2 || got.GetResources()[1].GetId() != "2" || got.GetResources()[1].GetDescription() != "server" {
		t.Fatalf("%v", got)
	}
	_, err = d.Detect(context.Background(), &pluginv1.DetectRequest{})
	invalid(t, err)
	failing := processDetector{read: func(context.Context, ledger.Snapshot) (ledger.Snapshot, error) {
		return ledger.Snapshot{}, errors.New("no ps")
	}}
	if _, err := failing.Detect(context.Background(), &pluginv1.DetectRequest{Worktree: "/w"}); err == nil {
		t.Fatal("no error")
	}
}

type stubBackend struct {
	landing.Backend
	err error
}

func (s stubBackend) Open(context.Context, landing.OpenRequest) (landing.PullRequest, error) {
	return landing.PullRequest{Number: 7, URL: "u", HeadRef: "h", HeadSHA: "s", BaseRef: "b", Draft: true}, s.err
}
func (s stubBackend) Merge(context.Context, landing.MergeRequest) (landing.MergeResult, error) {
	return landing.MergeResult{State: landing.MergeMerged, Commit: "c", Reason: "r"}, s.err
}
func (s stubBackend) Status(context.Context, int) (landing.Status, error) {
	return landing.Status{Number: 7, State: "open", HeadSHA: "s", Checks: []landing.Check{{Name: "ci", State: landing.CheckPassed}}}, s.err
}

func TestBuiltinBackendAdapter(t *testing.T) {
	ctx := context.Background()
	b := backendAdapter{stubBackend{}}
	opened, err := b.Open(ctx, &pluginv1.OpenRequest{Head: "h", Base: "b"})
	if err != nil || opened.GetNumber() != 7 || !opened.GetDraft() {
		t.Fatalf("%v %v", opened, err)
	}
	merged, err := b.Merge(ctx, &pluginv1.MergeRequest{Number: 7, Head: "h", Base: "b", Method: "squash"})
	if err != nil || merged.GetState() != "merged" || merged.GetCommit() != "c" {
		t.Fatalf("%v %v", merged, err)
	}
	status, err := b.Status(ctx, &pluginv1.StatusRequest{Number: 7})
	if err != nil || status.GetChecks()[0].GetState() != "passed" {
		t.Fatalf("%v %v", status, err)
	}
	_, err = b.Open(ctx, &pluginv1.OpenRequest{})
	invalid(t, err)
	_, err = b.Merge(ctx, &pluginv1.MergeRequest{Number: 1})
	invalid(t, err)
	_, err = b.Status(ctx, &pluginv1.StatusRequest{})
	invalid(t, err)

	failing := backendAdapter{stubBackend{err: errors.New("down")}}
	if _, err := failing.Open(ctx, &pluginv1.OpenRequest{Head: "h", Base: "b"}); err == nil {
		t.Error("open")
	}
	if _, err := failing.Merge(ctx, &pluginv1.MergeRequest{Number: 1, Head: "h", Base: "b"}); err == nil {
		t.Error("merge")
	}
	if _, err := failing.Status(ctx, &pluginv1.StatusRequest{Number: 1}); err == nil {
		t.Error("status")
	}
}

func TestBuiltinGenerator(t *testing.T) {
	ctx := context.Background()
	g := commitGenerator{generate: func(_ context.Context, prompt string) (commitmsg.Message, error) {
		return commitmsg.Message{Type: "feat", Subject: prompt}, nil
	}}
	got, err := g.Generate(ctx, &pluginv1.GenerateRequest{Kind: "commit", Prompt: "add it"})
	if err != nil || got.GetText() != "feat: add it" {
		t.Fatalf("%v %v", got, err)
	}
	_, err = g.Generate(ctx, &pluginv1.GenerateRequest{Kind: "branch"})
	invalid(t, err)
	failing := commitGenerator{generate: func(context.Context, string) (commitmsg.Message, error) {
		return commitmsg.Message{}, errors.New("no harness")
	}}
	if _, err := failing.Generate(ctx, &pluginv1.GenerateRequest{Kind: "commit"}); err == nil {
		t.Fatal("no error")
	}
}

func TestBuiltinBranchColumn(t *testing.T) {
	got, err := branchColumn{}.Compute(context.Background(), &pluginv1.ComputeRequest{Rows: []*pluginv1.ColumnRow{{Branch: "feat/a"}}})
	if err != nil || got.GetHeader() == "" || got.GetValues()[0] != "feat/a" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestBuiltinsPassTheConformanceScenarios(t *testing.T) {
	registry := NewRegistry()
	err := AddBuiltins(registry, BuiltinOptions{
		Read: func(context.Context, ledger.Snapshot) (ledger.Snapshot, error) { return ledger.Snapshot{}, nil },
		Generate: func(context.Context, string) (commitmsg.Message, error) {
			return commitmsg.Message{Type: "chore", Subject: "x"}, nil
		},
		Backend: stubBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := registry.Builtins()
	if len(names) != 5 {
		t.Fatalf("%v", names)
	}
	for _, name := range names {
		manifest, clients, ok := registry.Builtin(name)
		if !ok {
			t.Fatalf("%s is gone", name)
		}
		for _, check := range sdkplugin.Verify(context.Background(), manifest, clients) {
			if !check.Passed {
				t.Errorf("%s: %s: %s", name, check.Name, check.Detail)
			}
		}
	}
	if _, _, ok := registry.Builtin("absent"); ok {
		t.Error("found an absent built-in")
	}
}

func TestAddBuiltinsSkipsWhatItIsNotGiven(t *testing.T) {
	registry := NewRegistry()
	if err := AddBuiltins(registry, BuiltinOptions{}); err != nil {
		t.Fatal(err)
	}
	if registry.Backend(githubBuiltin) != nil {
		t.Fatal("a backend without a repository")
	}
	if registry.Detector(processBuiltin) == nil || registry.Generator(commitBuiltin) == nil {
		t.Fatal("the defaults were not registered")
	}
	if err := AddBuiltins(registry, BuiltinOptions{}); err == nil {
		t.Fatal("registered the built-ins twice")
	}
}

func TestDefaultGeneratorIsTheCommitMessageGenerator(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no true command")
	}
	generate := defaultGenerate(&commitmsg.Generator{Command: "true"})
	if _, err := generate(context.Background(), "prompt"); err == nil {
		t.Fatal("an empty answer was parsed")
	}
}
