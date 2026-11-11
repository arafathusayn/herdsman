// Package hclive starts private herdr-core servers for opt-in live tests: the
// installed hc in a session of its own, with every XDG directory under a
// temporary folder, stopped when the test ends. The user's own core, its
// session and its folders are never touched, and no herdr app is started.
//
// Live tests run only when HERDSMAN_HC_LIVE=1, and only on darwin/arm64,
// the platform the backend is qualified on so far.
package hclive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// EnvVar opts in to the live tests.
const EnvVar = "HERDSMAN_HC_LIVE"

// Require skips t unless the live tests are asked for, the platform is
// darwin/arm64 and hc is installed. It returns hc's path.
func Require(t testing.TB) string {
	t.Helper()
	if os.Getenv(EnvVar) != "1" {
		t.Skipf("set %s=1 to run against a private core of the installed hc", EnvVar)
	}
	if goruntime.GOOS != "darwin" || goruntime.GOARCH != "arm64" {
		t.Skipf("the live core gates run on darwin/arm64 only; this is %s/%s", goruntime.GOOS, goruntime.GOARCH)
	}
	bin, err := exec.LookPath("hc")
	if err != nil {
		t.Skip("hc is not installed")
	}
	return bin
}

// Options shape a private core.
type Options struct {
	// Env is added to the environment the core is started with, as the
	// environment of another agent would be.
	Env []string
	// Config is added to the core's config.toml before it starts.
	Config string
}

// Core is one private core.
type Core struct {
	t       testing.TB
	Bin     string
	Dir     string
	Session string
	Socket  string
	// Env is the environment hc runs with: the private session and folders.
	Env []string
}

var sessions atomic.Int64

// Start starts a private core and registers its stop. The folder is under
// /tmp because core refuses socket paths over 103 bytes.
func Start(t testing.TB, opts Options) *Core {
	t.Helper()
	bin := Require(t)
	dir, err := os.MkdirTemp("/tmp", "hsl")
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{t: t, Bin: bin, Dir: dir, Session: fmt.Sprintf("hsl%d-%d", os.Getpid(), sessions.Add(1))}
	env := []string{"HC_SESSION=" + c.Session}
	for _, kv := range [][2]string{{"XDG_RUNTIME_DIR", "run"}, {"XDG_STATE_HOME", "state"}, {"XDG_DATA_HOME", "data"}, {"XDG_CONFIG_HOME", "config"}} {
		p := filepath.Join(dir, kv[1])
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		env = append(env, kv[0]+"="+p)
	}
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(name, "HC_") || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "HERDR_") {
			continue
		}
		env = append(env, e)
	}
	c.Env = append(env, opts.Env...)
	// No notes, and a recovery shell that is /bin/sh, not the user's login
	// shell with their startup files. It writes its own environment to a
	// file first, so a test can read what a recovery shell inherited
	// without typing into it.
	cfg := filepath.Join(dir, "config", "herdr-core")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("notes = false\nshell = [\"/bin/sh\", \"-c\", \"env > %s/shell-$HC_TERMINAL_ID.env; exec /bin/sh -i\"]\n", dir) + opts.Config
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Socket = filepath.Join(dir, "run", "herdr-core", c.Session+".sock")
	if out, err := c.HC("server", "start", "--json"); err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("start a private core: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := c.HC("server", "stop", "--json"); err != nil {
			t.Errorf("stop the private core %s: %v\n%s", c.Session, err, out)
		}
		_ = os.RemoveAll(dir)
	})
	return c
}

// HC runs hc against this core only.
func (c *Core) HC(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Env = c.Env
	return cmd.CombinedOutput()
}

// Restart stops the core and starts the same session again: every kept
// terminal comes back as a shell (N8).
func (c *Core) Restart() {
	c.t.Helper()
	if out, err := c.HC("server", "stop", "--json"); err != nil {
		c.t.Fatalf("stop the private core: %v\n%s", err, out)
	}
	if out, err := c.HC("server", "start", "--json"); err != nil {
		c.t.Fatalf("start the private core again: %v\n%s", err, out)
	}
}

