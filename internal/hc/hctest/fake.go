// Package hctest runs an in-process fake herdr-core on a Unix socket for
// tests. It speaks the wire contract of hc 0.1.20 (JSON-RPC 2.0, one message
// per line) for the methods and events Herdsman uses, records every request,
// and can drop a method, restart (a new epoch with the sequence back to 0) or
// tell subscribers they missed events. Tests never reach a real core.
package hctest

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Protocol generations the fake can speak.
const (
	// Pinned is the generation Herdsman speaks (hc 0.1.20).
	Pinned uint32 = 1
	// Previous is the generation before it. A core at Previous refuses a
	// hello at Pinned, as a real core refuses any generation but its own.
	Previous uint32 = 0
)

// Version is the core version the fake reports.
const Version = "0.1.20-fake"

// Methods are the methods the fake serves, as hc 0.1.20 lists them in its
// hello answer.
var Methods = []string{
	"hello", "server.status", "server.stop", "server.restart", "terminal.run", "templates.list",
	"terminal.list", "terminal.get", "terminal.kill", "terminal.rm", "terminal.keep", "terminal.label",
	"terminal.read", "terminal.attach", "terminal.send", "terminal.wait", "terminal.keys",
	"terminal.interrupt", "terminal.put", "machine.exec", "file.read", "file.write", "file.edit",
	"doc.get", "doc.set", "events.subscribe", "machine.add", "machine.list", "machine.connect",
	"machine.rename", "machine.rm", "rpc.discover",
}

// Features are the hello features of hc 0.1.20.
var Features = []string{"framed", "attach", "events", "status", "shell", "resize_notifications", "automatic_input", "restart_environment"}

// Request is one recorded request.
type Request struct {
	Method string
	Params map[string]any
}

// Option configures a Core.
type Option func(*Core)

// WithGeneration makes the fake speak another protocol generation.
func WithGeneration(g uint32) Option { return func(c *Core) { c.generation = g } }

// Core is the fake core.
type Core struct {
	Socket string
	ID     string

	ln  net.Listener
	wg  sync.WaitGroup
	dir string

	mu         sync.Mutex
	generation uint32
	dropped    map[string]bool
	failures   map[string]string // method to error kind, for one call
	terms      map[string]map[string]any
	order      []string
	next       int
	pid        uint32
	seq        uint64
	epoch      int
	conns      map[*conn]bool
	requests   []Request
	execs      map[string]ExecAnswer
	// machines are the other machines added, in order; remote is the next
	// terminal number on each.
	machines []string
	remote   map[string]int
	// posting counts replies whose follow-up events are still being sent;
	// settled is signalled when it returns to zero.
	posting int
	settled *sync.Cond
}

// ExecAnswer is what machine.exec answers for one argv[0].
type ExecAnswer struct {
	Code   int32
	Stdout string
	Stderr string
}

type conn struct {
	c   net.Conn
	wmu sync.Mutex
	// sub is set once the connection subscribed; it then carries only
	// notifications.
	sub *subscription
}

type subscription struct {
	labels   map[string]string
	terminal string
}

func (c *conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.c.Write(append(b, '\n'))
	return err
}

// Start listens on a fresh socket that is closed when the test ends. The
// directory is short because macOS limits socket paths to 104 bytes.
func Start(t testing.TB, opts ...Option) *Core {
	t.Helper()
	dir, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{
		Socket: sock, ID: "fake0000000000000000000000000001", ln: ln, dir: dir,
		generation: Pinned, dropped: map[string]bool{}, failures: map[string]string{},
		terms: map[string]map[string]any{}, next: 1, pid: 40000, conns: map[*conn]bool{},
		execs: map[string]ExecAnswer{}, remote: map[string]int{},
	}
	c.settled = sync.NewCond(&c.mu)
	for _, o := range opts {
		o(c)
	}
	c.wg.Add(1)
	go c.serve()
	t.Cleanup(c.stop)
	return c
}

func (c *Core) stop() {
	_ = c.ln.Close()
	c.hangup()
	c.wg.Wait()
	_ = os.RemoveAll(c.dir)
}

// Drop removes a method: hello no longer lists it, and a call to it gets
// unknown_method.
func (c *Core) Drop(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropped[method] = true
}

// DropFeature removes a hello feature.
func (c *Core) DropFeature(feature string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropped["feature:"+feature] = true
}

// Fail makes the next call of method answer with an error of kind.
func (c *Core) Fail(method, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[method] = kind
}

