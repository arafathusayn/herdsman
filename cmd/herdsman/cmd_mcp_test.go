package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arafathusayn/herdsman/internal/mcp"
	"github.com/arafathusayn/herdsman/sdk/config"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// mcpLines is a client's whole conversation, one call to the status tool.
const mcpLines = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"daemon_message_status","arguments":{}}}
`

// serveSocket answers on a unix socket and returns its path. The socket lives
// under a short directory, because a socket path has a small length limit.
func serveSocket(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: h}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return socket
}

// runUntilReply feeds the lines with the input held open until the tool has
// answered: end of input cancels whatever is still running.
func runUntilReply(t *testing.T, lines string, args ...string) (string, string, int) {
	t.Helper()
	isolateConfig(t)
	return runInPlace(t, lines, args...)
}

// isolateConfig keeps this machine's own configuration out of a test.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HERDSMAN_CONFIG", "")
	t.Chdir(t.TempDir())
}

// runInPlace is runUntilReply for a test that arranged its own configuration.
func runInPlace(t *testing.T, lines string, args ...string) (string, string, int) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	out := &closeOnReply{want: `"id":2`, close: func() { write.Close() }}
	if _, err := write.WriteString(lines); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	code := runMCPCommand(args, read, out, &errOut)
	return out.String(), errOut.String(), code
}

// closeOnReply ends the input once the awaited reply has been written.
type closeOnReply struct {
	bytes.Buffer
	want  string
	close func()
}

func (c *closeOnReply) Write(p []byte) (int, error) {
	n, err := c.Buffer.Write(p)
	if strings.Contains(string(p), c.want) {
		c.close()
	}
	return n, err
}

func toolAnswer(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var reply struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &reply); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	return reply.Result
}

func TestMCPCallsTheDaemonOverItsSocket(t *testing.T) {
	var path, contentType, protocol string
	socket := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, contentType, protocol = r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get(rpc.HeaderProtocolVersion)
		w.Header().Set("Content-Type", rpc.ContentTypeProto)
	}))
	out, errOut, code := runUntilReply(t, mcpLines, "--socket", socket)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if answer := toolAnswer(t, out); answer["isError"] == true {
		t.Fatalf("%+v", answer)
	}
	if want := rpc.Path("herdsman.daemon.v1.MessageService", "Status"); path != want {
		t.Fatalf("path %s, want %s", path, want)
	}
	if contentType != rpc.ContentTypeProto || protocol != rpc.ProtocolVersion {
		t.Fatalf("content type %q, protocol %q", contentType, protocol)
	}
}

// The daemon knows a party by these headers, as it does for `herdsman message`.
func TestMCPSendsThePartysIdentity(t *testing.T) {
	var got http.Header
	socket := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", rpc.ContentTypeProto)
	}))
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	t.Setenv("HERDSMAN_NAME", "orchestrator")
	if _, errOut, code := runUntilReply(t, mcpLines, "--socket", socket); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got.Get(mcp.HeaderPane) != "w1:p2" || got.Get(mcp.HeaderName) != "orchestrator" || got.Get(mcp.HeaderVersion) != daemonVersion() {
		t.Fatalf("headers %v", got)
	}
}

func TestMCPOutsideAPaneSendsNoIdentity(t *testing.T) {
	var got http.Header
	socket := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", rpc.ContentTypeProto)
	}))
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HERDSMAN_NAME", "")
	if _, errOut, code := runUntilReply(t, mcpLines, "--socket", socket); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got.Values(mcp.HeaderPane) != nil || got.Values(mcp.HeaderName) != nil {
		t.Fatalf("headers %v", got)
	}
}

func TestMCPReportsTheDaemonsFailure(t *testing.T) {
	socket := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":"invalid_argument","message":"no route"}`))
	}))
	out, _, code := runUntilReply(t, mcpLines, "--socket", socket)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	answer := toolAnswer(t, out)
	if answer["isError"] != true || !strings.Contains(out, "no route") {
		t.Fatalf("%s", out)
	}
}

