package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The codes of a failed tool call that never came from the daemon. Callers
// branch on these, so they do not change.
const (
	codeInvalidArgument   = "invalid_argument"
	codeDenied            = "policy_denied"
	codePolicyUnavailable = "policy_unavailable"
	codeDryRunUnsupported = "dry_run_unsupported"
	codePlanOnly          = "plan_only"
	codePlanMissing       = "plan_missing"
	codeBadReply          = "bad_reply"
)

var toolNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// readOnlyMethods names the methods known to change nothing, until their protos
// say so with idempotency_level. A method on neither list counts as mutating.
var readOnlyMethods = map[protoreflect.FullName]bool{
	"herdsman.daemon.v1.EventService.Wait":             true,
	"herdsman.daemon.v1.MessageService.Hello":          true,
	"herdsman.daemon.v1.MessageService.Health":         true,
	"herdsman.daemon.v1.MessageService.ListRoutes":     true,
	"herdsman.daemon.v1.MessageService.Status":         true,
	"herdsman.daemon.v1.MessageService.GetSetup":       true,
	"herdsman.daemon.v1.MessageService.DaemonStatus":   true,
	"herdsman.daemon.v1.MessageService.ListOperations": true,
	"herdsman.daemon.v1.MessageService.GetOperation":   true,
	"herdsman.daemon.v1.OpsService.Records":            true,
	"herdsman.daemon.v1.RouteService.Get":              true,
	"herdsman.daemon.v1.RouteService.List":             true,
	"herdsman.config.v1.ConfigService.Export":          true,
	"herdsman.config.v1.ConfigService.Get":             true,
	"herdsman.config.v1.ConfigService.Show":            true,
	"herdsman.config.v1.ConfigService.Validate":        true,
	"herdsman.suggest.v1.SuggestService.Search":        true,
	"herdsman.suggest.v1.SuggestService.Get":           true,
	"herdsman.suggest.v1.SuggestService.Plan":          true,
	"herdsman.suggest.v1.SuggestService.Sources":       true,
}

// planOnlyMethods answer dry runs only. The daemon starts a route for a person
// at a terminal and refuses a caller under a coding agent, so a tool that asked
// it to could only ever fail. The verbs after it end or remove a person's route
// and confirm on a terminal first, which a tool call would pass over.
var planOnlyMethods = map[protoreflect.FullName]bool{
	"herdsman.daemon.v1.RouteService.Up":     true,
	"herdsman.daemon.v1.RouteService.Pause":  true,
	"herdsman.daemon.v1.RouteService.Resume": true,
	"herdsman.daemon.v1.RouteService.Stop":   true,
	"herdsman.daemon.v1.RouteService.Delete": true,
}

// peopleOnlyMethods get no tool. An agent holding this server runs as the same
// user as the person it works for, so these are the decisions it must not be
// able to make for them: who may talk on a channel, whether the daemon runs,
// and what the safety settings and hooks in a configuration file say.
// An export writes the channel secrets to a path of the caller's choosing, and
// an import builds an account whose approved pairs come from any folder.
var peopleOnlyMethods = map[protoreflect.FullName]bool{
	"herdsman.daemon.v1.MessageService.Shutdown":         true,
	"herdsman.daemon.v1.MessageService.Enroll":           true,
	"herdsman.daemon.v1.MessageService.ApprovePair":      true,
	"herdsman.daemon.v1.MessageService.RetirePair":       true,
	"herdsman.daemon.v1.MessageService.RevokeCredential": true,
	"herdsman.daemon.v1.MessageService.ResetEvents":      true,
	"herdsman.daemon.v1.OpsService.Export":               true,
	"herdsman.daemon.v1.OpsService.Import":               true,
	"herdsman.config.v1.ConfigService.Set":               true,
	"herdsman.config.v1.ConfigService.Unset":             true,
}

// Files are the contracts served as tools: only those the daemon serves.
func Files() []protoreflect.FileDescriptor {
	return []protoreflect.FileDescriptor{
		daemonv1.File_herdsman_daemon_v1_events_proto,
		daemonv1.File_herdsman_daemon_v1_message_proto,
		daemonv1.File_herdsman_daemon_v1_route_proto,
		daemonv1.File_herdsman_daemon_v1_ops_proto,
		suggestv1.File_herdsman_suggest_v1_suggest_proto,
		configv1.File_herdsman_config_v1_config_proto,
	}
}

// Annotations are the hints a client shows before calling a tool.
type Annotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

// Tool is one entry of the tool list.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations Annotations    `json:"annotations"`
}

// tool is a Tool with what calling it needs.
type tool struct {
	Tool
	method    protoreflect.MethodDescriptor
	procedure string
	mutating  bool
	// planOnly refuses any call that is not a dry run.
	planOnly bool
	// dryRun and plan are nil unless the method can answer with a plan.
	dryRun protoreflect.FieldDescriptor
	plan   protoreflect.FieldDescriptor
	// planText is set when the plan is a message carrying its text.
	planText protoreflect.FieldDescriptor
}