// ShellEnv is the environment the recovery shell at address started with,
// as it wrote it; ok is false while no shell has started there.
func (c *Core) ShellEnv(address string) (env []string, ok bool) {
	b, err := os.ReadFile(filepath.Join(c.Dir, "shell-"+address+".env"))
	if err != nil {
		return nil, false
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n"), true
}

// PrivateEnv is what a process needs to reach this core with hc: the
// session and the folders.
func (c *Core) PrivateEnv() []string {
	var out []string
	for _, e := range c.Env {
		if strings.HasPrefix(e, "HC_SESSION=") || strings.HasPrefix(e, "XDG_") {
			out = append(out, e)
		}
	}
	return out
}

// GetRaw is terminal.get as core answers it, every field included.
func (c *Core) GetRaw(address string) (map[string]any, error) {
	out, err := c.HC("get", address, "--json")
	if err != nil {
		return nil, fmt.Errorf("hc get %s: %v: %s", address, err, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("hc get %s: %v: %s", address, err, out)
	}
	return m, nil
}

// ListRaw is terminal.list as core answers it, finished terminals included.
func (c *Core) ListRaw() ([]byte, error) { return c.HC("ls", "--all", "--json") }

// StateHolds reports the files under the core's state, data and config
// folders whose bytes contain marker.
func (c *Core) StateHolds(marker string) []string {
	var found []string
	for _, sub := range []string{"state", "data", "config", "run"} {
		_ = filepath.WalkDir(filepath.Join(c.Dir, sub), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err == nil && strings.Contains(string(b), marker) {
				found = append(found, p)
			}
			return nil
		})
	}
	return found
}

// Proxy stands between a client and a core: it forwards every connection to
// the upstream socket, counts the methods clients call, can drop every
// connection at once, and can be pointed at another core.
type Proxy struct {
	Path string

	ln       net.Listener
	mu       sync.Mutex
	upstream string
	calls    map[string]int
	requests []string
	conns    map[net.Conn]bool
}

// NewProxy listens on path and forwards to upstream until the test ends.
func NewProxy(t testing.TB, path, upstream string) *Proxy {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{Path: path, ln: ln, upstream: upstream, calls: map[string]int{}, conns: map[net.Conn]bool{}}
	go p.serve()
	t.Cleanup(func() { _ = ln.Close(); p.Cut() })
	return p
}

// Upstream points new connections at another core's socket.
func (p *Proxy) Upstream(socket string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.upstream = socket
}

// Calls is how many requests of method clients sent.
func (p *Proxy) Calls(method string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[method]
}

// Requests are the requests clients sent, each as its line.
func (p *Proxy) Requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

// Cut drops every open connection, as a lost connection would.
func (p *Proxy) Cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
	clear(p.conns)
}

func (p *Proxy) track(cs ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range cs {
		p.conns[c] = true
	}
}

func (p *Proxy) serve() {
	for {
		down, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		target := p.upstream
		p.mu.Unlock()
		up, err := net.Dial("unix", target)
		if err != nil {
			_ = down.Close()
			continue
		}
		p.track(down, up)
		go func() {
			_, _ = io.Copy(down, up)
			_ = down.Close()
		}()
		go func() {
			defer up.Close()
			r := bufio.NewReaderSize(down, 1<<16)
			for {
				line, err := r.ReadBytes('\n')
				if len(line) > 0 {
					var req struct {
						Method string `json:"method"`
					}
					if json.Unmarshal(line, &req) == nil && req.Method != "" {
						p.mu.Lock()
						p.calls[req.Method]++
						p.requests = append(p.requests, string(line))
						p.mu.Unlock()
					}
					if _, werr := up.Write(line); werr != nil {
						return
					}
				}
				if err != nil {
					if !errors.Is(err, io.EOF) {
						_ = down.Close()
					}
					return
				}
			}
		}()
	}
}
