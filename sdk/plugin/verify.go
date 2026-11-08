package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// Check is one scenario's outcome.
type Check struct {
	Name   string
	Passed bool
	Detail string
}

// sampleTree is the tree every config codec must carry unchanged.
const sampleTree = `{"name":"sample","count":3,"enabled":true,"nested":{"key":"value"}}`

// Verify runs the conformance scenarios for the extension points the manifest
// declares. No scenario can land a change or touch a repository: the landing
// scenarios send only requests a backend has to refuse.
func Verify(ctx context.Context, manifest *pluginv1.Manifest, clients Clients) []Check {
	checks := verifyManifest(manifest)
	for _, point := range manifest.GetExtensionPoints() {
		switch point {
		case pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC:
			checks = append(checks, verifyCodec(ctx, clients.ConfigCodec)...)
		case pluginv1.ExtensionPoint_EXTENSION_POINT_RESOURCE_DETECTOR:
			checks = append(checks, verifyDetector(ctx, clients.ResourceDetector))
		case pluginv1.ExtensionPoint_EXTENSION_POINT_LANDING_BACKEND:
			checks = append(checks, verifyBackend(ctx, clients.LandingBackend)...)
		case pluginv1.ExtensionPoint_EXTENSION_POINT_MESSAGE_GENERATOR:
			checks = append(checks, verifyGenerator(ctx, clients.MessageGenerator)...)
		case pluginv1.ExtensionPoint_EXTENSION_POINT_LIST_COLUMN:
			checks = append(checks, verifyColumn(ctx, clients.ListColumn))
		default:
			checks = append(checks, Check{Name: "extension point", Detail: fmt.Sprintf("unknown extension point %s", point)})
		}
	}
	return checks
}

func outcome(name string, err error) Check {
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	return Check{Name: name, Passed: true}
}

func verifyManifest(m *pluginv1.Manifest) []Check {
	var err error
	switch {
	case !ValidName(m.GetName()):
		err = fmt.Errorf("invalid name %q", m.GetName())
	case m.GetVersion() == "":
		err = errors.New("no version")
	case m.GetProtocolMajor() != ProtocolMajor:
		err = fmt.Errorf("protocol major %d, want %d", m.GetProtocolMajor(), ProtocolMajor)
	case len(m.GetExtensionPoints()) == 0:
		err = errors.New("no extension point")
	}
	return []Check{outcome("manifest", err)}
}

func sameTree(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func wantInvalid(err error) error {
	if err == nil {
		return errors.New("it was accepted")
	}
	if failure, ok := errors.AsType[*rpc.Error](err); !ok || failure.Code != rpc.InvalidArgument {
		return fmt.Errorf("want invalid_argument, got: %w", err)
	}
	return nil
}

func verifyCodec(ctx context.Context, codec pluginv1.ConfigCodecService) []Check {
	roundTrip := func() error {
		encoded, err := codec.Encode(ctx, &pluginv1.EncodeRequest{Json: []byte(sampleTree)})
		if err != nil {
			return err
		}
		decoded, err := codec.Decode(ctx, &pluginv1.DecodeRequest{Source: encoded.GetSource(), Path: "sample"})
		if err != nil {
			return err
		}
		if !sameTree(decoded.GetJson(), []byte(sampleTree)) {
			return fmt.Errorf("the tree came back as %s", decoded.GetJson())
		}
		return nil
	}
	refuses := func() error {
		_, err := codec.Decode(ctx, &pluginv1.DecodeRequest{Source: []byte("\x00 not a config \x00"), Path: "sample"})
		return wantInvalid(err)
	}
	set := func() error {
		encoded, err := codec.Encode(ctx, &pluginv1.EncodeRequest{Json: []byte(`{"name":"sample"}`)})
		if err != nil {
			return err
		}
		changed, err := codec.Set(ctx, &pluginv1.SetRequest{Source: encoded.GetSource(), Key: "added", JsonValue: []byte(`"yes"`)})
		if failure, ok := errors.AsType[*rpc.Error](err); ok && failure.Code == rpc.Unimplemented {
			return nil // Set is optional.
		}
		if err != nil {
			return err
		}
		decoded, err := codec.Decode(ctx, &pluginv1.DecodeRequest{Source: changed.GetSource(), Path: "sample"})
		if err != nil {
			return err
		}
		if !sameTree(decoded.GetJson(), []byte(`{"name":"sample","added":"yes"}`)) {
			return fmt.Errorf("after Set the tree is %s", decoded.GetJson())
		}
		return nil
	}
	return []Check{
		outcome("codec: a tree survives Encode then Decode", roundTrip()),
		outcome("codec: Decode refuses what it cannot read", refuses()),
		outcome("codec: Set changes one key, or is unimplemented", set()),
	}
}

func verifyDetector(ctx context.Context, detector pluginv1.ResourceDetectorService) Check {
	const name = "detector: an empty worktree gives well-formed resources"
	dir, err := os.MkdirTemp("", "hpv")
	if err != nil {
		return outcome(name, err)
	}
	defer os.RemoveAll(dir)
	response, err := detector.Detect(ctx, &pluginv1.DetectRequest{Worktree: dir, Task: "verify"})
	if err != nil {
		return outcome(name, err)
	}
	for _, resource := range response.GetResources() {
		if resource.GetKind() == "" || resource.GetId() == "" {
			return outcome(name, fmt.Errorf("a resource lacks a kind or an id: %v", resource))
		}
	}
	return outcome(name, nil)
}

func verifyBackend(ctx context.Context, backend pluginv1.LandingBackendService) []Check {
	_, open := backend.Open(ctx, &pluginv1.OpenRequest{})
	_, merge := backend.Merge(ctx, &pluginv1.MergeRequest{Number: 1, Method: "squash"})
	_, status := backend.Status(ctx, &pluginv1.StatusRequest{})
	return []Check{
		outcome("backend: Open refuses a request without a head", wantInvalid(open)),
		outcome("backend: Merge refuses a request without a pinned head", wantInvalid(merge)),
		outcome("backend: Status refuses pull request 0", wantInvalid(status)),
	}
}

func verifyGenerator(ctx context.Context, generator pluginv1.MessageGeneratorService) []Check {
	generates := func() error {
		response, err := generator.Generate(ctx, &pluginv1.GenerateRequest{Kind: "commit", Prompt: "Add a file named sample.txt."})
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace([]byte(response.GetText()))) == 0 {
			return errors.New("the text is empty")
		}
		return nil
	}
	_, unknown := generator.Generate(ctx, &pluginv1.GenerateRequest{Kind: "no-such-kind", Prompt: "x"})
	return []Check{
		outcome("generator: a commit prompt gives text", generates()),
		outcome("generator: an unknown kind is refused", wantInvalid(unknown)),
	}
}

func verifyColumn(ctx context.Context, column pluginv1.ListColumnService) Check {
	const name = "column: one value per row, and a header"
	rows := []*pluginv1.ColumnRow{{Task: "a", Branch: "feat/a", Path: "/a"}, {Task: "b", Branch: "feat/b", Path: "/b"}}
	response, err := column.Compute(ctx, &pluginv1.ComputeRequest{Rows: rows})
	if err != nil {
		return outcome(name, err)
	}
	if response.GetHeader() == "" {
		return outcome(name, errors.New("no header"))
	}
	if got := len(response.GetValues()); got != len(rows) {
		return outcome(name, fmt.Errorf("%d values for %d rows", got, len(rows)))
	}
	return outcome(name, nil)
}