// SetExec sets machine.exec's answer for one program.
func (c *Core) SetExec(program string, a ExecAnswer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execs[program] = a
}

// Restart drops every connection and starts a new epoch: the sequence goes
// back to 0, the core id and the terminals stay (E08, E09).
func (c *Core) Restart() {
	c.hangup()
	c.mu.Lock()
	c.seq = 0
	c.epoch++
	c.mu.Unlock()
}

// Wipe gives the core a new identity, as wiping its state does. Connections
// stay until the next Restart.
func (c *Core) Wipe(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ID = id
}

// Epoch returns how many times the fake restarted.
func (c *Core) Epoch() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

// Settle waits until every event that follows a reply already sent has been
// sent too. A real core may send a call's events after its reply; a test that
// subscribes after such a call settles first, so those events land before
// its snapshot and not after it.
func (c *Core) Settle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.posting > 0 {
		c.settled.Wait()
	}
}

// Seq returns the last event sequence.
func (c *Core) Seq() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

func (c *Core) hangup() {
	c.mu.Lock()
	conns := slices.Collect(maps.Keys(c.conns))
	c.mu.Unlock()
	for _, x := range conns {
		_ = x.c.Close()
	}
}

// Subscribers returns how many subscriptions are open.
func (c *Core) Subscribers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for x := range c.conns {
		if x.sub != nil {
			n++
		}
	}
	return n
}

// Requests returns a copy of the requests so far.
func (c *Core) Requests() []Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requests)
}

// Calls returns how many times method was called.
func (c *Core) Calls(method string) int {
	n := 0
	for _, r := range c.Requests() {
		if r.Method == method {
			n++
		}
	}
	return n
}

// Terminal returns a copy of one terminal record, or nil.
func (c *Core) Terminal(addr string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.terms[addr]; t != nil {
		return maps.Clone(t)
	}
	return nil
}

// Live returns copies of every live terminal record, in creation order.
func (c *Core) Live() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, a := range c.order {
		if t := c.terms[a]; t != nil && t["state"] == "live" {
			out = append(out, maps.Clone(t))
		}
	}
	return out
}

// Add creates a live terminal with these labels without a request, and
// announces it, as a terminal another client started.
func (c *Core) Add(labels map[string]string) string {
	c.mu.Lock()
	t := c.newTerminal([]string{"sh"}, labels, false, "")
	c.mu.Unlock()
	c.emit(t["address"].(string), "created", maps.Clone(t))
	return t["address"].(string)
}

// AddRemote creates a live terminal on another machine of this core, as hc
// 0.1.20 shows one: its address is "<machine>/t<n>", the record has no
// machine field, and terminal.list names the machine. pid is the record's
// pid (0 for the next fake one). It is announced like Add.
func (c *Core) AddRemote(machine string, labels map[string]string, pid uint32) string {
	c.mu.Lock()
	if _, ok := c.remote[machine]; !ok {
		c.machines = append(c.machines, machine)
		c.remote[machine] = 1
	}
	n := c.remote[machine]
	c.remote[machine]++
	t := c.newTerminal([]string{"sh"}, labels, false, "")
	delete(c.terms, t["address"].(string))
	c.order = c.order[:len(c.order)-1]
	c.next--
	addr := machine + "/t" + strconv.Itoa(n)
	t["address"], t["notify"], t["cwd"] = addr, true, "/home/remote"
	if pid != 0 {
		t["pid"] = pid
	}
	c.terms[addr] = t
	c.order = append(c.order, addr)
	out := maps.Clone(t)
	c.mu.Unlock()
	c.emit(addr, "created", out)
	return addr
}

// Mutate changes a terminal and advances the sequence without telling any
// subscriber: the events a slow subscriber missed. Missed then sends the
// fresh snapshot.
func (c *Core) Mutate(addr string, fn func(t map[string]any)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.terms[addr]; t != nil {
		fn(t)
		c.seq++
	}
}

// Missed sends every subscriber a missed notification with a fresh snapshot
// of its terminals at the current sequence.
func (c *Core) Missed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for x := range c.conns {
		if x.sub != nil {
			_ = x.write(notification("missed", map[string]any{"seq": c.seq, "terminals": c.matching(x.sub)}))
		}
	}
}

// Heartbeat sends every subscriber a heartbeat at the current sequence.
func (c *Core) Heartbeat() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for x := range c.conns {
		if x.sub != nil {
			_ = x.write(notification("heartbeat", map[string]any{"seq": c.seq}))
		}
	}
}

