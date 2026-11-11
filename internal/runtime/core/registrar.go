package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/sys"
)

// TerminalReader reads one terminal's record from the verified core.
type TerminalReader interface {
	Get(ctx context.Context, terminal string) (hc.Terminal, error)
}

// The stages a launch reaches on the launch socket (spec section 8).
const (
	StageAuthenticated = "authenticated"
	StageRegistered    = "registered"
	StageFailed        = "failed"
	StageRefused       = "refused"
)

// Outcome is one thing the registrar saw of a launch.
type Outcome struct {
	Stage    string
	Evidence runtime.ProcessEvidence
	Reason   string
	At       time.Time
}

// Arm is a launch the registrar waits for: the reserved terminal on the
// verified core, and what to do once the wrapper registers.
type Arm struct {
	Launch  Launch
	Address string
	Core    TerminalReader
	// Registered records the execution's process evidence. It runs before
	// the wrapper is told, so a launch that cannot be recorded never starts
	// its harness. An error refuses the registration.
	Registered func(ctx context.Context, e runtime.ProcessEvidence) error
}

type armed struct {
	Arm
	out  chan Outcome
	busy bool
}

// Registrar serves the daemon's launch socket. It authenticates a core
// wrapper by process: the socket peer must be the program of the terminal the
// daemon reserved for that intent, as core reports it. The intent the wrapper
// names is a reference, not a secret; no bearer value exists to leak.
type Registrar struct {
	Path  string
	Procs ProcessTable
	// Peer reads a connection's peer pid (default: the kernel's record).
	Peer func(net.Conn) (int, error)
	Now  func() time.Time

	ln   net.Listener
	mu   sync.Mutex
	arms map[string]*armed
	wg   sync.WaitGroup
}

// NewRegistrar listens on path, a socket only this user can reach.
func NewRegistrar(path string, procs ProcessTable) (*Registrar, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	if procs == nil {
		procs = SystemProcs{}
	}
	return &Registrar{Path: path, Procs: procs, ln: ln, arms: map[string]*armed{}, Now: time.Now, Peer: kernelPeer}, nil
}

func kernelPeer(c net.Conn) (int, error) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return 0, errors.New("core: the launch connection has no peer")
	}
	cred, err := sys.PeerCredentials(sc)
	if err != nil {
		return 0, err
	}
	return cred.PID, nil
}

// Serve accepts wrappers until ctx ends or Close.
func (r *Registrar) Serve(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = r.ln.Close()
	}()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			r.wg.Wait()
			return
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(ctx, c)
		}()
	}
}

// Close stops listening.
func (r *Registrar) Close() error {
	err := r.ln.Close()
	_ = os.Remove(r.Path)
	return err
}

// Arm makes the registrar wait for a launch. Arming an intent again keeps
// the outcomes already queued. The channel gets every outcome of the launch.
func (r *Registrar) Arm(a Arm) <-chan Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, ok := r.arms[a.Launch.Intent]; ok {
		prev.Arm = a
		return prev.out
	}
	x := &armed{Arm: a, out: make(chan Outcome, 8)}
	r.arms[a.Launch.Intent] = x
	return x.out
}

// Disarm forgets a launch; a wrapper that names it later is refused.
func (r *Registrar) Disarm(intent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.arms, intent)
}

func (r *Registrar) take(intent string) (*armed, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	x, ok := r.arms[intent]
	switch {
	case !ok:
		return nil, fmt.Errorf("no launch waits for intent %s", intent)
	case x.busy:
		return nil, fmt.Errorf("launch %s is already being registered", intent)
	}
	x.busy = true
	return x, nil
}

func (r *Registrar) release(x *armed) {
	r.mu.Lock()
	defer r.mu.Unlock()
	x.busy = false
}

func (x *armed) emit(o Outcome) {
	select {
	case x.out <- o:
	default: // a full queue drops the oldest news: the waiter reads state again
		<-x.out
		x.out <- o
	}
}

func reply(c net.Conn, rep launch.WrapReply) error {
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	_, err = c.Write(append(b, '\n'))
	return err
}

func readRequest(r *bufio.Reader) (launch.WrapRequest, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return launch.WrapRequest{}, err
	}
	if len(line) > 1<<16 {
		return launch.WrapRequest{}, errors.New("request too large")
	}
	var req launch.WrapRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return launch.WrapRequest{}, err
	}
	return req, nil
}

