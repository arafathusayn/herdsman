package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// helperEnv selects the behaviour of this test binary when a test starts it as
// a plugin.
const helperEnv = "HERDSMAN_PLUGIN_TEST_HELPER"

type testCodec struct{}

func (testCodec) Decode(_ context.Context, r *pluginv1.DecodeRequest) (*pluginv1.DecodeResponse, error) {
	return &pluginv1.DecodeResponse{Json: r.GetSource()}, nil
}
func (testCodec) Encode(_ context.Context, r *pluginv1.EncodeRequest) (*pluginv1.EncodeResponse, error) {
	return &pluginv1.EncodeResponse{Source: r.GetJson()}, nil
}
func (testCodec) Set(context.Context, *pluginv1.SetRequest) (*pluginv1.SetResponse, error) {
	return nil, rpc.Errorf(rpc.Unimplemented, "no set")
}

// blockingCodec answers Decode only when its caller gives up.
type blockingCodec struct{ testCodec }

func (blockingCodec) Decode(ctx context.Context, _ *pluginv1.DecodeRequest) (*pluginv1.DecodeResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type testDetector struct{}

func (testDetector) Detect(context.Context, *pluginv1.DetectRequest) (*pluginv1.DetectResponse, error) {
	return &pluginv1.DetectResponse{}, nil
}

type testBackend struct{}

func (testBackend) Open(context.Context, *pluginv1.OpenRequest) (*pluginv1.OpenResponse, error) {
	return &pluginv1.OpenResponse{}, nil
}
func (testBackend) Merge(context.Context, *pluginv1.MergeRequest) (*pluginv1.MergeResponse, error) {
	return &pluginv1.MergeResponse{}, nil
}
func (testBackend) Status(context.Context, *pluginv1.StatusRequest) (*pluginv1.StatusResponse, error) {
	return &pluginv1.StatusResponse{}, nil
}

type testGenerator struct{}

func (testGenerator) Generate(context.Context, *pluginv1.GenerateRequest) (*pluginv1.GenerateResponse, error) {
	return &pluginv1.GenerateResponse{}, nil
}

type testColumn struct{}

func (testColumn) Compute(context.Context, *pluginv1.ComputeRequest) (*pluginv1.ComputeResponse, error) {
	return &pluginv1.ComputeResponse{}, nil
}

func testManifest(points ...pluginv1.ExtensionPoint) *pluginv1.Manifest {
	return &pluginv1.Manifest{Name: "sample", Version: "1.0.0", ExtensionPoints: points}
}

func codecManifest() *pluginv1.Manifest {
	return testManifest(pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC)
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		runHelper(mode)
		return
	}
	os.Exit(m.Run())
}

