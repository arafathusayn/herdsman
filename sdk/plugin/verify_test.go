package plugin

import (
	"context"
	"strings"
	"testing"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

type goodCodec struct{ testCodec }

func (goodCodec) Decode(_ context.Context, r *pluginv1.DecodeRequest) (*pluginv1.DecodeResponse, error) {
	if !strings.HasPrefix(string(r.GetSource()), "{") {
		return nil, rpc.Errorf(rpc.InvalidArgument, "not a tree")
	}
	return &pluginv1.DecodeResponse{Json: r.GetSource()}, nil
}

type badRoundTrip struct{ goodCodec }

func (badRoundTrip) Encode(context.Context, *pluginv1.EncodeRequest) (*pluginv1.EncodeResponse, error) {
	return &pluginv1.EncodeResponse{Source: []byte(`{"other":"tree"}`)}, nil
}

type acceptsGarbage struct{ testCodec }

type setsNothing struct{ goodCodec }

func (setsNothing) Set(_ context.Context, r *pluginv1.SetRequest) (*pluginv1.SetResponse, error) {
	return &pluginv1.SetResponse{Source: r.GetSource()}, nil
}

type setsKey struct{ goodCodec }

func (setsKey) Set(context.Context, *pluginv1.SetRequest) (*pluginv1.SetResponse, error) {
	return &pluginv1.SetResponse{Source: []byte(`{"name":"sample","added":"yes"}`)}, nil
}

type anonymousResource struct{}

func (anonymousResource) Detect(context.Context, *pluginv1.DetectRequest) (*pluginv1.DetectResponse, error) {
	return &pluginv1.DetectResponse{Resources: []*pluginv1.DetectedResource{{Kind: "x"}}}, nil
}

type strictBackend struct{}

func (strictBackend) Open(context.Context, *pluginv1.OpenRequest) (*pluginv1.OpenResponse, error) {
	return nil, rpc.Errorf(rpc.InvalidArgument, "no head")
}
func (strictBackend) Merge(context.Context, *pluginv1.MergeRequest) (*pluginv1.MergeResponse, error) {
	return nil, rpc.Errorf(rpc.InvalidArgument, "no head")
}
func (strictBackend) Status(context.Context, *pluginv1.StatusRequest) (*pluginv1.StatusResponse, error) {
	return nil, rpc.Errorf(rpc.InvalidArgument, "no number")
}

type goodGenerator struct{}

func (goodGenerator) Generate(_ context.Context, r *pluginv1.GenerateRequest) (*pluginv1.GenerateResponse, error) {
	if r.GetKind() != "commit" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "unknown kind")
	}
	return &pluginv1.GenerateResponse{Text: "chore: x"}, nil
}

type lenientGenerator struct{}

func (lenientGenerator) Generate(context.Context, *pluginv1.GenerateRequest) (*pluginv1.GenerateResponse, error) {
	return &pluginv1.GenerateResponse{Text: "text"}, nil
}

type goodColumn struct{}

func (goodColumn) Compute(_ context.Context, r *pluginv1.ComputeRequest) (*pluginv1.ComputeResponse, error) {
	return &pluginv1.ComputeResponse{Header: "h", Values: make([]string, len(r.GetRows()))}, nil
}

type shortColumn struct{}

func (shortColumn) Compute(context.Context, *pluginv1.ComputeRequest) (*pluginv1.ComputeResponse, error) {
	return &pluginv1.ComputeResponse{Header: "h"}, nil
}

type headerlessColumn struct{}

func (headerlessColumn) Compute(_ context.Context, r *pluginv1.ComputeRequest) (*pluginv1.ComputeResponse, error) {
	return &pluginv1.ComputeResponse{Values: make([]string, len(r.GetRows()))}, nil
}

func manifestOf(point pluginv1.ExtensionPoint) *pluginv1.Manifest {
	return &pluginv1.Manifest{Name: "sample", Version: "1.0.0", ProtocolMajor: ProtocolMajor, ExtensionPoints: []pluginv1.ExtensionPoint{point}}
}

func passed(checks []Check) bool {
	for _, c := range checks {
		if !c.Passed {
			return false
		}
	}
	return len(checks) > 0
}

func TestVerifyEachExtensionPoint(t *testing.T) {
	const (
		codec     = pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC
		detector  = pluginv1.ExtensionPoint_EXTENSION_POINT_RESOURCE_DETECTOR
		backend   = pluginv1.ExtensionPoint_EXTENSION_POINT_LANDING_BACKEND
		generator = pluginv1.ExtensionPoint_EXTENSION_POINT_MESSAGE_GENERATOR
		column    = pluginv1.ExtensionPoint_EXTENSION_POINT_LIST_COLUMN
	)
	for name, c := range map[string]struct {
		point   pluginv1.ExtensionPoint
		clients Clients
		want    bool
	}{
		"codec":             {codec, Clients{ConfigCodec: goodCodec{}}, true},
		"codec with set":    {codec, Clients{ConfigCodec: setsKey{}}, true},
		"codec no change":   {codec, Clients{ConfigCodec: setsNothing{}}, false},
		"codec round trip":  {codec, Clients{ConfigCodec: badRoundTrip{}}, false},
		"codec garbage":     {codec, Clients{ConfigCodec: acceptsGarbage{}}, false},
		"detector":          {detector, Clients{ResourceDetector: testDetector{}}, true},
		"detector unnamed":  {detector, Clients{ResourceDetector: anonymousResource{}}, false},
		"backend":           {backend, Clients{LandingBackend: strictBackend{}}, true},
		"backend lenient":   {backend, Clients{LandingBackend: testBackend{}}, false},
		"generator":         {generator, Clients{MessageGenerator: goodGenerator{}}, true},
		"generator lenient": {generator, Clients{MessageGenerator: lenientGenerator{}}, false},
		"generator empty":   {generator, Clients{MessageGenerator: testGenerator{}}, false},
		"column":            {column, Clients{ListColumn: goodColumn{}}, true},
		"column short":      {column, Clients{ListColumn: shortColumn{}}, false},
		"column headerless": {column, Clients{ListColumn: headerlessColumn{}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			checks := Verify(context.Background(), manifestOf(c.point), c.clients)
			if got := passed(checks); got != c.want {
				t.Fatalf("passed %v, want %v: %+v", got, c.want, checks)
			}
		})
	}
}

func TestVerifyChecksTheManifest(t *testing.T) {
	for name, m := range map[string]*pluginv1.Manifest{
		"name":    {Name: "No Good", Version: "1", ProtocolMajor: ProtocolMajor},
		"version": {Name: "a", ProtocolMajor: ProtocolMajor},
		"major":   {Name: "a", Version: "1", ProtocolMajor: 4},
		"points":  {Name: "a", Version: "1", ProtocolMajor: ProtocolMajor},
		"unknown": {Name: "a", Version: "1", ProtocolMajor: ProtocolMajor, ExtensionPoints: []pluginv1.ExtensionPoint{99}},
	} {
		if passed(Verify(context.Background(), m, Clients{})) {
			t.Errorf("%s: passed", name)
		}
	}
}

func TestEnvironmentIsLimited(t *testing.T) {
	t.Setenv("HERDSMAN_PRIVATE_HOME", "/secret")
	t.Setenv("PATH", "/bin")
	env := strings.Join(Host{Env: []string{"A=b"}}.Environment(), "\n")
	if strings.Contains(env, "/secret") || !strings.Contains(env, "PATH=/bin") || !strings.Contains(env, "A=b") {
		t.Fatal(env)
	}
}
