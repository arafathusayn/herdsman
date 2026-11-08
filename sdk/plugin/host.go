package plugin

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

const (
	defaultTimeout = 10 * time.Second
	// maxHandshake bounds the first line: a path and a checksum fit easily.
	maxHandshake = 8 << 10
	// maxStderr bounds what is kept of a plugin's complaint.
	maxStderr = 4 << 10
	stopWait  = 2 * time.Second
)

// inheritedEnv is all a plugin receives from the host's environment, so that
// nothing leading to the daemon's socket reaches it.
var inheritedEnv = []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "USER"}

// HandshakeError says why a plugin could not be started. The caller reports it
// by name and goes on without the plugin.
type HandshakeError struct {
	Path   string
	Reason string
}

func (e *HandshakeError) Error() string { return e.Path + ": " + e.Reason }

// Host starts plugin processes. Its zero value is ready to use.
type Host struct {
	// Name, when set, is the name the manifest must carry.
	Name string
	// Env is added to the small environment a plugin inherits.
	Env []string
	// Timeout bounds the handshake; zero means ten seconds.
	Timeout time.Duration
}

// Clients holds one client per service of a plugin. A client of an extension
// point the plugin did not declare answers with an error.
type Clients struct {
	Manifest         pluginv1.ManifestService
	ConfigCodec      pluginv1.ConfigCodecService
	ResourceDetector pluginv1.ResourceDetectorService
	LandingBackend   pluginv1.LandingBackendService
	MessageGenerator pluginv1.MessageGeneratorService
	ListColumn       pluginv1.ListColumnService
}

func newClients(socket string) Clients {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		DisableCompression: true,
	}
	hc := &http.Client{Transport: &rpc.Transport{Base: transport}}
	const base = "http://plugin"
	return Clients{
		Manifest:         pluginv1.NewManifestServiceClient(hc, base),
		ConfigCodec:      pluginv1.NewConfigCodecServiceClient(hc, base),
		ResourceDetector: pluginv1.NewResourceDetectorServiceClient(hc, base),
		LandingBackend:   pluginv1.NewLandingBackendServiceClient(hc, base),
		MessageGenerator: pluginv1.NewMessageGeneratorServiceClient(hc, base),
		ListColumn:       pluginv1.NewListColumnServiceClient(hc, base),
	}
}

// Process is one running plugin.
type Process struct {
	Clients
	Manifest *pluginv1.Manifest
	Socket   string

	cmd         *exec.Cmd
	stdin       io.Closer
	done        chan struct{}
	lineRead    chan struct{}
	releaseWait func()
}

// cappedBuffer keeps the start of what is written to it.
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := maxStderr - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.buf.String())
}

// Environment is what a plugin started by this host receives.
func (h Host) Environment() []string {
	var env []string
	for _, name := range inheritedEnv {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return append(env, h.Env...)
}

// Start runs the executable and completes the handshake. Every failure is a
// *HandshakeError and leaves no process behind.
func (h Host) Start(ctx context.Context, path string) (*Process, error) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cmd := exec.Command(path)
	cmd.Env = h.Environment()
	var stderr cappedBuffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &HandshakeError{path, "start: " + err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &HandshakeError{path, "start: " + err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return nil, &HandshakeError{path, "start: " + err.Error()}
	}
	p := &Process{cmd: cmd, stdin: stdin, done: make(chan struct{}), lineRead: make(chan struct{})}
	p.releaseWait = sync.OnceFunc(func() { close(p.lineRead) })
	go func() {
		defer close(p.done)
		// Wait closes stdout, so it must not start before the handshake line was
		// read, or before the host gives up on one.
		<-p.lineRead
		_ = cmd.Wait()
	}()

	fail := func(reason string) (*Process, error) {
		p.kill()
		if said := stderr.String(); said != "" {
			reason += "; stderr: " + said
		}
		return nil, &HandshakeError{path, reason}
	}

	lines := make(chan readResult, 1)
	go readHandshake(stdout, lines, p)
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var line readResult
	select {
	case line = <-lines:
	case <-waitCtx.Done():
		return fail("no handshake within " + timeout.String())
	}
	if line.err != nil {
		return fail("no handshake line: " + line.err.Error())
	}
	handshake, err := ParseHandshake(line.text)
	if err != nil {
		return fail(err.Error())
	}
	if handshake.Major != ProtocolMajor {
		return fail(fmt.Sprintf("speaks protocol major %d; this host speaks %d", handshake.Major, ProtocolMajor))
	}
	clients := newClients(handshake.Socket)
	response, err := clients.Manifest.GetManifest(waitCtx, &pluginv1.GetManifestRequest{})
	if err != nil {
		return fail("read the manifest: " + err.Error())
	}
	manifest := response.GetManifest()
	checksum, err := ManifestChecksum(manifest)
	if err != nil {
		return fail("read the manifest: " + err.Error())
	}
	if checksum != handshake.Checksum {
		return fail("the manifest does not match the handshake's checksum")
	}
	if manifest.GetProtocolMajor() != ProtocolMajor {
		return fail(fmt.Sprintf("the manifest says protocol major %d; this host speaks %d", manifest.GetProtocolMajor(), ProtocolMajor))
	}
	if h.Name != "" && manifest.GetName() != h.Name {
		return fail(fmt.Sprintf("the manifest names %q, expected %q", manifest.GetName(), h.Name))
	}
	p.Clients, p.Manifest, p.Socket = clients, manifest, handshake.Socket
	return p, nil
}

type readResult struct {
	text string
	err  error
}

// readHandshake delivers the first line, then keeps stdout drained so that a
// chatty plugin never blocks on a full pipe.
func readHandshake(stdout io.Reader, lines chan<- readResult, p *Process) {
	reader := bufio.NewReaderSize(stdout, maxHandshake)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			err = errors.New("the first line is too long")
		}
		lines <- readResult{err: err}
	} else {
		lines <- readResult{text: strings.TrimRight(string(line), "\r\n")}
	}
	p.releaseWait()
	_, _ = io.Copy(io.Discard, reader)
}

// kill does not wait for stdout: a child of the plugin may hold it open.
func (p *Process) kill() {
	_ = p.stdin.Close()
	p.releaseWait()
	_ = p.cmd.Process.Kill()
	<-p.done
}

// Close asks the plugin to stop by closing its stdin, and kills it if it does
// not. It can be called more than once.
func (p *Process) Close() error {
	_ = p.stdin.Close()
	select {
	case <-p.done:
	case <-time.After(stopWait):
		p.kill()
	}
	return nil
}