// peerIsProgram checks that pid is the live program of the reserved terminal
// and returns its evidence. Core's word for the terminal's pid and the
// kernel's word for the socket's peer must agree; the execution label must
// be this launch's.
func (r *Registrar) peerIsProgram(ctx context.Context, x *armed, pid int) (runtime.ProcessEvidence, error) {
	ev, err := Evidence(r.Procs, pid)
	if err != nil {
		return ev, fmt.Errorf("identify process %d: %w", pid, err)
	}
	t, err := x.Core.Get(ctx, x.Address)
	if err != nil {
		return ev, fmt.Errorf("read terminal %s from core: %w", x.Address, err)
	}
	switch {
	case !t.Live():
		return ev, fmt.Errorf("terminal %s has finished", x.Address)
	case t.PID == nil || int(*t.PID) != pid:
		return ev, fmt.Errorf("process %d is not the program of terminal %s", pid, x.Address)
	case t.Labels[herdrapp.LabelExecution] != x.Launch.Execution:
		return ev, fmt.Errorf("terminal %s does not carry execution %s", x.Address, x.Launch.Execution)
	}
	return ev, nil
}

func (r *Registrar) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	br := bufio.NewReaderSize(c, 1<<16)
	_ = c.SetReadDeadline(r.Now().Add(30 * time.Second))
	req, err := readRequest(br)
	if err != nil {
		return
	}
	if req.Op != launch.WrapAuthenticate || req.Protocol != launch.WrapProtocol {
		_ = reply(c, launch.WrapReply{Error: fmt.Sprintf("expected %s at protocol %d", launch.WrapAuthenticate, launch.WrapProtocol)})
		return
	}
	x, err := r.take(req.Intent)
	if err != nil {
		_ = reply(c, launch.WrapReply{Error: err.Error()})
		return
	}
	defer r.release(x)
	pid, err := r.Peer(c)
	if err != nil {
		_ = reply(c, launch.WrapReply{Error: "read the peer: " + err.Error()})
		return
	}
	first, err := r.peerIsProgram(ctx, x, pid)
	if err != nil {
		x.emit(Outcome{Stage: StageRefused, Reason: err.Error(), At: r.Now()})
		_ = reply(c, launch.WrapReply{Error: err.Error()})
		return
	}
	// Authenticated: this authorises the launch environment of this intent
	// and nothing else: no enrollment, no readiness, no standing as a caller.
	d := x.Launch.Descriptor
	x.emit(Outcome{Stage: StageAuthenticated, Evidence: first, At: r.Now()})
	if err := reply(c, launch.WrapReply{OK: true, Descriptor: &d}); err != nil {
		x.emit(Outcome{Stage: StageFailed, Reason: "the wrapper went away after authentication", At: r.Now()})
		return
	}
	_ = c.SetReadDeadline(r.Now().Add(preludeBound(d)))
	req, err = readRequest(br)
	if err != nil {
		x.emit(Outcome{Stage: StageFailed, Reason: "the wrapper went away before it registered", At: r.Now()})
		return
	}
	switch req.Op {
	case launch.WrapFailed:
		x.emit(Outcome{Stage: StageFailed, Reason: req.Reason, At: r.Now()})
		_ = reply(c, launch.WrapReply{OK: true})
		return
	case launch.WrapRegister:
	default:
		x.emit(Outcome{Stage: StageFailed, Reason: "unexpected request " + req.Op, At: r.Now()})
		_ = reply(c, launch.WrapReply{Error: "unexpected request " + req.Op})
		return
	}
	again, err := r.peerIsProgram(ctx, x, pid)
	if err == nil && again != first {
		err = fmt.Errorf("process %d changed between authentication and registration", pid)
	}
	if err == nil && x.Registered != nil {
		err = x.Registered(ctx, again)
	}
	if err != nil {
		x.emit(Outcome{Stage: StageFailed, Reason: err.Error(), At: r.Now()})
		_ = reply(c, launch.WrapReply{Error: err.Error()})
		return
	}
	x.emit(Outcome{Stage: StageRegistered, Evidence: again, At: r.Now()})
	_ = reply(c, launch.WrapReply{OK: true})
}

// preludeBound is how long a wrapper may run its prelude before it must say
// how it went.
func preludeBound(d launch.CoreDescriptor) time.Duration {
	per := time.Duration(d.TimeoutMS) * time.Millisecond
	if per <= 0 {
		per = launch.DefaultPreludeTimeout
	}
	return time.Duration(len(d.Prelude))*per + time.Minute
}
