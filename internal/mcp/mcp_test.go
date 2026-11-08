package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
)

var update = flag.Bool("update", false, "rewrite testdata/tools.golden")

// widgetProto has the shapes no real method has yet: a core Plan in a response,
// a streaming method, an idempotency option. Everything else is tested on the
// real contracts.
const widgetProto = `
name: "widget.proto"
package: "herdsman.widget.v1"
syntax: "proto3"
dependency: "herdsman/core/v1/core.proto"
message_type: { name: "GetRequest" field: { name: "id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL json_name: "id" } }
message_type: { name: "GetResponse" field: { name: "id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL json_name: "id" } }
message_type: { name: "MakeRequest"
  field: { name: "id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL json_name: "id" }
  field: { name: "dry_run" number: 2 type: TYPE_BOOL label: LABEL_OPTIONAL json_name: "dryRun" } }
message_type: { name: "MakeResponse"
  field: { name: "id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL json_name: "id" }
  field: { name: "plan" number: 2 type: TYPE_MESSAGE label: LABEL_OPTIONAL type_name: ".herdsman.core.v1.Plan" json_name: "plan" } }
message_type: { name: "OldRequest" field: { name: "id" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL json_name: "id" } }
message_type: { name: "OldResponse" }
service: { name: "WidgetService"
  method: { name: "Get" input_type: ".herdsman.widget.v1.GetRequest" output_type: ".herdsman.widget.v1.GetResponse" options: { idempotency_level: NO_SIDE_EFFECTS } }
  method: { name: "Make" input_type: ".herdsman.widget.v1.MakeRequest" output_type: ".herdsman.widget.v1.MakeResponse" }
  method: { name: "OldThing" input_type: ".herdsman.widget.v1.OldRequest" output_type: ".herdsman.widget.v1.OldResponse" }
  method: { name: "Watch" input_type: ".herdsman.widget.v1.GetRequest" output_type: ".herdsman.widget.v1.GetResponse" server_streaming: true }
}
`

func parseFile(t *testing.T, text string) protoreflect.FileDescriptor {
	t.Helper()
	return parseWith(t, text, protoregistry.GlobalFiles)
}

func parseWith(t *testing.T, text string, resolver protodesc.Resolver) protoreflect.FileDescriptor {
	t.Helper()
	var fdp descriptorpb.FileDescriptorProto
	if err := prototext.Unmarshal([]byte(text), &fdp); err != nil {
		t.Fatal(err)
	}
	file, err := protodesc.NewFile(&fdp, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func message(t *testing.T, file protoreflect.FileDescriptor, name string) protoreflect.MessageDescriptor {
	t.Helper()
	m := file.Messages().ByName(protoreflect.Name(name))
	if m == nil {
		t.Fatalf("no message %s", name)
	}
	return m
}

// session drives Serve over pipes, one line at a time.
type session struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	done   chan error
	closed bool
}

func start(t *testing.T, s *Server) *session {
	t.Helper()
	inRead, inWrite := io.Pipe()
	outRead, outWrite := io.Pipe()
	c := &session{t: t, in: inWrite, out: bufio.NewReader(outRead), done: make(chan error, 1)}
	go func() {
		err := s.Serve(context.Background(), inRead, outWrite)
		outWrite.Close()
		c.done <- err
	}()
	t.Cleanup(c.close)
	return c
}

func (c *session) close() {
	if !c.closed {
		c.closed = true
		c.in.Close()
		<-c.done
	}
}

func (c *session) write(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *session) read() reply {
	c.t.Helper()
	line, err := c.out.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("reading a reply: %v", err)
	}
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		c.t.Fatalf("reply %q: %v", line, err)
	}
	if r.JSONRPC != "2.0" {
		c.t.Fatalf("reply %q is not JSON-RPC 2.0", line)
	}
	return r
}

func (c *session) call(id int, method, params string) reply {
	c.t.Helper()
	c.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
	return c.read()
}

func (c *session) initialize() {
	c.t.Helper()
	if r := c.call(1, "initialize", `{"protocolVersion":"2025-06-18"}`); r.Error != nil {
		c.t.Fatalf("initialize: %+v", r.Error)
	}
}

type toolReply struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Structured map[string]any `json:"structuredContent"`
}

func (c *session) tool(name, args string) toolReply {
	c.t.Helper()
	r := c.call(2, "tools/call", fmt.Sprintf(`{"name":%q,"arguments":%s}`, name, args))
	if r.Error != nil {
		c.t.Fatalf("tools/call %s: %+v", name, r.Error)
	}
	var out toolReply
	if err := json.Unmarshal(r.Result, &out); err != nil {
		c.t.Fatal(err)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" {
		c.t.Fatalf("content: %+v", out.Content)
	}
	return out
}