// snake turns a descriptor name into lower snake case, keeping an initialism as
// one word.
func snake(name string) string {
	runes := []rune(name)
	var out strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			if !unicode.IsUpper(prev) || (i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
				out.WriteByte('_')
			}
		}
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}

// toolName is <package>_<service>_<method>, leaving out the service when it
// repeats the package. Released names never change.
func toolName(method protoreflect.MethodDescriptor) (string, error) {
	service := method.Parent().(protoreflect.ServiceDescriptor)
	parts := strings.Split(string(service.ParentFile().Package()), ".")
	if len(parts) != 3 || parts[0] != "herdsman" {
		return "", fmt.Errorf("mcp: service %s is not in a herdsman.<name>.<version> package", service.FullName())
	}
	words := []string{parts[1]}
	if stem := snake(strings.TrimSuffix(string(service.Name()), "Service")); stem != parts[1] {
		words = append(words, stem)
	}
	name := strings.Join(append(words, snake(string(method.Name()))), "_")
	if !toolNamePattern.MatchString(name) {
		return "", fmt.Errorf("mcp: %s gives the tool name %q, which clients refuse", method.FullName(), name)
	}
	return name, nil
}

func isMutating(method protoreflect.MethodDescriptor) bool {
	options := method.Options().(*descriptorpb.MethodOptions)
	return options.GetIdempotencyLevel() != descriptorpb.MethodOptions_NO_SIDE_EFFECTS && !readOnlyMethods[method.FullName()]
}

// singularMessage holds one message, not a list or a map of them.
func singularMessage(field protoreflect.FieldDescriptor) bool {
	return field.Kind() == protoreflect.MessageKind && !field.IsList() && !field.IsMap()
}

// planFields finds the request's dry_run and where the response carries the
// plan: a core Plan, or a message with its plan as text. Both or neither: a
// method with only one of them cannot answer a dry run.
func planFields(method protoreflect.MethodDescriptor) (dryRun, plan, planText protoreflect.FieldDescriptor) {
	field := method.Input().Fields().ByName(dryRunField)
	if field == nil || field.Kind() != protoreflect.BoolKind || field.IsList() {
		return nil, nil, nil
	}
	planName := (&corev1.Plan{}).ProtoReflect().Descriptor().FullName()
	output := method.Output().Fields()
	for i := range output.Len() {
		candidate := output.Get(i)
		if singularMessage(candidate) && candidate.Message().FullName() == planName {
			return field, candidate, nil
		}
	}
	for i := range output.Len() {
		candidate := output.Get(i)
		if !singularMessage(candidate) {
			continue
		}
		text := candidate.Message().Fields().ByName("plan")
		if text != nil && text.Kind() == protoreflect.StringKind && !text.IsList() {
			return field, candidate, text
		}
	}
	return nil, nil, nil
}

func newTool(method protoreflect.MethodDescriptor) (*tool, error) {
	name, err := toolName(method)
	if err != nil {
		return nil, err
	}
	service := method.Parent().(protoreflect.ServiceDescriptor)
	mutating := isMutating(method)
	t := &tool{
		method:    method,
		procedure: rpc.Path(string(service.FullName()), string(method.Name())),
		mutating:  mutating,
	}
	description := "Calls " + string(service.FullName()) + "/" + string(method.Name()) + "."
	if mutating {
		t.dryRun, t.plan, t.planText = planFields(method)
		t.planOnly = planOnlyMethods[method.FullName()]
		switch {
		case t.planOnly:
			description += " Only plans: dry_run must be true, and the plan is returned with nothing done."
		case t.dryRun != nil:
			description += " Changes state; with dry_run it returns the plan and acts on nothing."
		default:
			description += " Changes state."
		}
	}
	acts := mutating && !t.planOnly
	t.Tool = Tool{
		Name:        name,
		Description: description,
		InputSchema: inputSchema(method.Input(), t.dryRun != nil),
		Annotations: Annotations{ReadOnlyHint: !acts, DestructiveHint: acts},
	}
	return t, nil
}

func buildTools(files []protoreflect.FileDescriptor) ([]*tool, error) {
	var tools []*tool
	seen := map[string]protoreflect.FullName{}
	for _, file := range files {
		services := file.Services()
		for i := range services.Len() {
			methods := services.Get(i).Methods()
			for j := range methods.Len() {
				method := methods.Get(j)
				if method.IsStreamingClient() || method.IsStreamingServer() || peopleOnlyMethods[method.FullName()] {
					continue
				}
				t, err := newTool(method)
				if err != nil {
					return nil, err
				}
				if other, dup := seen[t.Name]; dup {
					return nil, fmt.Errorf("mcp: %s and %s both give the tool name %q", other, method.FullName(), t.Name)
				}
				seen[t.Name] = method.FullName()
				tools = append(tools, t)
			}
		}
	}
	slices.SortFunc(tools, func(a, b *tool) int { return strings.Compare(a.Name, b.Name) })
	return tools, nil
}

