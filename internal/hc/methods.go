package hc

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The one list of core methods Herdsman uses. Capabilities are checked
// against it at hello, and the client refuses to send anything else.
//
// Left out on purpose: terminal.keys and terminal.wait (no automation presses
// keys or waits on core's turn, spec §10), terminal.attach (framed streams are
// the app's and the optional TUI view's), doc.get and doc.set (mapping state
// stays in Herdsman's store, §14), file.*, templates.list, terminal.put,
// terminal.rm, server.stop, server.restart and machine.* other than exec.
const (
	MethodHello        = "hello"
	MethodServerStatus = "server.status"
	MethodRun          = "terminal.run"
	MethodList         = "terminal.list"
	MethodGet          = "terminal.get"
	MethodRead         = "terminal.read"
	MethodSend         = "terminal.send"
	MethodInterrupt    = "terminal.interrupt"
	MethodKill         = "terminal.kill"
	MethodLabel        = "terminal.label"
	MethodKeep         = "terminal.keep"
	MethodExec         = "machine.exec"
	MethodSubscribe    = "events.subscribe"
)

// Methods lists every core method Herdsman calls.
var Methods = []string{
	MethodHello,
	MethodServerStatus,
	MethodRun,
	MethodList,
	MethodGet,
	MethodRead,
	MethodSend,
	MethodInterrupt,
	MethodKill,
	MethodLabel,
	MethodKeep,
	MethodExec,
	MethodSubscribe,
}

// Features lists the hello features Herdsman needs: plain-mode event
// notifications.
var Features = []string{"events"}

// Notifications core sends on a subscription connection.
const (
	NotifyEvent     = "event"
	NotifyHeartbeat = "heartbeat"
	NotifyMissed    = "missed"
)

// Notifications lists every notification Herdsman reads.
var Notifications = []string{NotifyEvent, NotifyHeartbeat, NotifyMissed}

// Event types inside an event notification. Core may add more; an unknown
// type is passed on and leaves the cache alone.
const (
	EventCreated    = "created"
	EventForeground = "foreground"
	EventLost       = "lost"
	EventRestarted  = "restarted"
	EventKeep       = "keep"
	EventFinished   = "finished"
	EventRemoved    = "removed"
	EventStatus     = "status"
	EventTurn       = "turn"
	EventTitle      = "title"
	EventLabels     = "labels"
	EventSize       = "size"
	EventDoc        = "doc"
)

// EventTypes lists every event type Herdsman understands.
var EventTypes = []string{
	EventCreated, EventForeground, EventLost, EventRestarted, EventKeep, EventFinished,
	EventRemoved, EventStatus, EventTurn, EventTitle, EventLabels, EventSize, EventDoc,
}

func listed(method string) bool { return slices.Contains(Methods, method) }

// MissingFieldError is an answer that lacks a field the protocol requires.
type MissingFieldError struct{ Type, Field string }

func (e *MissingFieldError) Error() string {
	return fmt.Sprintf("hc: %s answer lacks required field %q", e.Type, e.Field)
}

// requireFields checks that b is an object carrying every named field. Fields
// marked with a trailing "!" must also not be null.
func requireFields(b []byte, typ string, fields ...string) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("hc: %s answer: %w", typ, err)
	}
	if m == nil {
		return fmt.Errorf("hc: %s answer is null", typ)
	}
	for _, f := range fields {
		nonNull := f[len(f)-1] == '!'
		if nonNull {
			f = f[:len(f)-1]
		}
		v, ok := m[f]
		if !ok || (nonNull && string(v) == "null") {
			return &MissingFieldError{Type: typ, Field: f}
		}
	}
	return nil
}

// CoreInfo is a core's identity. ID is permanent until its state is wiped.
type CoreInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *CoreInfo) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "core", "id!", "name!"); err != nil {
		return err
	}
	type plain CoreInfo
	return json.Unmarshal(b, (*plain)(c))
}

// HelloResult is core's answer to hello.
type HelloResult struct {
	Generation uint32   `json:"generation"`
	Version    string   `json:"version"`
	Core       CoreInfo `json:"core"`
	Framed     bool     `json:"framed"`
	Methods    []string `json:"methods"`
	Features   []string `json:"features"`
}

func (h *HelloResult) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "hello", "generation!", "version!", "core!", "framed!", "methods!", "features!"); err != nil {
		return err
	}
	type plain HelloResult
	return json.Unmarshal(b, (*plain)(h))
}

type helloParams struct {
	Generation uint32 `json:"generation"`
}