func TestMCPDenyFlagRefusesTheTool(t *testing.T) {
	out, _, code := runUntilReply(t, mcpLines, "--socket", "/nonexistent", "--deny", "daemon_message_*")
	if code != 0 || !strings.Contains(out, "policy_denied") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestMCPDaemonDownIsAToolError(t *testing.T) {
	out, _, code := runUntilReply(t, mcpLines, "--socket", filepath.Join(t.TempDir(), "absent"))
	if code != 0 || toolAnswer(t, out)["isError"] != true {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestMCPFindsTheSocketFromTheEnvironment(t *testing.T) {
	t.Setenv("HERDSMAN_PRIVATE_HOME", t.TempDir())
	out, _, code := runUntilReply(t, mcpLines)
	if code != 0 || !strings.Contains(out, "daemon.sock") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestMCPNoHomeIsAFailure(t *testing.T) {
	t.Setenv("HERDSMAN_PRIVATE_HOME", "")
	t.Setenv("HOME", "")
	var errOut bytes.Buffer
	if code := runMCPCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &errOut); code != 1 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

func TestMCPBadUsage(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"--no-such-flag"}} {
		var errOut bytes.Buffer
		if code := runMCPCommand(args, strings.NewReader(""), &bytes.Buffer{}, &errOut); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func TestMCPClosedOutputIsAFailure(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	read.Close()
	defer write.Close()
	var errOut bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	if code := runMCPCommand([]string{"--socket", "/nonexistent"}, in, write, &errOut); code != 1 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

func TestMCPBadRequestPathIsAnError(t *testing.T) {
	if _, err := mcpCaller("/nonexistent")(context.Background(), "\x7f", nil); err == nil {
		t.Fatal("a procedure that is no URL was accepted")
	}
}

func TestMCPBadDescriptorsStopTheServer(t *testing.T) {
	var fdp descriptorpb.FileDescriptorProto
	text := `name: "a.proto" package: "other.v1" syntax: "proto3" message_type: { name: "R" }
service: { name: "XService" method: { name: "A" input_type: ".other.v1.R" output_type: ".other.v1.R" } }`
	if err := prototext.Unmarshal([]byte(text), &fdp); err != nil {
		t.Fatal(err)
	}
	file, err := protodesc.NewFile(&fdp, nil)
	if err != nil {
		t.Fatal(err)
	}
	options := mcp.Options{Call: mcpCaller("/nonexistent")}
	if err := serveMCP([]protoreflect.FileDescriptor{file}, options, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("serveMCP accepted a service outside the herdsman packages")
	}
}

// configFile writes a configuration and returns its path.
func configFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "herdsman.config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMCPAppliesTheConfiguredDeny(t *testing.T) {
	for name, entry := range map[string]string{
		"tool name": "daemon_message_status",
		"pattern":   "daemon_*",
		"procedure": "herdsman.daemon.v1.MessageService/Status",
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			socket := serveSocket(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			path := configFile(t, "[policy]\ndeny = [\""+entry+"\"]\n")
			out, _, code := runUntilReply(t, mcpLines, "--socket", socket, "--config", path)
			if code != 0 || !strings.Contains(out, "policy_denied") {
				t.Fatalf("exit %d: %s", code, out)
			}
			if calls.Load() != 0 {
				t.Fatal("a denied tool reached the daemon")
			}
		})
	}
}

func TestMCPFindsTheProjectsConfiguration(t *testing.T) {
	isolateConfig(t)
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "herdsman.config.toml"), []byte("[policy]\ndeny = [\"daemon_*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code := runInPlace(t, mcpLines, "--socket", "/nonexistent")
	if code != 0 || !strings.Contains(out, "policy_denied") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// A project file may add to the user's deny list but never take from it.
func TestMCPProjectCannotLiftTheUsersDeny(t *testing.T) {
	isolateConfig(t)
	user := config.UserDir(os.Environ())
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(user, "config.toml"), []byte("[policy]\ndeny = [\"daemon_*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := configFile(t, "[policy]\ndeny = [\"suggest_*\"]\n")
	out, _, code := runInPlace(t, mcpLines, "--socket", "/nonexistent", "--config", path)
	if code != 0 || !strings.Contains(out, "policy_denied") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestMCPFlagsAddToTheConfiguredDeny(t *testing.T) {
	path := configFile(t, "[policy]\ndeny = [\"suggest_*\"]\n")
	out, _, code := runUntilReply(t, mcpLines, "--socket", "/nonexistent", "--config", path, "--deny", "daemon_*")
	if code != 0 || !strings.Contains(out, "policy_denied") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestMCPUnreadableConfigurationRefusesTools(t *testing.T) {
	for name, text := range map[string]string{
		"not toml":        "[policy\n",
		"misspelled key":  "[policy]\ndenny = [\"x\"]\n",
		"malformed entry": "[policy]\ndeny = [\"daemon[\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, _, code := runUntilReply(t, mcpLines, "--socket", "/nonexistent", "--config", configFile(t, text))
			if code != 0 || !strings.Contains(out, "policy_unavailable") {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

// The configuration is read for each call, so a limit added while the server
// runs holds at once, and one that stops being readable refuses.
func TestMCPConfigurationIsReadOnEveryCall(t *testing.T) {
	isolateConfig(t)
	path := configFile(t, "[policy]\ndeny = [\"suggest_*\"]\n")
	socket := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", rpc.ContentTypeProto)
	}))
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	call := func(id int) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"daemon_message_status"}}`+"\n", id)
	}
	steps := map[string]func(){
		`"id":2`: func() {
			os.WriteFile(path, []byte("[policy]\ndeny = [\"daemon_*\"]\n"), 0o600)
			write.WriteString(call(3))
		},
		`"id":3`: func() {
			os.Remove(path)
			write.WriteString(call(4))
		},
		`"id":4`: func() { write.Close() },
	}
	out := &stepWriter{steps: steps}
	if _, err := write.WriteString(mcpLines); err != nil {
		t.Fatal(err)
	}
	if code := runMCPCommand([]string{"--socket", socket, "--config", path}, read, out, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines %q", lines)
	}
	if strings.Contains(lines[1], "isError") {
		t.Fatalf("the first call was allowed by the file: %s", lines[1])
	}
	if !strings.Contains(lines[2], "policy_denied") || !strings.Contains(lines[3], "policy_unavailable") {
		t.Fatalf("after the edits: %s / %s", lines[2], lines[3])
	}
}

// stepWriter runs one step after the reply it waits for has been kept.
type stepWriter struct {
	bytes.Buffer
	steps map[string]func()
}

func (w *stepWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	for want, step := range w.steps {
		if strings.Contains(string(p), want) {
			step()
		}
	}
	return n, err
}

func TestMCPHelp(t *testing.T) {
	out, _, code := call(t, "mcp", "--help")
	if code != 0 || !strings.Contains(out, "usage: herdsman mcp") {
		t.Fatalf("exit %d: %s", code, out)
	}
}