func runHelper(mode string) {
	checksum, _ := ManifestChecksum(withMajor(codecManifest()))
	switch mode {
	case "serve":
		if err := Serve(context.Background(), codecManifest(), Extensions{ConfigCodec: testCodec{}}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "env":
		// The variable's value is reported through the manifest's description.
		m := codecManifest()
		m.Description = "private=" + os.Getenv("HERDSMAN_PRIVATE_HOME") + " extra=" + os.Getenv("PLUGIN_EXTRA")
		if err := Serve(context.Background(), m, Extensions{ConfigCodec: testCodec{}}); err != nil {
			os.Exit(1)
		}
	case "garbage":
		fmt.Println("hello there")
		io.Copy(io.Discard, os.Stdin)
	case "complain":
		fmt.Fprintln(os.Stderr, "the plugin could not start")
		os.Exit(3)
	case "hang":
		io.Copy(io.Discard, os.Stdin)
	case "major":
		fmt.Printf("herdsman-plugin/2 /tmp/x.sock %s\n", checksum)
		io.Copy(io.Discard, os.Stdin)
	case "nosocket":
		fmt.Printf("herdsman-plugin/1 %s %s\n", filepath.Join(os.TempDir(), "absent-plugin.sock"), checksum)
		io.Copy(io.Discard, os.Stdin)
	case "descendant":
		spawnDescendant()
		fmt.Println("not a handshake")
		io.Copy(io.Discard, os.Stdin)
	case "servedescendant":
		spawnDescendant()
		if err := Serve(context.Background(), codecManifest(), Extensions{ConfigCodec: testCodec{}}); err != nil {
			os.Exit(1)
		}
	case "blocked":
		if err := Serve(context.Background(), codecManifest(), Extensions{ConfigCodec: blockingCodec{}}); err != nil {
			os.Exit(1)
		}
	case "longline":
		fmt.Println(strings.Repeat("x", 3*maxHandshake))
		io.Copy(io.Discard, os.Stdin)
	case "stubborn":
		serveStubborn()
	case "wrongmajor":
		serveWrongMajor()
	case "checksum":
		dir, _ := os.MkdirTemp("", "hp")
		defer os.RemoveAll(dir)
		serveWithLine(dir, strings.Repeat("0", 64))
	}
}

// serveWithLine serves a correct manifest behind a handshake line whose
// checksum is the given one.
func serveWithLine(dir, checksum string) {
	r, w := io.Pipe()
	go func() {
		_ = serve(context.Background(), codecManifest(), Extensions{ConfigCodec: testCodec{}}, os.Stdin, w)
	}()
	line := make([]byte, 512)
	n, _ := r.Read(line)
	parsed, err := ParseHandshake(strings.TrimSpace(string(line[:n])))
	if err != nil {
		os.Exit(9)
	}
	fmt.Println(FormatHandshake(parsed.Socket, checksum))
	go io.Copy(io.Discard, r)
	io.Copy(io.Discard, os.Stdin)
	_ = dir
}

// spawnDescendant leaves a child holding this process's stderr and records its
// pid.
func spawnDescendant() {
	child := exec.Command("sleep", "12")
	child.Stderr = os.Stderr
	if child.Start() != nil {
		os.Exit(8)
	}
	if os.WriteFile(os.Getenv("PLUGIN_PIDFILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600) != nil {
		os.Exit(8)
	}
}

// serveStubborn completes the handshake and then ignores its stdin closing.
func serveStubborn() {
	r, w := io.Pipe()
	go func() {
		_ = serve(context.Background(), codecManifest(), Extensions{ConfigCodec: testCodec{}}, blockingReader{}, w)
	}()
	line := make([]byte, 512)
	n, _ := r.Read(line)
	os.Stdout.Write(line[:n])
	select {}
}

// blockingReader never ends.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) { select {} }

// serveWrongMajor answers with a manifest of another major, behind a handshake
// line that is otherwise correct.
func serveWrongMajor() {
	dir, _ := os.MkdirTemp("", "hp")
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(9)
	}
	manifest := codecManifest()
	manifest.ProtocolMajor = 7
	checksum, _ := ManifestChecksum(manifest)
	go http.Serve(listener, rpc.NewMux(rpc.Unary(pluginv1.ManifestServiceName, "GetManifest", manifestServer{manifest}.GetManifest)))
	fmt.Println(FormatHandshake(socket, checksum))
	io.Copy(io.Discard, os.Stdin)
}

func startHelper(t *testing.T, mode string, host Host) (*Process, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host.Env = append(host.Env, helperEnv+"="+mode)
	if host.Timeout == 0 {
		host.Timeout = 10 * time.Second
	}
	p, err := host.Start(context.Background(), exe)
	if p != nil {
		t.Cleanup(func() { _ = p.Close() })
	}
	return p, err
}

func TestHandshakeLineRoundTrips(t *testing.T) {
	sum := strings.Repeat("a", 64)
	got, err := ParseHandshake(FormatHandshake("/tmp/p/s", sum))
	if err != nil {
		t.Fatal(err)
	}
	if got.Major != ProtocolMajor || got.Socket != "/tmp/p/s" || got.Checksum != sum {
		t.Fatalf("%+v", got)
	}
}

func TestParseHandshakeRefusesMalformedLines(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for name, line := range map[string]string{
		"empty":          "",
		"fields":         "herdsman-plugin/1 /tmp/s",
		"too many":       "herdsman-plugin/1 /tmp/s " + sum + " x",
		"prefix":         "other/1 /tmp/s " + sum,
		"major":          "herdsman-plugin/x /tmp/s " + sum,
		"negative":       "herdsman-plugin/-1 /tmp/s " + sum,
		"relative":       "herdsman-plugin/1 tmp/s " + sum,
		"short checksum": "herdsman-plugin/1 /tmp/s abc",
		"not hex":        "herdsman-plugin/1 /tmp/s " + strings.Repeat("z", 64),
	} {
		if _, err := ParseHandshake(line); err == nil {
			t.Errorf("%s: accepted %q", name, line)
		}
	}
}

func TestManifestChecksumFollowsContent(t *testing.T) {
	a, err := ManifestChecksum(codecManifest())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ManifestChecksum(codecManifest())
	changed := codecManifest()
	changed.Version = "2.0.0"
	c, _ := ManifestChecksum(changed)
	if a != b || a == c || len(a) != 64 {
		t.Fatalf("%s %s %s", a, b, c)
	}
}

func TestHostStartsAPluginAndCallsIt(t *testing.T) {
	p, err := startHelper(t, "serve", Host{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.GetName() != "sample" || p.Manifest.GetProtocolMajor() != ProtocolMajor {
		t.Fatalf("%v", p.Manifest)
	}
	res, err := p.ConfigCodec.Decode(context.Background(), &pluginv1.DecodeRequest{Source: []byte("{}")})
	if err != nil || string(res.GetJson()) != "{}" {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := p.ConfigCodec.Set(context.Background(), &pluginv1.SetRequest{}); !isCode(err, rpc.Unimplemented) {
		t.Fatalf("Set: %v", err)
	}
}

func isCode(err error, code rpc.Code) bool {
	e, ok := errors.AsType[*rpc.Error](err)
	return ok && e.Code == code
}

func TestHostChecksTheExpectedName(t *testing.T) {
	_, err := startHelper(t, "serve", Host{Name: "other"})
	wantHandshakeError(t, err, "other")
}

func wantHandshakeError(t *testing.T, err error, text string) {
	t.Helper()
	failure, ok := errors.AsType[*HandshakeError](err)
	if !ok {
		t.Fatalf("got %v, want a handshake error", err)
	}
	if !strings.Contains(failure.Error(), text) {
		t.Fatalf("%q lacks %q", failure.Error(), text)
	}
}

func TestHostReportsEachFailedHandshake(t *testing.T) {
	for mode, text := range map[string]string{
		"garbage":    "handshake line",
		"complain":   "the plugin could not start",
		"major":      "protocol major 2",
		"nosocket":   "manifest",
		"checksum":   "checksum",
		"longline":   "too long",
		"wrongmajor": "protocol major 7",
	} {
		t.Run(mode, func(t *testing.T) {
			_, err := startHelper(t, mode, Host{})
			wantHandshakeError(t, err, text)
		})
	}
}

func TestHostGivesUpOnASilentPlugin(t *testing.T) {
	start := time.Now()
	_, err := startHelper(t, "hang", Host{Timeout: 200 * time.Millisecond})
	wantHandshakeError(t, err, "no handshake")
	if time.Since(start) > 5*time.Second {
		t.Fatal("the host waited too long")
	}
}

func TestHostReportsAnExecutableThatDoesNotStart(t *testing.T) {
	_, err := Host{}.Start(context.Background(), filepath.Join(t.TempDir(), "absent"))
	wantHandshakeError(t, err, "start")
}

func TestPluginSeesOnlyTheHostsEnvironment(t *testing.T) {
	t.Setenv("HERDSMAN_PRIVATE_HOME", "/secret")
	p, err := startHelper(t, "env", Host{Env: []string{"PLUGIN_EXTRA=given"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Manifest.GetDescription(); got != "private= extra=given" {
		t.Fatalf("%q", got)
	}
}

func TestClosingEndsThePluginAndItsSocket(t *testing.T) {
	p, err := startHelper(t, "serve", Host{})
	if err != nil {
		t.Fatal(err)
	}
	socket := p.Socket
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestServeRefusesAnInconsistentPlugin(t *testing.T) {
	codec := pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC
	for name, c := range map[string]struct {
		manifest *pluginv1.Manifest
		ext      Extensions
	}{
		"nil":          {nil, Extensions{ConfigCodec: testCodec{}}},
		"name":         {&pluginv1.Manifest{Name: "Bad Name", Version: "1", ExtensionPoints: []pluginv1.ExtensionPoint{codec}}, Extensions{ConfigCodec: testCodec{}}},
		"version":      {&pluginv1.Manifest{Name: "a", ExtensionPoints: []pluginv1.ExtensionPoint{codec}}, Extensions{ConfigCodec: testCodec{}}},
		"major":        {&pluginv1.Manifest{Name: "a", Version: "1", ProtocolMajor: 9, ExtensionPoints: []pluginv1.ExtensionPoint{codec}}, Extensions{ConfigCodec: testCodec{}}},
		"none":         {testManifest(), Extensions{}},
		"unspecified":  {testManifest(pluginv1.ExtensionPoint_EXTENSION_POINT_UNSPECIFIED), Extensions{}},
		"repeated":     {testManifest(codec, codec), Extensions{ConfigCodec: testCodec{}}},
		"missing impl": {testManifest(codec), Extensions{}},
		"extra impl":   {testManifest(codec), Extensions{ConfigCodec: testCodec{}, ListColumn: testColumn{}}},
	} {
		if err := serve(context.Background(), c.manifest, c.ext, strings.NewReader(""), io.Discard); err == nil {
			t.Errorf("%s: served", name)
		}
	}
}

func TestServeMountsEveryExtensionPoint(t *testing.T) {
	m := testManifest(
		pluginv1.ExtensionPoint_EXTENSION_POINT_CONFIG_CODEC,
		pluginv1.ExtensionPoint_EXTENSION_POINT_RESOURCE_DETECTOR,
		pluginv1.ExtensionPoint_EXTENSION_POINT_LANDING_BACKEND,
		pluginv1.ExtensionPoint_EXTENSION_POINT_MESSAGE_GENERATOR,
		pluginv1.ExtensionPoint_EXTENSION_POINT_LIST_COLUMN,
	)
	ext := Extensions{testCodec{}, testDetector{}, testBackend{}, testGenerator{}, testColumn{}}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), m, ext, inR, outW) }()
	line := make([]byte, 512)
	n, err := outR.Read(line)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHandshake(strings.TrimSpace(string(line[:n])))
	if err != nil {
		t.Fatal(err)
	}
	clients := newClients(parsed.Socket, time.Minute)
	ctx := context.Background()
	got, err := clients.Manifest.GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil || got.GetManifest().GetName() != "sample" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := clients.ResourceDetector.Detect(ctx, &pluginv1.DetectRequest{}); err != nil {
		t.Error(err)
	}
	if _, err := clients.LandingBackend.Status(ctx, &pluginv1.StatusRequest{}); err != nil {
		t.Error(err)
	}
	if _, err := clients.MessageGenerator.Generate(ctx, &pluginv1.GenerateRequest{}); err != nil {
		t.Error(err)
	}
	if _, err := clients.ListColumn.Compute(ctx, &pluginv1.ComputeRequest{}); err != nil {
		t.Error(err)
	}
	inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	defer inW.Close()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, codecManifest(), Extensions{ConfigCodec: testCodec{}}, inR, outW)
	}()
	if _, err := outR.Read(make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestCloseKillsAPluginThatIgnoresItsStdin(t *testing.T) {
	p, err := startHelper(t, "stubborn", Host{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < stopWait || elapsed > stopWait+5*time.Second {
		t.Fatalf("closed after %s", elapsed)
	}
}

func TestServeExportedRefusesABadManifest(t *testing.T) {
	if err := Serve(context.Background(), nil, Extensions{}); err == nil {
		t.Fatal("served without a manifest")
	}
}

func TestManifestChecksumReportsAnUnencodableManifest(t *testing.T) {
	if _, err := ManifestChecksum(&pluginv1.Manifest{Name: "\xff"}); err == nil {
		t.Fatal("invalid UTF-8 was encoded")
	}
}

func TestServeReportsItsSetupFailures(t *testing.T) {
	serveOnce := func(out io.Writer) error {
		return serve(context.Background(), codecManifest(), Extensions{ConfigCodec: testCodec{}}, strings.NewReader(""), out)
	}
	t.Run("no temporary folder", func(t *testing.T) {
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))
		if err := serveOnce(io.Discard); err == nil {
			t.Fatal("served")
		}
	})
	t.Run("socket path too long", func(t *testing.T) {
		long := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
		if err := os.MkdirAll(long, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", long)
		if err := serveOnce(io.Discard); err == nil {
			t.Fatal("served")
		}
	})
	t.Run("stdout closed", func(t *testing.T) {
		if err := serveOnce(failingWriter{}); err == nil {
			t.Fatal("served")
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

// wantDescendantGone waits for the recorded process to end.
func wantDescendantGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("descendant %d outlived the plugin", pid)
}

func TestAFailedHandshakeIsNotHeldUpByADescendant(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	_, err := startHelper(t, "descendant", Host{Env: []string{"PLUGIN_PIDFILE=" + pidFile}})
	wantHandshakeError(t, err, "handshake line")
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("the host waited %s for a descendant", elapsed)
	}
	wantDescendantGone(t, pidFile)
}

func TestCloseIsNotHeldUpByADescendant(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	p, err := startHelper(t, "servedescendant", Host{Env: []string{"PLUGIN_PIDFILE=" + pidFile}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("Close waited %s for a descendant", elapsed)
	}
	wantDescendantGone(t, pidFile)
}

func TestACallToASilentPluginEnds(t *testing.T) {
	p, err := startHelper(t, "blocked", Host{CallTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := p.ConfigCodec.Decode(context.Background(), &pluginv1.DecodeRequest{}); err == nil {
		t.Fatal("no error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the call took %s", elapsed)
	}
}

func TestAfterCloseRunsOnce(t *testing.T) {
	p, err := startHelper(t, "serve", Host{})
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	p.AfterClose = func() { runs++ }
	p.Close()
	p.Close()
	if runs != 1 {
		t.Fatalf("%d runs", runs)
	}
}