func wantCode(t *testing.T, got toolReply, code string) {
	t.Helper()
	if !got.IsError || got.Structured["code"] != code {
		t.Fatalf("want a tool error %s, got %+v", code, got)
	}
}

// fake records what reached the daemon.
type fake struct {
	mu    sync.Mutex
	paths []string
	body  [][]byte
	reply func(path string, body []byte) ([]byte, error)
}

func (f *fake) call(_ context.Context, path string, body []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	f.body = append(f.body, body)
	if f.reply == nil {
		return nil, nil
	}
	return f.reply(path, body)
}

func (f *fake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func noDeny() ([]string, error) { return nil, nil }

// The real tools these tests call, one of each kind.
const (
	readTool   = "daemon_message_status"
	actTool    = "daemon_message_send"
	planTool   = "daemon_route_up"
	readMethod = "herdsman.daemon.v1.MessageService/Status"
)

func realServer(t *testing.T, f *fake, deny func() ([]string, error)) *session {
	t.Helper()
	s, err := NewServer(Files(), Options{Call: f.call, Deny: deny, Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	c := start(t, s)
	c.initialize()
	return c
}

func widgetServer(t *testing.T, f *fake, deny func() ([]string, error)) (*session, protoreflect.FileDescriptor) {
	t.Helper()
	file := parseFile(t, widgetProto)
	s, err := NewServer([]protoreflect.FileDescriptor{file}, Options{Call: f.call, Deny: deny, Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	c := start(t, s)
	c.initialize()
	return c, file
}

// The list of real tools is a contract: a name never changes once released.
func TestToolListGolden(t *testing.T) {
	s, err := NewServer(Files(), Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(s.Tools(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "tools.golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("tool list differs from %s; a released name may never change. Got:\n%s", path, got)
	}
}

func TestToolsComeFromTheDescriptors(t *testing.T) {
	f := &fake{}
	c, _ := widgetServer(t, f, noDeny)
	r := c.call(3, "tools/list", `{}`)
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	var list struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Annotations struct {
				ReadOnly    bool `json:"readOnlyHint"`
				Destructive bool `json:"destructiveHint"`
			} `json:"annotations"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		_, hasDry := tool.InputSchema["properties"].(map[string]any)["dry_run"]
		mutating := tool.Name != "widget_get"
		if hasDry != (tool.Name == "widget_make") || strings.Contains(tool.Description, "dry_run") != hasDry || tool.Annotations.ReadOnly == mutating || tool.Annotations.Destructive != mutating {
			t.Errorf("%s: dry_run %v, annotations %+v", tool.Name, hasDry, tool.Annotations)
		}
		if !strings.Contains(tool.Description, "herdsman.widget.v1.WidgetService/") {
			t.Errorf("%s: description %q", tool.Name, tool.Description)
		}
	}
	if want := []string{"widget_get", "widget_make", "widget_old_thing"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names %v, want %v (a streaming method has no tool)", names, want)
	}
}

func TestProceduresFollowTheTools(t *testing.T) {
	s, err := NewServer(Files(), Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Procedures()
	if len(got) != len(s.Tools()) || !slices.Contains(got, rpc.Path("herdsman.daemon.v1.RouteService", "Up")) {
		t.Fatalf("%v", got)
	}
}

func TestSnakeCase(t *testing.T) {
	for in, want := range map[string]string{
		"AddResource": "add_resource", "Get": "get", "GetURL": "get_url", "URLParts": "url_parts", "V1Thing": "v1_thing",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestServiceNamedLikeItsPackageKeepsNameShort(t *testing.T) {
	file := parseFile(t, `name: "a.proto" package: "herdsman.suggest.v1" syntax: "proto3"
message_type: { name: "R" }
service: { name: "SuggestService" method: { name: "Search" input_type: ".herdsman.suggest.v1.R" output_type: ".herdsman.suggest.v1.R" } }
service: { name: "SourceService" method: { name: "Search" input_type: ".herdsman.suggest.v1.R" output_type: ".herdsman.suggest.v1.R" } }`)
	s, err := NewServer([]protoreflect.FileDescriptor{file}, Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range s.Tools() {
		names = append(names, tool.Name)
	}
	if want := []string{"suggest_search", "suggest_source_search"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names %v, want %v", names, want)
	}
}

// Agents must not be able to approve themselves or stop the daemon.
func TestPeopleOnlyMethodsHaveNoTool(t *testing.T) {
	s, err := NewServer(Files(), Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	service := daemonv1.File_herdsman_daemon_v1_message_proto.Services().ByName("MessageService")
	names := map[string]bool{}
	for _, tool := range s.Tools() {
		names[tool.Name] = true
	}
	for _, name := range []protoreflect.Name{"Shutdown", "Enroll", "ApprovePair", "RetirePair", "RevokeCredential", "ResetEvents"} {
		if service.Methods().ByName(name) == nil {
			t.Errorf("%s is no longer in the contract", name)
		}
		if names["daemon_message_"+snake(string(name))] {
			t.Errorf("%s has a tool", name)
		}
	}
	if !names[actTool] {
		t.Fatalf("no %s among %v", actTool, names)
	}
}

// A tool offers dry_run only when the daemon answers it with a plan.
func TestOnlyToolsThatPlanAdvertiseDryRun(t *testing.T) {
	s, err := NewServer(Files(), Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	var planning []string
	for _, tool := range s.Tools() {
		_, hasDry := tool.InputSchema["properties"].(map[string]any)[dryRunField]
		if hasDry {
			planning = append(planning, tool.Name)
		}
		if strings.Contains(tool.Description, dryRunField) != hasDry {
			t.Errorf("%s: dry_run in the schema %v, description %q", tool.Name, hasDry, tool.Description)
		}
	}
	if want := []string{planTool}; !reflect.DeepEqual(planning, want) {
		t.Fatalf("tools offering dry_run %v, want %v", planning, want)
	}
}

func upReply(t *testing.T, start *daemonv1.RouteStart) func(string, []byte) ([]byte, error) {
	t.Helper()
	data := mustMarshal(t, &daemonv1.UpResponse{Start: start})
	return func(string, []byte) ([]byte, error) { return data, nil }
}

func TestRouteUpDryRunReturnsTheDaemonsPlan(t *testing.T) {
	f := &fake{}
	c := realServer(t, f, noDeny)
	f.reply = upReply(t, &daemonv1.RouteStart{State: "planned", Plan: "run the adjutant"})
	got := c.tool(planTool, `{"repo":"/r","dry_run":true}`)
	if got.IsError {
		t.Fatalf("%+v", got)
	}
	plan, ok := got.Structured["plan"].(map[string]any)
	if !ok || plan["state"] != "planned" || plan["plan"] != "run the adjutant" {
		t.Fatalf("%+v", got.Structured)
	}
	sent := &daemonv1.UpRequest{}
	if err := proto.Unmarshal(f.body[0], sent); err != nil {
		t.Fatal(err)
	}
	if !sent.GetDryRun() || f.paths[0] != rpc.Path("herdsman.daemon.v1.RouteService", "Up") {
		t.Fatalf("%s %+v", f.paths[0], sent)
	}
}

func TestRouteUpAnswerWithoutPlanTextIsAnError(t *testing.T) {
	for name, start := range map[string]*daemonv1.RouteStart{"no start": nil, "no text": {State: "planned"}} {
		t.Run(name, func(t *testing.T) {
			f := &fake{}
			c := realServer(t, f, noDeny)
			f.reply = upReply(t, start)
			wantCode(t, c.tool(planTool, `{"dry_run":true}`), codePlanMissing)
		})
	}
}

// The daemon starts a route only for a person at a terminal, so the tool never
// asks it to.
func TestRouteUpOnlyPlans(t *testing.T) {
	for name, args := range map[string]string{"no dry_run": `{"repo":"/r"}`, "false": `{"dry_run":false}`} {
		t.Run(name, func(t *testing.T) {
			f := &fake{}
			c := realServer(t, f, noDeny)
			wantCode(t, c.tool(planTool, args), codePlanOnly)
			if f.calls() != 0 {
				t.Fatalf("the daemon was called: %v", f.paths)
			}
		})
	}
}

func TestPlanOnlyToolIsNotMarkedDestructive(t *testing.T) {
	s, err := NewServer(Files(), Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range s.Tools() {
		if tool.Name == planTool && (!tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint) {
			t.Fatalf("%+v", tool.Annotations)
		}
	}
}

func TestUnknownMethodsCountAsMutating(t *testing.T) {
	// Status is read-only by the table, not by an option in its proto.
	file := parseFile(t, `name: "a.proto" package: "herdsman.daemon.v1" syntax: "proto3"
message_type: { name: "R" }
service: { name: "MessageService"
 method: { name: "Status" input_type: ".herdsman.daemon.v1.R" output_type: ".herdsman.daemon.v1.R" }
 method: { name: "Brandnew" input_type: ".herdsman.daemon.v1.R" output_type: ".herdsman.daemon.v1.R" } }`)
	s, err := NewServer([]protoreflect.FileDescriptor{file}, Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range s.Tools() {
		got[tool.Name] = tool.Annotations.ReadOnlyHint
	}
	if want := map[string]bool{"daemon_message_status": true, "daemon_message_brandnew": false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only %v, want %v", got, want)
	}
}

func TestNewServerRefusesBadDescriptors(t *testing.T) {
	ok := Options{Call: (&fake{}).call, Deny: noDeny}
	body := `message_type: { name: "R" } `
	method := func(pkg, name string) string {
		return fmt.Sprintf(`method: { name: %q input_type: ".%s.R" output_type: ".%s.R" }`, name, pkg, pkg)
	}
	cases := map[string]string{
		"package": `name: "a.proto" package: "other.v1" syntax: "proto3" ` + body + `service: { name: "XService" ` + method("other.v1", "A") + ` }`,
		"long": `name: "a.proto" package: "herdsman.x.v1" syntax: "proto3" ` + body + `service: { name: "XService" ` +
			method("herdsman.x.v1", strings.Repeat("A", 64)) + ` }`,
		"duplicate": `name: "a.proto" package: "herdsman.x.v1" syntax: "proto3" ` + body + `service: { name: "XService" ` +
			method("herdsman.x.v1", "GetURL") + method("herdsman.x.v1", "GetUrl") + ` }`,
	}
	for name, text := range cases {
		if _, err := NewServer([]protoreflect.FileDescriptor{parseFile(t, text)}, ok); err == nil {
			t.Errorf("%s: NewServer accepted it", name)
		}
	}
	if _, err := NewServer(nil, Options{Deny: noDeny}); err == nil {
		t.Error("NewServer accepted a nil Call")
	}
}

func TestSchemaFromMessage(t *testing.T) {
	// The vendored protobuf has no Timestamp package, so the test declares one.
	known := new(protoregistry.Files)
	for _, f := range []protoreflect.FileDescriptor{anypb.File_google_protobuf_any_proto, parseFile(t, `name: "google/protobuf/timestamp.proto" package: "google.protobuf" syntax: "proto3" message_type: { name: "Timestamp" }`)} {
		if err := known.RegisterFile(f); err != nil {
			t.Fatal(err)
		}
	}
	file := parseWith(t, `name: "s.proto" package: "herdsman.s.v1" syntax: "proto3"
dependency: "google/protobuf/timestamp.proto"
dependency: "google/protobuf/any.proto"
enum_type: { name: "Color" value: { name: "COLOR_UNSPECIFIED" number: 0 } value: { name: "RED" number: 1 } }
message_type: { name: "Node"
 field: { name: "next" number: 1 type: TYPE_MESSAGE label: LABEL_OPTIONAL type_name: ".herdsman.s.v1.Node" json_name: "next" }
 field: { name: "a_bool" number: 2 type: TYPE_BOOL label: LABEL_OPTIONAL json_name: "aBool" }
 field: { name: "a_string" number: 3 type: TYPE_STRING label: LABEL_OPTIONAL json_name: "aString" }
 field: { name: "a_bytes" number: 4 type: TYPE_BYTES label: LABEL_OPTIONAL json_name: "aBytes" }
 field: { name: "an_int32" number: 5 type: TYPE_INT32 label: LABEL_OPTIONAL json_name: "anInt32" }
 field: { name: "a_uint32" number: 6 type: TYPE_UINT32 label: LABEL_OPTIONAL json_name: "aUint32" }
 field: { name: "an_int64" number: 7 type: TYPE_INT64 label: LABEL_OPTIONAL json_name: "anInt64" }
 field: { name: "a_double" number: 8 type: TYPE_DOUBLE label: LABEL_OPTIONAL json_name: "aDouble" }
 field: { name: "a_color" number: 9 type: TYPE_ENUM label: LABEL_OPTIONAL type_name: ".herdsman.s.v1.Color" json_name: "aColor" }
 field: { name: "many" number: 10 type: TYPE_STRING label: LABEL_REPEATED json_name: "many" }
 field: { name: "by_key" number: 11 type: TYPE_MESSAGE label: LABEL_REPEATED type_name: ".herdsman.s.v1.Node.ByKeyEntry" json_name: "byKey" }
 field: { name: "at" number: 12 type: TYPE_MESSAGE label: LABEL_OPTIONAL type_name: ".google.protobuf.Timestamp" json_name: "at" }
 field: { name: "anything" number: 13 type: TYPE_MESSAGE label: LABEL_OPTIONAL type_name: ".google.protobuf.Any" json_name: "anything" }
 nested_type: { name: "ByKeyEntry" options: { map_entry: true }
  field: { name: "key" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL json_name: "key" }
  field: { name: "value" number: 2 type: TYPE_INT32 label: LABEL_OPTIONAL json_name: "value" } }
}`, known)
	got := inputSchema(message(t, file, "Node"), false)
	props := got["properties"].(map[string]any)
	max32 := map[string]any{"type": "integer", "minimum": int64(-2147483648), "maximum": int64(2147483647)}
	want := map[string]any{
		"next":     map[string]any{"$ref": "#/$defs/herdsman.s.v1.Node"},
		"a_bool":   map[string]any{"type": "boolean"},
		"a_string": map[string]any{"type": "string"},
		"a_bytes":  map[string]any{"type": "string", "contentEncoding": "base64"},
		"an_int32": max32,
		"a_uint32": map[string]any{"type": "integer", "minimum": int64(0), "maximum": int64(4294967295)},
		"an_int64": map[string]any{"type": []string{"integer", "string"}},
		"a_double": map[string]any{"type": []string{"number", "string"}},
		"a_color":  map[string]any{"type": "string", "enum": []string{"COLOR_UNSPECIFIED", "RED"}},
		"many":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"by_key":   map[string]any{"type": "object", "additionalProperties": max32},
		"at":       map[string]any{"type": "string"},
		"anything": map[string]any{},
	}
	if !reflect.DeepEqual(props, want) {
		t.Fatalf("properties\n got %#v\nwant %#v", props, want)
	}
	if got["additionalProperties"] != false || got["type"] != "object" {
		t.Fatalf("root %#v", got)
	}
	if _, ok := got["$defs"].(map[string]any)["herdsman.s.v1.Node"]; !ok {
		t.Fatalf("a recursive message needs a definition: %#v", got["$defs"])
	}
	if err := json.NewEncoder(io.Discard).Encode(got); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaHasNoDefsWithoutNestedMessages(t *testing.T) {
	file := parseFile(t, widgetProto)
	got := inputSchema(message(t, file, "MakeRequest"), true)
	if _, ok := got["$defs"]; ok {
		t.Fatalf("unexpected $defs: %#v", got)
	}
	if got["properties"].(map[string]any)["dry_run"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("no dry_run: %#v", got)
	}
}

func TestSchemaHidesDryRunWithoutAPlan(t *testing.T) {
	file := parseFile(t, widgetProto)
	got := inputSchema(message(t, file, "MakeRequest"), false)
	if _, ok := got["properties"].(map[string]any)["dry_run"]; ok {
		t.Fatalf("dry_run offered: %#v", got)
	}
}

func TestInitializeNegotiatesVersion(t *testing.T) {
	s, err := NewServer(nil, Options{Call: (&fake{}).call, Deny: noDeny, Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	c := start(t, s)
	type init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct{ Name, Version string }
		Capabilities    map[string]any
	}
	check := func(asked, want string) {
		t.Helper()
		r := c.call(1, "initialize", fmt.Sprintf(`{"protocolVersion":%q}`, asked))
		var got init
		if err := json.Unmarshal(r.Result, &got); err != nil {
			t.Fatal(err)
		}
		if got.ProtocolVersion != want || got.ServerInfo.Name != "herdsman" || got.ServerInfo.Version != "1.2.3" || got.Capabilities["tools"] == nil {
			t.Fatalf("asked %s: %+v", asked, got)
		}
	}
	check("2024-11-05", "2024-11-05")
	check("1999-01-01", latestProtocol)
}

func TestProtocolErrors(t *testing.T) {
	s, err := NewServer(nil, Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	c := start(t, s)
	expect := func(line string, code int) {
		t.Helper()
		c.write(line)
		r := c.read()
		if r.Error == nil || r.Error.Code != code {
			t.Fatalf("%s: %+v", line, r)
		}
	}
	expect(`{not json`, codeParse)
	expect(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, codeInvalidRequest)
	expect(`{"jsonrpc":"1.0","id":1,"method":"ping"}`, codeInvalidRequest)
	expect(`{"jsonrpc":"2.0","id":1}`, codeInvalidRequest)
	expect(`{"jsonrpc":"2.0","id":null,"method":"ping"}`, codeInvalidRequest)
	expect(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, codeNotInitialized)
	expect(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`, codeNotInitialized)
	expect(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":[]}`, codeInvalidParams)
	if r := c.call(9, "ping", `{}`); r.Error != nil || string(r.Result) != `{}` || string(r.ID) != "9" {
		t.Fatalf("ping before initialize: %+v", r)
	}
	c.initialize()
	expect(`{"jsonrpc":"2.0","id":1,"method":"no/such"}`, codeMethodNotFound)
	expect(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such"}}`, codeInvalidParams)
	expect(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":7}`, codeInvalidParams)
}

func TestNotificationsAndBlankLinesGetNoReply(t *testing.T) {
	s, err := NewServer(nil, Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	c := start(t, s)
	c.write(``)
	c.write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	c.write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":7}`)
	c.write(`{"jsonrpc":"2.0","method":"notifications/other"}`)
	if r := c.call(5, "ping", `{}`); string(r.ID) != "5" {
		t.Fatalf("the first reply belongs to the ping: %+v", r)
	}
}

func TestOversizedLineIsRefusedAndServingContinues(t *testing.T) {
	s, err := NewServer(nil, Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	c := start(t, s)
	// The reply arrives before the pipe has taken the whole line; a failure
	// here shows up as the missing reply below.
	go io.WriteString(c.in, strings.Repeat("x", rpc.MaxBody+1)+"\n")
	if r := c.read(); r.Error == nil || r.Error.Code != codeInvalidRequest {
		t.Fatalf("%+v", r)
	}
	if r := c.call(6, "ping", `{}`); r.Error != nil {
		t.Fatalf("%+v", r)
	}
}

func TestFinalLineWithoutNewlineIsServed(t *testing.T) {
	s, err := NewServer(nil, Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"ping"}`), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":3`) {
		t.Fatalf("output %q", out.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

func TestWriteFailureIsReturned(t *testing.T) {
	s, err := NewServer(nil, Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"ping"}`+"\n"), failingWriter{})
	if err == nil || !strings.Contains(err.Error(), "closed pipe") {
		t.Fatalf("err %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestReadFailureIsReturned(t *testing.T) {
	s, err := NewServer(nil, Options{Call: (&fake{}).call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Serve(context.Background(), failingReader{}, io.Discard); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("err %v", err)
	}
}

func marshalMake(t *testing.T, file protoreflect.FileDescriptor, plan *corev1.Plan) []byte {
	t.Helper()
	out := dynamicpb.NewMessage(message(t, file, "MakeResponse"))
	if plan != nil {
		field := out.Descriptor().Fields().ByName("plan")
		raw, err := proto.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		inner := out.NewField(field)
		if err := proto.Unmarshal(raw, inner.Message().Interface()); err != nil {
			t.Fatal(err)
		}
		out.Set(field, inner)
	} else {
		out.Set(out.Descriptor().Fields().ByName("id"), protoreflect.ValueOfString("made"))
	}
	data, err := proto.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDryRunReturnsThePlanTheDaemonBuilt(t *testing.T) {
	f := &fake{}
	c, file := widgetServer(t, f, noDeny)
	f.reply = func(string, []byte) ([]byte, error) {
		return marshalMake(t, file, &corev1.Plan{Steps: []*corev1.PlanStep{{Action: "create", Target: "w1", Irreversible: true}}}), nil
	}
	got := c.tool("widget_make", `{"id":"w1","dry_run":true}`)
	if got.IsError {
		t.Fatalf("%+v", got)
	}
	plan, ok := got.Structured["plan"].(map[string]any)
	if !ok {
		t.Fatalf("no plan: %+v", got.Structured)
	}
	step := plan["steps"].([]any)[0].(map[string]any)
	if step["action"] != "create" || step["target"] != "w1" || step["irreversible"] != true {
		t.Fatalf("step %+v", step)
	}
	if want := rpc.Path("herdsman.widget.v1.WidgetService", "Make"); f.paths[0] != want {
		t.Fatalf("path %s, want %s", f.paths[0], want)
	}
	sent := dynamicpb.NewMessage(message(t, file, "MakeRequest"))
	if err := proto.Unmarshal(f.body[0], sent); err != nil {
		t.Fatal(err)
	}
	if !sent.Get(sent.Descriptor().Fields().ByName("dry_run")).Bool() {
		t.Fatal("the daemon was not told dry_run")
	}
}

func TestDryRunAnswerWithoutAPlanIsAnError(t *testing.T) {
	f := &fake{}
	c, file := widgetServer(t, f, noDeny)
	f.reply = func(string, []byte) ([]byte, error) { return marshalMake(t, file, nil), nil }
	wantCode(t, c.tool("widget_make", `{"dry_run":true}`), codePlanMissing)
}

// A daemon ignores a field it does not know and acts, so the request must
// never leave this process.
func TestDryRunNeverReachesAMethodThatCannotPlan(t *testing.T) {
	f := &fake{}
	c := realServer(t, f, noDeny)
	wantCode(t, c.tool(actTool, `{"dry_run":true}`), codeDryRunUnsupported)
	if f.calls() != 0 {
		t.Fatalf("the daemon was called: %v", f.paths)
	}
}

func TestHalfADryRunContractCannotPlan(t *testing.T) {
	file := parseFile(t, `name: "h.proto" package: "herdsman.h.v1" syntax: "proto3"
message_type: { name: "Yes" field: { name: "dry_run" number: 1 type: TYPE_BOOL label: LABEL_OPTIONAL json_name: "dryRun" } }
message_type: { name: "Odd" field: { name: "dry_run" number: 1 type: TYPE_STRING label: LABEL_OPTIONAL json_name: "dryRun" } }
message_type: { name: "Num" field: { name: "plan" number: 1 type: TYPE_INT32 label: LABEL_OPTIONAL json_name: "plan" } }
message_type: { name: "Many" field: { name: "items" number: 1 type: TYPE_MESSAGE label: LABEL_REPEATED type_name: ".herdsman.h.v1.Num" json_name: "items" } }
message_type: { name: "One" field: { name: "item" number: 1 type: TYPE_MESSAGE label: LABEL_OPTIONAL type_name: ".herdsman.h.v1.Num" json_name: "item" } }
service: { name: "HService"
 method: { name: "NoPlan" input_type: ".herdsman.h.v1.Yes" output_type: ".herdsman.h.v1.Yes" }
 method: { name: "NotBool" input_type: ".herdsman.h.v1.Odd" output_type: ".herdsman.h.v1.Yes" }
 method: { name: "ListOfThem" input_type: ".herdsman.h.v1.Yes" output_type: ".herdsman.h.v1.Many" }
 method: { name: "PlanNotText" input_type: ".herdsman.h.v1.Yes" output_type: ".herdsman.h.v1.One" } }`)
	methods := file.Services().Get(0).Methods()
	for i := range methods.Len() {
		if dry, plan, text := planFields(methods.Get(i)); dry != nil || plan != nil || text != nil {
			t.Errorf("%s can plan", methods.Get(i).Name())
		}
	}
}

func TestReplyThatCannotBeWrittenIsAnError(t *testing.T) {
	file := parseWith(t, `name: "b.proto" package: "herdsman.b.v1" syntax: "proto3"
dependency: "google/protobuf/any.proto"
message_type: { name: "B" field: { name: "x" number: 1 type: TYPE_MESSAGE label: LABEL_OPTIONAL type_name: ".google.protobuf.Any" json_name: "x" } }`, protoregistry.GlobalFiles)
	reply := dynamicpb.NewMessage(message(t, file, "B"))
	field := reply.Descriptor().Fields().ByName("x")
	inner := reply.NewField(field)
	if err := proto.Unmarshal(mustMarshal(t, &anypb.Any{TypeUrl: "type.googleapis.com/no.Such"}), inner.Message().Interface()); err != nil {
		t.Fatal(err)
	}
	reply.Set(field, inner)
	got := encodeResponse(&tool{}, reply, false)
	if !got.IsError || !strings.Contains(got.Content[0].Text, codeBadReply) {
		t.Fatalf("%+v", got)
	}
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDryRunFalseStillActs(t *testing.T) {
	f := &fake{}
	c := realServer(t, f, noDeny)
	if got := c.tool(actTool, `{"dry_run":false}`); got.IsError {
		t.Fatalf("%+v", got)
	}
	if f.calls() != 1 {
		t.Fatalf("calls %d", f.calls())
	}
}

func TestInvalidArgumentsNeverReachTheDaemon(t *testing.T) {
	f := &fake{}
	c := realServer(t, f, noDeny)
	for name, tool := range map[string][2]string{
		"unknown field":      {readTool, `{"nope":1}`},
		"wrong type":         {readTool, `{"route":7}`},
		"not an object":      {readTool, `[1]`},
		"dry_run read-only":  {readTool, `{"dry_run":true}`},
		"dry_run not a bool": {actTool, `{"dry_run":"yes"}`},
		"camel spelling":     {actTool, `{"dryRun":true}`},
	} {
		t.Run(name, func(t *testing.T) { wantCode(t, c.tool(tool[0], tool[1]), codeInvalidArgument) })
	}
	if f.calls() != 0 {
		t.Fatalf("the daemon was called: %v", f.paths)
	}
}

func TestMissingArgumentsMeanAnEmptyRequest(t *testing.T) {
	f := &fake{}
	c := realServer(t, f, noDeny)
	r := c.call(4, "tools/call", `{"name":"`+readTool+`"}`)
	if r.Error != nil || !strings.Contains(string(r.Result), `"structuredContent":{}`) {
		t.Fatalf("%s %+v", r.Result, r.Error)
	}
	if got := c.tool(readTool, `null`); got.IsError {
		t.Fatalf("%+v", got)
	}
}

func TestDenyApplies(t *testing.T) {
	cases := map[string][]string{
		"tool name":  {planTool},
		"glob":       {"daemon_*"},
		"procedure":  {"herdsman.daemon.v1.RouteService/Up"},
		"among more": {"git push", planTool},
	}
	for name, deny := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{}
			c := realServer(t, f, func() ([]string, error) { return deny, nil })
			wantCode(t, c.tool(planTool, `{}`), codeDenied)
			wantCode(t, c.tool(planTool, `{"dry_run":true}`), codeDenied)
			if f.calls() != 0 {
				t.Fatalf("the daemon was called: %v", f.paths)
			}
		})
	}
}

func TestDenyLeavesOtherToolsAlone(t *testing.T) {
	f := &fake{}
	c := realServer(t, f, func() ([]string, error) { return []string{planTool, "git push"}, nil })
	if got := c.tool(readTool, `{}`); got.IsError {
		t.Fatalf("%+v", got)
	}
}

func TestPolicyIsReadOnEveryCall(t *testing.T) {
	f := &fake{}
	var denied atomic.Bool
	c := realServer(t, f, func() ([]string, error) {
		if denied.Load() {
			return []string{readMethod}, nil
		}
		return nil, nil
	})
	if got := c.tool(readTool, `{}`); got.IsError {
		t.Fatalf("%+v", got)
	}
	denied.Store(true)
	wantCode(t, c.tool(readTool, `{}`), codeDenied)
}

func TestUnreadablePolicyRefusesEverything(t *testing.T) {
	cases := map[string]func() ([]string, error){
		"load error":      func() ([]string, error) { return nil, errors.New("disk") },
		"no loader":       nil,
		"malformed entry": func() ([]string, error) { return []string{"daemon["}, nil },
	}
	for name, deny := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{}
			c := realServer(t, f, deny)
			wantCode(t, c.tool(readTool, `{}`), codePolicyUnavailable)
			if f.calls() != 0 {
				t.Fatalf("the daemon was called: %v", f.paths)
			}
		})
	}
}

func TestDaemonFailuresBecomeToolErrors(t *testing.T) {
	f := &fake{}
	c := realServer(t, f, noDeny)
	f.reply = func(string, []byte) ([]byte, error) { return nil, rpc.Errorf(rpc.InvalidArgument, "bad id") }
	got := c.tool(readTool, `{}`)
	wantCode(t, got, string(rpc.InvalidArgument))
	if got.Structured["message"] != "bad id" || !strings.Contains(got.Content[0].Text, "bad id") {
		t.Fatalf("%+v", got)
	}
	f.reply = func(string, []byte) ([]byte, error) { return nil, fmt.Errorf("wrapped: %w", errors.New("no socket")) }
	got = c.tool(readTool, `{}`)
	if !got.IsError || !strings.Contains(got.Content[0].Text, "no socket") {
		t.Fatalf("%+v", got)
	}
}

func TestUndecodableReplyIsAnError(t *testing.T) {
	f := &fake{}
	c := realServer(t, f, noDeny)
	f.reply = func(string, []byte) ([]byte, error) { return []byte{0xff}, nil }
	wantCode(t, c.tool(readTool, `{}`), codeBadReply)
}

func TestSuccessCarriesTheResponse(t *testing.T) {
	f := &fake{}
	c, file := widgetServer(t, f, noDeny)
	f.reply = func(string, []byte) ([]byte, error) { return marshalMake(t, file, nil), nil }
	got := c.tool("widget_make", `{"id":"x"}`)
	if got.IsError || got.Structured["id"] != "made" || !strings.Contains(got.Content[0].Text, `"id":"made"`) {
		t.Fatalf("%+v", got)
	}
}

// blocker holds calls until their context ends.
type blocker struct {
	started chan struct{}
	ended   chan struct{}
}

func (b *blocker) call(ctx context.Context, _ string, _ []byte) ([]byte, error) {
	b.started <- struct{}{}
	<-ctx.Done()
	b.ended <- struct{}{}
	return nil, ctx.Err()
}

func blockedSession(t *testing.T) (*session, *blocker) {
	t.Helper()
	b := &blocker{started: make(chan struct{}, maxInFlight+1), ended: make(chan struct{}, maxInFlight+1)}
	s, err := NewServer([]protoreflect.FileDescriptor{parseFile(t, widgetProto)}, Options{Call: b.call, Deny: noDeny})
	if err != nil {
		t.Fatal(err)
	}
	c := start(t, s)
	c.initialize()
	return c, b
}

func waitFor(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestCancelledCallStopsAndGetsNoReply(t *testing.T) {
	c, b := blockedSession(t)
	c.write(`{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"widget_get"}}`)
	waitFor(t, b.started, "the call")
	c.write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`)
	c.write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":40}}`)
	waitFor(t, b.ended, "the cancellation")
	if r := c.call(41, "ping", `{}`); string(r.ID) != "41" {
		t.Fatalf("a cancelled call was answered: %+v", r)
	}
}

func TestEndOfInputCancelsCallsInFlight(t *testing.T) {
	c, b := blockedSession(t)
	c.write(`{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"widget_get"}}`)
	waitFor(t, b.started, "the call")
	c.close()
	waitFor(t, b.ended, "the cancellation")
}

func TestDuplicateRunningIDIsRefused(t *testing.T) {
	c, b := blockedSession(t)
	c.write(`{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"widget_get"}}`)
	waitFor(t, b.started, "the call")
	r := c.call(40, "tools/call", `{"name":"widget_get"}`)
	if r.Error == nil || r.Error.Code != codeInvalidRequest {
		t.Fatalf("%+v", r)
	}
}

func TestTooManyCallsAreRefused(t *testing.T) {
	c, b := blockedSession(t)
	for i := range maxInFlight {
		c.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"widget_get"}}`, 100+i))
		waitFor(t, b.started, "a call")
	}
	r := c.call(7, "tools/call", `{"name":"widget_get"}`)
	if r.Error == nil || r.Error.Code != codeBusy {
		t.Fatalf("%+v", r)
	}
}