// toolResult is the answer to tools/call. StructuredContent is always an object.
type toolResult struct {
	Content           []textContent   `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func success(payload []byte) toolResult {
	return toolResult{Content: []textContent{{Type: "text", Text: string(payload)}}, StructuredContent: payload}
}

func failure(code, message string) toolResult {
	// Two strings always marshal.
	payload, _ := json.Marshal(map[string]string{"code": code, "message": message})
	result := success(payload)
	result.Content[0].Text = code + ": " + message
	result.IsError = true
	return result
}

// checkPolicy refuses what the deny list names, and refuses everything when the
// list cannot be read: a limit that cannot be read cannot be proven safe.
func (s *Server) checkPolicy(t *tool) *toolResult {
	refuse := func(code, message string) *toolResult {
		result := failure(code, message)
		return &result
	}
	if s.options.Deny == nil {
		return refuse(codePolicyUnavailable, "no policy is configured; tool "+t.Name+" refused")
	}
	deny, err := s.options.Deny()
	if err != nil {
		return refuse(codePolicyUnavailable, "policy could not be read; tool "+t.Name+" refused: "+err.Error())
	}
	procedure := strings.TrimPrefix(t.procedure, rpc.BasePath)
	denied := ""
	for _, entry := range deny {
		matched, err := path.Match(entry, t.Name)
		if err != nil {
			return refuse(codePolicyUnavailable, fmt.Sprintf("[policy] deny entry %q is malformed; tool %s refused", entry, t.Name))
		}
		if denied == "" && (matched || entry == procedure) {
			denied = entry
		}
	}
	if denied != "" {
		return refuse(codeDenied, fmt.Sprintf("tool %s is denied by [policy] deny entry %q", t.Name, denied))
	}
	return nil
}

// decoded is a request ready to send.
type decoded struct {
	body []byte
	// dryRun asks the method for its plan.
	dryRun bool
	// unsupported asks for a plan from a method that cannot give one.
	unsupported bool
}

// decodeArguments builds the request. A method that cannot plan loses dry_run
// here, so the strict decoding below does not call it unknown.
func decodeArguments(t *tool, arguments json.RawMessage) (decoded, error) {
	var top map[string]json.RawMessage
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &top); err != nil {
			return decoded{}, errors.New("arguments must be an object")
		}
	}
	var out decoded
	if raw, ok := top[dryRunField]; ok && t.mutating && t.dryRun == nil {
		if err := json.Unmarshal(raw, &out.unsupported); err != nil {
			return decoded{}, errors.New("dry_run must be a boolean")
		}
		delete(top, dryRunField)
	}
	// Marshaling what was just unmarshaled cannot fail.
	body, _ := json.Marshal(top)
	if top == nil {
		body = []byte("{}")
	}
	request := dynamicpb.NewMessage(t.method.Input())
	if err := protojson.Unmarshal(body, request); err != nil {
		return decoded{}, err
	}
	out.dryRun = t.dryRun != nil && request.Get(t.dryRun).Bool()
	var err error
	out.body, err = proto.MarshalOptions{Deterministic: true}.Marshal(request)
	return out, err
}

func marshalJSON(m proto.Message) ([]byte, error) {
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return nil, err
	}
	// protojson varies its spacing on purpose; clients get one form.
	var compact bytes.Buffer
	err = json.Compact(&compact, data)
	return compact.Bytes(), err
}

// callTool runs one tool: the policy, then the arguments, then the daemon.
func (s *Server) callTool(ctx context.Context, t *tool, arguments json.RawMessage) toolResult {
	if refused := s.checkPolicy(t); refused != nil {
		return *refused
	}
	request, err := decodeArguments(t, arguments)
	if err != nil {
		return failure(codeInvalidArgument, err.Error())
	}
	if request.unsupported {
		// The daemon would ignore the unknown field and act.
		return failure(codeDryRunUnsupported, "tool "+t.Name+" cannot plan; nothing was done")
	}
	if t.planOnly && !request.dryRun {
		return failure(codePlanOnly, "tool "+t.Name+" only plans: call it with dry_run true; a person starts a route from a terminal")
	}
	answer, err := s.options.Call(ctx, t.procedure, request.body)
	if err != nil {
		failed := rpc.FromError(err)
		return failure(string(failed.Code), failed.Message)
	}
	response := dynamicpb.NewMessage(t.method.Output())
	if err := proto.Unmarshal(answer, response); err != nil {
		return failure(codeBadReply, "the daemon's answer could not be read: "+err.Error())
	}
	return encodeResponse(t, response, request.dryRun)
}

func encodeResponse(t *tool, response *dynamicpb.Message, dryRun bool) toolResult {
	var body proto.Message = response
	if dryRun {
		if !response.Has(t.plan) || (t.planText != nil && !response.Get(t.plan).Message().Has(t.planText)) {
			return failure(codePlanMissing, "the daemon answered a dry run of "+t.Name+" without a plan")
		}
		body = response.Get(t.plan).Message().Interface()
	}
	payload, err := marshalJSON(body)
	if err != nil {
		return failure(codeBadReply, "the daemon's answer could not be written: "+err.Error())
	}
	if dryRun {
		payload = append(append([]byte(`{"plan":`), payload...), '}')
	}
	return success(payload)
}