// EmitAt sends every subscriber an event with the given sequence, without
// changing state or the sequence: an event racing the snapshot.
func (c *Core) EmitAt(seq uint64, addr, typ string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for x := range c.conns {
		if x.sub != nil {
			_ = x.write(notification("event", map[string]any{"seq": seq, "terminal": addr, "type": typ, "data": data}))
		}
	}
}

// Emit advances the sequence and sends one event to the subscribers that
// follow the terminal, without changing state.
func (c *Core) Emit(addr, typ string, data any) { c.emit(addr, typ, data) }

// Notify sends every subscriber a raw notification.
func (c *Core) Notify(method string, params any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for x := range c.conns {
		if x.sub != nil {
			_ = x.write(notification(method, params))
		}
	}
}

// emit advances the sequence and sends one event to the subscribers that
// follow the terminal.
func (c *Core) emit(addr, typ string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := c.terms[addr]
	for x := range c.conns {
		if x.sub == nil || (t != nil && !follows(x.sub, t)) {
			continue
		}
		_ = x.write(notification("event", map[string]any{"seq": c.seq, "terminal": addr, "type": typ, "data": data}))
	}
}

func notification(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

func follows(s *subscription, t map[string]any) bool {
	if s.terminal != "" && t["address"] != s.terminal && t["name"] != s.terminal {
		return false
	}
	labels, _ := t["labels"].(map[string]string)
	for k, v := range s.labels {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (c *Core) matching(s *subscription) []map[string]any {
	out := []map[string]any{}
	for _, a := range c.order {
		if t := c.terms[a]; t != nil && t["state"] == "live" && follows(s, t) {
			out = append(out, maps.Clone(t))
		}
	}
	return out
}

func (c *Core) newTerminal(argv []string, labels map[string]string, keep bool, name string) map[string]any {
	addr := "t" + strconv.Itoa(c.next)
	c.next++
	c.pid++
	if labels == nil {
		labels = map[string]string{}
	}
	t := map[string]any{
		"address": addr, "argv": argv, "cols": 200, "rows": 120, "created_ms": uint64(1794353479752 + c.next),
		"cwd": "/work", "die_with_parent": false, "exit_code": nil, "finished_ms": nil, "identity": "ghostty",
		"keep": keep, "labels": labels, "name": nil, "notify": false, "parent": nil, "pid": c.pid,
		"reason": nil, "reported": false, "rm": false, "screen": "exact", "signal": nil, "state": "live",
		"status": nil, "status_ms": nil, "template": nil, "title": "", "turn": 0,
		// A field no Herdsman build knows: answers must tolerate it.
		"later_field": map[string]any{"x": 1},
	}
	if name != "" {
		t["name"] = name
	}
	c.terms[addr] = t
	c.order = append(c.order, addr)
	return t
}

func (c *Core) serve() {
	defer c.wg.Done()
	for {
		nc, err := c.ln.Accept()
		if err != nil {
			return
		}
		x := &conn{c: nc}
		c.mu.Lock()
		c.conns[x] = true
		c.mu.Unlock()
		c.wg.Add(1)
		go c.handle(x)
	}
}

type wireRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  map[string]any  `json:"params"`
}

type rpcError struct {
	code int
	kind string
	msg  string
	hint string
}

func failure(kind, msg string) *rpcError { return &rpcError{code: -32000, kind: kind, msg: msg} }

func (c *Core) handle(x *conn) {
	defer c.wg.Done()
	defer func() {
		_ = x.c.Close()
		c.mu.Lock()
		delete(c.conns, x)
		c.mu.Unlock()
	}()
	r := bufio.NewReaderSize(x.c, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req wireRequest
		if err := json.Unmarshal(line, &req); err != nil || req.JSONRPC != "2.0" {
			_ = x.write(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{
				"code": -32700, "message": "parse error", "data": map[string]any{"kind": "parse_error", "hint": "see `hc schema`"}}})
			continue
		}
		if req.Params == nil {
			req.Params = map[string]any{}
		}
		c.mu.Lock()
		c.requests = append(c.requests, Request{Method: req.Method, Params: req.Params})
		c.mu.Unlock()
		result, rerr, after := c.dispatch(x, req.Method, req.Params)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			data := map[string]any{"kind": rerr.kind}
			if rerr.hint != "" {
				data["hint"] = rerr.hint
			}
			resp["error"] = map[string]any{"code": rerr.code, "message": rerr.msg, "data": data}
		} else {
			resp["result"] = result
		}
		if after != nil {
			c.mu.Lock()
			c.posting++
			c.mu.Unlock()
		}
		err = x.write(resp)
		if after != nil {
			if err == nil {
				after()
			}
			c.mu.Lock()
			if c.posting--; c.posting == 0 {
				c.settled.Broadcast()
			}
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func str(p map[string]any, k string) string { s, _ := p[k].(string); return s }

func labelsOf(p map[string]any, k string) map[string]string {
	out := map[string]string{}
	if m, ok := p[k].(map[string]any); ok {
		for key, v := range m {
			out[key], _ = v.(string)
		}
	}
	return out
}

// dispatch answers one request. after runs once the answer is written, so
// events follow their answer as on a real core.
func (c *Core) dispatch(x *conn, method string, p map[string]any) (any, *rpcError, func()) {
	c.mu.Lock()
	gen := c.generation
	dropped := c.dropped[method]
	kind, failing := c.failures[method]
	delete(c.failures, method)
	c.mu.Unlock()
	if !slices.Contains(Methods, method) || dropped {
		return nil, &rpcError{code: -32601, kind: "unknown_method", msg: fmt.Sprintf("unknown method %q", method), hint: "see `hc schema`"}, nil
	}
	if failing {
		return nil, failure(kind, method+" failed"), nil
	}
	switch method {
	case "hello":
		if g, ok := p["generation"].(float64); ok && uint32(g) != gen {
			return nil, &rpcError{code: -32000, kind: "unsupported_generation",
				msg: fmt.Sprintf("protocol generation %d not supported", uint32(g)), hint: fmt.Sprintf("this core speaks generation %d", gen)}, nil
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		var methods, features []string
		for _, m := range Methods {
			if !c.dropped[m] {
				methods = append(methods, m)
			}
		}
		for _, f := range Features {
			if !c.dropped["feature:"+f] {
				features = append(features, f)
			}
		}
		return map[string]any{
			"core": map[string]any{"id": c.ID, "name": "fake-core"}, "features": features, "framed": false,
			"generation": gen, "methods": methods, "version": Version,
			"login_session": map[string]any{"gui": true, "id": 1, "user": 501},
		}, nil, nil
	case "server.status":
		c.mu.Lock()
		defer c.mu.Unlock()
		live := 0
		for _, t := range c.terms {
			if t["state"] == "live" {
				live++
			}
		}
		return map[string]any{
			"core": map[string]any{"id": c.ID, "name": "fake-core"}, "version": Version, "pid": 39999,
			"uptime_ms": 1000, "holders": []any{map[string]any{"pid": 39998, "instance": "fake", "current": true, "terminals": live}},
			"holder": nil, "installed_by": nil, "live": live, "finished": len(c.terms) - live,
		}, nil, nil
	case "terminal.run":
		return c.run(p)
	case "terminal.list":
		c.mu.Lock()
		defer c.mu.Unlock()
		want := labelsOf(p, "labels")
		all, _ := p["all"].(bool)
		out := []map[string]any{}
		hidden := 0
		for _, a := range c.order {
			t := c.terms[a]
			if t == nil || !follows(&subscription{labels: want}, t) {
				continue
			}
			if t["state"] != "live" && !all {
				hidden++
				continue
			}
			out = append(out, maps.Clone(t))
		}
		// Like hc 0.1.20: no machines field when no machine was added.
		res := map[string]any{"terminals": out, "hidden_finished": hidden}
		if len(c.machines) > 0 {
			var ms []map[string]any
			for _, m := range c.machines {
				ms = append(ms, map[string]any{"name": m, "state": "connected"})
			}
			res["machines"] = ms
		}
		return res, nil, nil
	case "terminal.get":
		t, err := c.lookup(str(p, "terminal"))
		return t, err, nil
	case "terminal.read":
		t, err := c.lookup(str(p, "terminal"))
		if err != nil {
			return nil, err, nil
		}
		text := "screen of " + t["address"].(string)
		if s, ok := t["test_screen"].(string); ok {
			text = s // a test's screen, set with Mutate
		}
		return map[string]any{"text": text, "complete": true, "how": "screen", "future": true}, nil, nil
	case "terminal.send":
		return c.send(p)
	case "terminal.interrupt":
		t, err := c.lookup(str(p, "terminal"))
		if err != nil {
			return nil, err, nil
		}
		if t["state"] != "live" {
			return nil, failure("finished", "it has finished"), nil
		}
		if t["status"] == nil {
			return map[string]any{"key": "ctrl+c", "outcome": "unknown"}, nil, nil
		}
		return map[string]any{"key": "esc", "outcome": "stopped"}, nil, nil
	case "terminal.kill":
		return c.kill(str(p, "terminal"))
	case "terminal.label":
		return c.label(str(p, "terminal"), labelsOf(p, "labels"))
	case "terminal.keep":
		return c.keep(str(p, "terminal"), p["keep"] == true)
	case "machine.exec":
		return c.exec(p)
	case "events.subscribe":
		s := &subscription{labels: labelsOf(p, "labels"), terminal: str(p, "terminal")}
		c.mu.Lock()
		defer c.mu.Unlock()
		if s.terminal != "" && c.find(s.terminal) == nil {
			return nil, failure("not_found", fmt.Sprintf("no terminal %q", s.terminal)), nil
		}
		x.sub = s
		return map[string]any{"channel": nil, "seq": c.seq, "terminals": c.matching(s)}, nil, nil
	}
	return nil, failure("unavailable", method+" is not faked"), nil
}

func (c *Core) find(ref string) map[string]any {
	if t := c.terms[ref]; t != nil {
		return t
	}
	for _, t := range c.terms {
		if t["name"] == ref {
			return t
		}
	}
	return nil
}

func (c *Core) lookup(ref string) (map[string]any, *rpcError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.find(ref)
	if t == nil {
		return nil, &rpcError{code: -32000, kind: "not_found", msg: fmt.Sprintf("no terminal %q", ref), hint: "see `hc ls --all`"}
	}
	return maps.Clone(t), nil
}

func (c *Core) run(p map[string]any) (any, *rpcError, func()) {
	var argv []string
	if a, ok := p["argv"].([]any); ok {
		for _, v := range a {
			s, _ := v.(string)
			argv = append(argv, s)
		}
	}
	if len(argv) == 0 {
		return nil, failure("invalid_params", "nothing to run"), nil
	}
	if p["no_parent"] != true || p["no_notify"] != true {
		return nil, failure("invalid_params", "fake core: Herdsman launches set no_parent and no_notify"), nil
	}
	if _, ok := p["on"]; ok {
		return nil, failure("invalid_params", "fake core: no other machines"), nil
	}
	name := str(p, "name")
	c.mu.Lock()
	if name != "" {
		for _, t := range c.terms {
			if t["name"] == name && t["state"] == "live" {
				c.mu.Unlock()
				return nil, failure("name_taken", fmt.Sprintf("%s is taken", name)), nil
			}
		}
	}
	if rm, ok := p["rm"].(bool); !ok || rm {
		c.mu.Unlock()
		return nil, failure("invalid_params", "fake core: Herdsman launches keep their record (rm:false)"), nil
	}
	t := c.newTerminal(argv, labelsOf(p, "labels"), p["keep"] == true, name)
	if cwd := str(p, "cwd"); cwd != "" {
		t["cwd"] = cwd
	}
	// Core keeps the launch env in the terminal's record for its whole life.
	t["env"] = labelsOf(p, "env")
	out := maps.Clone(t)
	c.mu.Unlock()
	addr := out["address"].(string)
	return out, nil, func() { c.emit(addr, "created", maps.Clone(out)) }
}

// SetStatus sets a terminal's reported status word and announces it.
func (c *Core) SetStatus(addr, state string) {
	c.mu.Lock()
	t := c.terms[addr]
	if t == nil {
		c.mu.Unlock()
		return
	}
	st := map[string]any{"state": state, "app": "fake", "msg": "", "title": ""}
	t["status"] = st
	t["status_ms"] = uint64(time.Now().UnixMilli())
	t["reported"] = true
	c.mu.Unlock()
	c.emit(addr, "status", maps.Clone(st))
}

func (c *Core) send(p map[string]any) (any, *rpcError, func()) {
	t, err := c.lookup(str(p, "terminal"))
	if err != nil {
		return nil, err, nil
	}
	if t["state"] != "live" {
		return nil, failure("finished", "it has finished"), nil
	}
	word := "silent"
	if st, ok := t["status"].(map[string]any); ok {
		word, _ = st["state"].(string)
	}
	if word == "blocked" && p["force"] != true {
		return nil, failure("blocked", "it is blocked"), nil
	}
	if want := str(p, "if"); want != "" && want != word {
		e := failure("status_mismatch", fmt.Sprintf("%s is %s, not %s", t["address"], word, want))
		e.hint = "nothing was typed"
		return nil, e, nil
	}
	c.mu.Lock()
	live := c.terms[t["address"].(string)]
	turn, _ := live["turn"].(int)
	turn++
	live["turn"] = turn
	c.mu.Unlock()
	addr := t["address"].(string)
	return map[string]any{"turn": turn, "status": word, "started": true}, nil, func() {
		c.emit(addr, "turn", map[string]any{"turn": turn})
	}
}

func (c *Core) kill(ref string) (any, *rpcError, func()) {
	c.mu.Lock()
	t := c.find(ref)
	if t == nil {
		c.mu.Unlock()
		return nil, failure("not_found", fmt.Sprintf("no terminal %q", ref)), nil
	}
	finished := t["state"] == "live"
	if finished {
		t["state"] = "finished"
		t["reason"] = "killed"
		t["finished_ms"] = uint64(time.Now().UnixMilli())
		t["status"] = nil
	}
	out := maps.Clone(t)
	c.mu.Unlock()
	addr := out["address"].(string)
	if !finished {
		return out, nil, nil
	}
	return out, nil, func() {
		c.emit(addr, "finished", map[string]any{"exit_code": nil, "finished_ms": out["finished_ms"], "reason": "killed", "signal": nil})
	}
}

func (c *Core) label(ref string, set map[string]string) (any, *rpcError, func()) {
	if len(set) == 0 {
		return nil, failure("invalid_params", "no labels"), nil
	}
	c.mu.Lock()
	t := c.find(ref)
	if t == nil {
		c.mu.Unlock()
		return nil, failure("not_found", fmt.Sprintf("no terminal %q", ref)), nil
	}
	labels := maps.Clone(t["labels"].(map[string]string))
	for k, v := range set {
		if v == "" {
			delete(labels, k)
		} else {
			labels[k] = v
		}
	}
	t["labels"] = labels
	out := maps.Clone(t)
	c.mu.Unlock()
	addr := out["address"].(string)
	return out, nil, func() { c.emitLabels(addr, labels, set) }
}

// emitLabels announces a label change to every subscriber that follows the
// terminal now, or that filters on a label the change touched and so may
// have followed it before: membership changes both ways.
func (c *Core) emitLabels(addr string, labels, set map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	now := map[string]any{"address": addr, "labels": labels}
	for x := range c.conns {
		if x.sub == nil || (!follows(x.sub, now) && !filtersOn(x.sub, addr, set)) {
			continue
		}
		_ = x.write(notification("event", map[string]any{"seq": c.seq, "terminal": addr, "type": "labels", "data": map[string]any{"labels": labels}}))
	}
}

func filtersOn(s *subscription, addr string, set map[string]string) bool {
	if s.terminal != "" && s.terminal != addr {
		return false
	}
	for k := range set {
		if _, ok := s.labels[k]; ok {
			return true
		}
	}
	return false
}

func (c *Core) keep(ref string, keep bool) (any, *rpcError, func()) {
	c.mu.Lock()
	t := c.find(ref)
	if t == nil {
		c.mu.Unlock()
		return nil, failure("not_found", fmt.Sprintf("no terminal %q", ref)), nil
	}
	if t["state"] != "live" {
		c.mu.Unlock()
		return nil, failure("invalid_params", "only live terminals can be kept"), nil
	}
	t["keep"] = keep
	out := maps.Clone(t)
	c.mu.Unlock()
	addr := out["address"].(string)
	return out, nil, func() { c.emit(addr, "keep", map[string]any{"keep": keep}) }
}

func (c *Core) exec(p map[string]any) (any, *rpcError, func()) {
	a, _ := p["argv"].([]any)
	if len(a) == 0 {
		return nil, failure("invalid_params", "nothing to run"), nil
	}
	prog, _ := a[0].(string)
	c.mu.Lock()
	ans, ok := c.execs[prog]
	c.mu.Unlock()
	if !ok {
		if prog == "echo" {
			var words []string
			for _, w := range a[1:] {
				s, _ := w.(string)
				words = append(words, s)
			}
			ans = ExecAnswer{Stdout: strings.Join(words, " ") + "\n"}
		} else {
			return nil, failure("spawn_failed", "no such program "+prog), nil
		}
	}
	return map[string]any{
		"code": ans.Code, "signal": nil, "ms": 3, "timed_out": false, "truncated": false,
		"stdout": base64.StdEncoding.EncodeToString([]byte(ans.Stdout)),
		"stderr": base64.StdEncoding.EncodeToString([]byte(ans.Stderr)),
	}, nil, nil
}