// Status is one OSC 7501 record: the program's own words, untranslated.
type Status struct {
	State    string `json:"state"` // idle, working, done, blocked or error
	Kind     string `json:"kind,omitempty"`
	Msg      string `json:"msg,omitempty"`
	Title    string `json:"title,omitempty"`
	App      string `json:"app,omitempty"`
	Progress *uint8 `json:"progress,omitempty"`
}

func (s *Status) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "status", "state!"); err != nil {
		return err
	}
	type plain Status
	return json.Unmarshal(b, (*plain)(s))
}

// Foreground is what runs in a terminal now.
type Foreground struct {
	PID  int32    `json:"pid"`
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

func (f *Foreground) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "foreground", "pid!", "name!", "argv!", "cwd!"); err != nil {
		return err
	}
	type plain Foreground
	return json.Unmarshal(b, (*plain)(f))
}

// Terminal is core's record of one terminal. A core address, created_ms, a
// name, a label or a status is never an execution's identity (spec §6).
type Terminal struct {
	Address    string            `json:"address"`
	Name       string            `json:"name,omitempty"`
	Labels     map[string]string `json:"labels"`
	Argv       []string          `json:"argv"`
	Cwd        string            `json:"cwd"`
	State      string            `json:"state"` // live or finished
	CreatedMS  uint64            `json:"created_ms"`
	FinishedMS *uint64           `json:"finished_ms,omitempty"`
	Screen     string            `json:"screen"`
	Cols       uint16            `json:"cols"`
	Rows       uint16            `json:"rows"`
	PID        *uint32           `json:"pid,omitempty"`
	ExitCode   *int32            `json:"exit_code,omitempty"`
	Signal     *int32            `json:"signal,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Keep       bool              `json:"keep,omitempty"`
	Notify     bool              `json:"notify,omitempty"`
	Parent     string            `json:"parent,omitempty"`
	Status     *Status           `json:"status,omitempty"`
	StatusMS   *uint64           `json:"status_ms,omitempty"`
	Turn       uint64            `json:"turn,omitempty"`
	Title      string            `json:"title,omitempty"`
	Foreground *Foreground       `json:"foreground,omitempty"`
	Reported   bool              `json:"reported,omitempty"`
}

// terminalFields are the fields core's schema requires of a terminal.
var terminalFields = []string{"address!", "labels!", "argv!", "cwd!", "state!", "created_ms!", "screen!", "cols!", "rows!"}

func (t *Terminal) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "terminal", terminalFields...); err != nil {
		return err
	}
	type plain Terminal
	return json.Unmarshal(b, (*plain)(t))
}

// Live reports whether the terminal's program (or its kept shell) runs.
func (t Terminal) Live() bool { return t.State == "live" }

// MachineOf is the other machine a core address names ("<machine>/t<n>"),
// or "" for a terminal on the core's own machine. hc 0.1.20 puts no machine
// field in a terminal record, an event or a snapshot: a remote record is
// known by its address alone.
func MachineOf(address string) string {
	machine, _, ok := strings.Cut(address, "/")
	if !ok {
		return ""
	}
	return machine
}

// Local reports a terminal on the core's own machine. V1 acts on local
// terminals only (spec section 11).
func (t Terminal) Local() bool { return t.Address != "" && MachineOf(t.Address) == "" }

// Holder is one core holder process.
type Holder struct {
	PID       uint32 `json:"pid"`
	Instance  string `json:"instance"`
	Current   bool   `json:"current"`
	Terminals uint   `json:"terminals"`
}

func (h *Holder) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "holder", "pid!", "instance!", "current!", "terminals!"); err != nil {
		return err
	}
	type plain Holder
	return json.Unmarshal(b, (*plain)(h))
}

// ServerStatus is core's answer to server.status: its identity and holders.
type ServerStatus struct {
	Core     CoreInfo `json:"core"`
	Version  string   `json:"version"`
	PID      uint32   `json:"pid"`
	UptimeMS uint64   `json:"uptime_ms"`
	Holders  []Holder `json:"holders"`
	Live     uint     `json:"live"`
	Finished uint     `json:"finished"`
}

func (s *ServerStatus) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "server.status", "core!", "version!", "pid!", "uptime_ms!", "holders!", "live!", "finished!"); err != nil {
		return err
	}
	type plain ServerStatus
	return json.Unmarshal(b, (*plain)(s))
}

// ServerStatus asks core for its identity, holders and terminal counts. On a
// socket this never starts a core (N14 is about the hc command).
func (c *Client) ServerStatus(ctx context.Context) (ServerStatus, error) {
	var out ServerStatus
	err := c.call(ctx, MethodServerStatus, nil, &out)
	return out, err
}

// Size is a terminal's first size in cells.
type Size struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// RunRequest starts one program in a new terminal on this core. Herdsman
// launches always go without a core parent and without core notes (spec §4),
// keep their finished record (rm:false, §13), never wait, and never run on
// another machine; the client sets those itself.
type RunRequest struct {
	Argv   []string
	Cwd    string
	Env    map[string]string
	Labels map[string]string
	Name   string
	Keep   bool
	Size   *Size
}

type runParams struct {
	Argv     []string          `json:"argv"`
	Cwd      string            `json:"cwd,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Name     string            `json:"name,omitempty"`
	Keep     bool              `json:"keep,omitempty"`
	Size     *Size             `json:"size,omitempty"`
	NoParent bool              `json:"no_parent"`
	NoNotify bool              `json:"no_notify"`
	Rm       bool              `json:"rm"`
}

// Run starts a program and returns its terminal once it runs.
func (c *Client) Run(ctx context.Context, req RunRequest) (Terminal, error) {
	if len(req.Argv) == 0 {
		return Terminal{}, fmt.Errorf("hc %s: argv is empty", MethodRun)
	}
	var out Terminal
	err := c.call(ctx, MethodRun, runParams{
		Argv: req.Argv, Cwd: req.Cwd, Env: req.Env, Labels: req.Labels, Name: req.Name,
		Keep: req.Keep, Size: req.Size, NoParent: true, NoNotify: true,
	}, &out)
	return out, err
}

// ListResult is core's answer to terminal.list. Machines are not read: V1
// rejects nonlocal targets. The 0.1.20 schema marks machines required, but
// the core leaves it out when no machine was added, so it is not checked.
type ListResult struct {
	Terminals      []Terminal `json:"terminals"`
	HiddenFinished uint       `json:"hidden_finished"`
}

func (l *ListResult) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "terminal.list", "terminals!", "hidden_finished!"); err != nil {
		return err
	}
	type plain ListResult
	return json.Unmarshal(b, (*plain)(l))
}

type listParams struct {
	All    bool              `json:"all,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// List returns the terminals carrying every given label; all includes
// finished ones.
func (c *Client) List(ctx context.Context, labels map[string]string, all bool) (ListResult, error) {
	var out ListResult
	err := c.call(ctx, MethodList, listParams{All: all, Labels: labels}, &out)
	return out, err
}

type terminalParams struct {
	Terminal string `json:"terminal"`
}

// Get returns one terminal's record, live or finished.
func (c *Client) Get(ctx context.Context, terminal string) (Terminal, error) {
	var out Terminal
	err := c.call(ctx, MethodGet, terminalParams{Terminal: terminal}, &out)
	return out, err
}

// ReadResult is a terminal's screen and history as text.
type ReadResult struct {
	Text     string `json:"text"`
	Complete bool   `json:"complete"`
	How      string `json:"how"` // screen, history or stitched
	Why      string `json:"why,omitempty"`
}

func (r *ReadResult) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "terminal.read", "text!", "complete!", "how!"); err != nil {
		return err
	}
	type plain ReadResult
	return json.Unmarshal(b, (*plain)(r))
}

type readParams struct {
	Terminal string `json:"terminal"`
	Lines    uint   `json:"lines,omitempty"`
}

// Read returns the screen plus lines of history.
func (c *Client) Read(ctx context.Context, terminal string, lines uint) (ReadResult, error) {
	var out ReadResult
	err := c.call(ctx, MethodRead, readParams{Terminal: terminal, Lines: lines}, &out)
	return out, err
}

// SendResult says how a send went in.
type SendResult struct {
	Turn    uint64 `json:"turn"`
	Status  string `json:"status,omitempty"`
	Started *bool  `json:"started,omitempty"`
}

func (s *SendResult) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "terminal.send", "turn!"); err != nil {
		return err
	}
	type plain SendResult
	return json.Unmarshal(b, (*plain)(s))
}

type sendParams struct {
	Terminal string `json:"terminal"`
	Text     string `json:"text"`
	If       string `json:"if"`
	Force    bool   `json:"force"`
}

// Send pastes text and presses Enter, only while the terminal's status is
// exactly ifStatus ("idle" or "working"), and never into a blocked program.
// It is a person's explicit action (spec §10); nothing automatic calls it.
func (c *Client) Send(ctx context.Context, terminal, text, ifStatus string) (SendResult, error) {
	if ifStatus != "idle" && ifStatus != "working" {
		return SendResult{}, fmt.Errorf("hc %s: if must be idle or working, not %q", MethodSend, ifStatus)
	}
	var out SendResult
	err := c.call(ctx, MethodSend, sendParams{Terminal: terminal, Text: text, If: ifStatus, Force: false}, &out)
	return out, err
}

// InterruptResult says which stop key was pressed and what was seen.
type InterruptResult struct {
	Key     string `json:"key"`     // esc or ctrl+c
	Outcome string `json:"outcome"` // stopped, exited, still_working or unknown
}

func (r *InterruptResult) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "terminal.interrupt", "key!", "outcome!"); err != nil {
		return err
	}
	type plain InterruptResult
	return json.Unmarshal(b, (*plain)(r))
}

type interruptParams struct {
	Terminal string   `json:"terminal"`
	Timeout  *float64 `json:"timeout,omitempty"`
}

// Interrupt presses the stop key once and watches for timeoutSeconds (core's
// default when zero). It never escalates.
func (c *Client) Interrupt(ctx context.Context, terminal string, timeoutSeconds float64) (InterruptResult, error) {
	p := interruptParams{Terminal: terminal}
	if timeoutSeconds > 0 {
		p.Timeout = &timeoutSeconds
	}
	var out InterruptResult
	err := c.call(ctx, MethodInterrupt, p, &out)
	return out, err
}

// Kill ends a terminal's program (hangup, then terminate, then kill) and
// returns the record core observed.
func (c *Client) Kill(ctx context.Context, terminal string) (Terminal, error) {
	var out Terminal
	err := c.call(ctx, MethodKill, terminalParams{Terminal: terminal}, &out)
	return out, err
}

type labelParams struct {
	Terminal string            `json:"terminal"`
	Labels   map[string]string `json:"labels"`
}

// Label sets labels; an empty value removes that label.
func (c *Client) Label(ctx context.Context, terminal string, labels map[string]string) (Terminal, error) {
	if len(labels) == 0 {
		return Terminal{}, fmt.Errorf("hc %s: no labels", MethodLabel)
	}
	var out Terminal
	err := c.call(ctx, MethodLabel, labelParams{Terminal: terminal, Labels: labels}, &out)
	return out, err
}

type keepParams struct {
	Terminal string `json:"terminal"`
	Keep     bool   `json:"keep"`
}

// Keep sets whether a live terminal gets a shell after its program is lost.
func (c *Client) Keep(ctx context.Context, terminal string, keep bool) (Terminal, error) {
	var out Terminal
	err := c.call(ctx, MethodKeep, keepParams{Terminal: terminal, Keep: keep}, &out)
	return out, err
}

// ExecRequest runs one program on this core's machine without a terminal.
type ExecRequest struct {
	Argv           []string
	Cwd            string
	Env            map[string]string
	Input          []byte  // stdin; nil closes it
	TimeoutSeconds float64 // core's default (60) when zero, at most 600
}

type execParams struct {
	Argv    []string          `json:"argv"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Input   []byte            `json:"input,omitempty"`
	Timeout *float64          `json:"timeout,omitempty"`
}

// ExecResult is what the program wrote and how it ended. A nonzero exit or a
// timeout is a result, not an error.
type ExecResult struct {
	Code      *int32 `json:"code,omitempty"`
	Signal    *int32 `json:"signal,omitempty"`
	Stdout    []byte `json:"stdout"`
	Stderr    []byte `json:"stderr"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
	MS        uint64 `json:"ms"`
}

func (r *ExecResult) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "machine.exec", "stdout!", "stderr!", "truncated!", "timed_out!", "ms!"); err != nil {
		return err
	}
	type plain ExecResult
	return json.Unmarshal(b, (*plain)(r))
}

// Exec runs argv as given (no shell) where core runs, so a check sees what
// core sees: its PATH and its privacy grants (spec §8, §16).
func (c *Client) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if len(req.Argv) == 0 {
		return ExecResult{}, fmt.Errorf("hc %s: argv is empty", MethodExec)
	}
	p := execParams{Argv: req.Argv, Cwd: req.Cwd, Env: req.Env, Input: req.Input}
	if req.TimeoutSeconds > 0 {
		t := req.TimeoutSeconds
		p.Timeout = &t
	}
	var out ExecResult
	err := c.call(ctx, MethodExec, p, &out)
	return out, err
}
