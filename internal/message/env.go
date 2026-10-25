// Package message is the adjutant message channel's commands (`herdsman
// message ...`), ported from the skill's scripts/messages: the public
// grammar of message.sh (status, wait, read, release, ack, send, hold,
// unhold) and the commands it does not front (register, courier,
// card-watch, resume-goal, authority, leak-gate). Each keeps its script's
// arguments, environment variables and defaults, output lines, exit codes
// and file effects; the inbox rules live in internal/inbox.
//
// No command runs bash, jq, nc or herdr as a program: herdr is reached
// through its socket (internal/herdr), events through a subscription.
package message

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Env is one command run: its environment, output and herdr.
type Env struct {
	// Getenv reads the environment (os.Getenv in the binary).
	Getenv func(string) string
	Stdout io.Writer
	Stderr io.Writer
	Client *herdr.Client
	// Now and Sleep are the clock (nil: the real one).
	Now   func() time.Time
	Sleep func(time.Duration)
	// Events opens event subscriptions (nil: herdr's, through Client).
	Events Events
	// Executable is the running herdsman binary, for the binary setup proof.
	Executable string
	// JSON prints machine output (--json): one object per status or event.
	JSON bool
	// Ctx bounds herdr calls (nil: background).
	Ctx context.Context

	ch       *inbox.Channel
	mu       sync.Mutex // guards locks and drained: a signal handler releases them
	locks    []*inbox.Lock
	drained  bool        // releaseAll ran: a lock missing from locks is already free
	mutation *inbox.Lock // the held shared input lock, if any

	// Parsed common settings (commonSettings).
	settingsDone          bool
	healthAfter, lockWait string
	eventReadMax          int
}

func (e *Env) ctx() context.Context {
	if e.Ctx == nil {
		return context.Background()
	}
	return e.Ctx
}

func (e *Env) env(k string) string {
	if e.Getenv == nil {
		return os.Getenv(k)
	}
	return e.Getenv(k)
}

// envDefault is ${K:-def}.
func (e *Env) envDefault(k, def string) string {
	if v := e.env(k); v != "" {
		return v
	}
	return def
}

func (e *Env) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

func (e *Env) unix() int64 { return e.now().Unix() }

func (e *Env) sleep(d time.Duration) {
	if e.Sleep == nil {
		time.Sleep(d)
		return
	}
	e.Sleep(d)
}

// exitErr ends a command with a code; msg, when set, goes to stderr.
type exitErr struct {
	code int
	msg  string
}

func (x *exitErr) Error() string { return x.msg }

// die is the scripts' die: the message on stderr, exit 2.
func die(format string, a ...any) error {
	return &inbox.Fatal{Msg: fmt.Sprintf(format, a...)}
}

// exitWith ends the command with code and nothing more on stderr.
func exitWith(code int) error { return &exitErr{code: code} }

// finish turns a command's error into its exit code, printing a die
// message, and frees every lock the run still holds, as the scripts' exit
// trap does.
func (e *Env) finish(err error) int {
	e.releaseAll()
	if err == nil {
		return 0
	}
	if msg, ok := inbox.IsFatal(err); ok {
		fmt.Fprintln(e.Stderr, msg)
		return 2
	}
	if x, ok := errors.AsType[*exitErr](err); ok {
		if x.msg != "" {
			fmt.Fprintln(e.Stderr, x.msg)
		}
		return x.code
	}
	fmt.Fprintln(e.Stderr, err)
	return 2
}

func (e *Env) hold(l *inbox.Lock) *inbox.Lock {
	if l != nil {
		e.mu.Lock()
		if e.drained {
			// The signal handler already ran and the process is exiting:
			// do not leave this lock behind.
			_ = l.Release()
		} else {
			e.locks = append(e.locks, l)
		}
		e.mu.Unlock()
	}
	return l
}

// release frees one held lock.
func (e *Env) release(l *inbox.Lock) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l == e.mutation {
		e.mutation = nil
		if e.ch != nil {
			e.ch.MutationHeld = false
		}
	}
	for i, h := range e.locks {
		if h == l {
			e.locks = append(e.locks[:i], e.locks[i+1:]...)
			return l.Release()
		}
	}
	if e.drained {
		return nil // a signal already freed it; another process may own it now
	}
	return l.Release()
}

func (e *Env) releaseAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.mutation != nil && e.ch != nil {
		e.ch.MutationHeld = false
	}
	e.mutation = nil
	for i := len(e.locks) - 1; i >= 0; i-- {
		_ = e.locks[i].Release()
	}
	e.locks = nil
}

// Release frees every lock the run holds. A signal handler calls it from
// its own goroutine while the command runs: the lock list is guarded.
func (e *Env) Release() {
	e.releaseAll()
	e.mu.Lock()
	e.drained = true
	e.mu.Unlock()
}

// options are the inbox settings of the environment.
func (e *Env) options(readOnly bool) inbox.Options {
	return inbox.Options{
		Dir: e.env("HERDSMAN_INBOX"), Preflight: e.env("HERDSMAN_PREFLIGHT"), Installed: e.env("HERDSMAN_MESSAGES_DIR"),
		HealthAfter: e.healthAfter, LockWait: e.lockWait, ReadOnly: readOnly, Now: e.Now, Sleep: e.Sleep,
	}
}

// open is root_init. An unset HERDSMAN_INBOX ends the run with exit 1, as
// the scripts' ${HERDSMAN_INBOX:?} does under set -e.
func (e *Env) open(readOnly bool) error {
	if !e.settingsDone {
		if err := e.commonSettings(); err != nil {
			return err
		}
	}
	if e.env("HERDSMAN_INBOX") == "" {
		return &exitErr{code: 1, msg: "HERDSMAN_INBOX: set HERDSMAN_INBOX"}
	}
	ch, err := inbox.OpenChannel(e.options(readOnly), e.Executable)
	if err != nil {
		return err
	}
	e.ch = ch
	return nil
}

func (e *Env) path(names ...string) string { return e.ch.Path(names...) }

// pane is $HERDR_PANE_ID.
func (e *Env) pane() string { return e.env("HERDR_PANE_ID") }

// lockChannel is lock: the queue lock or die.
func (e *Env) lockChannel() (*inbox.Lock, error) {
	l := e.hold(e.ch.Lock(inbox.ChannelLock))
	if l == nil {
		return nil, die("channel lock busy; retry with backoff, never auto-remove")
	}
	return l, nil
}

// tryChannel is try_lock: the queue lock, or nil when busy.
func (e *Env) tryChannel() *inbox.Lock { return e.hold(e.ch.Lock(inbox.ChannelLock)) }

// tryMutation is mutation_lock: the shared input lock, or nil when busy.
func (e *Env) tryMutation() *inbox.Lock { return e.takeMutation(e.ch.LockWait) }

// takeMutation takes the shared input lock (tries attempts); as its new
// owner the process first publishes any waiting exposure (e93bcc1).
func (e *Env) takeMutation(tries int) *inbox.Lock {
	l, err := e.ch.LockMutation(tries, e.ch.Pause)
	if l == nil {
		return nil
	}
	e.hold(l)
	e.mu.Lock()
	e.mutation = l
	e.mu.Unlock()
	e.ch.MutationHeld = true
	if err != nil {
		fmt.Fprintln(e.Stderr, err)
	}
	return l
}

// errDisplayBusy is a display call that did not get the input lock.
var errDisplayBusy = errors.New("display lock busy")

// display is display_call: a display metadata report or a notification,
// under the shared input lock taken briefly (six tries).
func (e *Env) display(call func(ctx context.Context) error) error {
	l := e.takeMutation(6)
	if l == nil {
		return errDisplayBusy
	}
	err := call(e.ctx())
	if rerr := e.release(l); rerr != nil {
		return die("cannot release display owner")
	}
	return err
}

// reportMetadata is `pane report-metadata <pane> --source <source>` with a
// token set or cleared (value nil) and a time to live (0: none).
func (e *Env) reportMetadata(pane, source, token string, value *string, ttlMS int) error {
	return e.display(func(ctx context.Context) error {
		params := map[string]any{"pane_id": pane, "source": source, "tokens": map[string]*string{token: value}}
		if ttlMS > 0 {
			params["ttl_ms"] = ttlMS
		}
		return e.Client.Call(ctx, "pane.report_metadata", params, nil)
	})
}

// showNotification is `notification show <title> --body <body> --sound
// request`; true when herdr showed it.
func (e *Env) showNotification(title, body string) bool {
	shown := false
	err := e.display(func(ctx context.Context) error {
		var err error
		shown, err = e.Client.ShowNotification(ctx, herdr.Notification{Title: title, Body: body, Sound: "request"})
		return err
	})
	return err == nil && shown
}

// kv is a JSON object that keeps its key order, as jq builds one.
type kv []struct {
	K string
	V any
}

func obj(pairs ...any) kv {
	var o kv
	for i := 0; i+1 < len(pairs); i += 2 {
		o = append(o, struct {
			K string
			V any
		}{pairs[i].(string), pairs[i+1]})
	}
	return o
}

func (o kv) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := compact(p.K)
		b.Write(k)
		b.WriteByte(':')
		v, err := compact(p.V)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// compact is jq -c: one line, no HTML escapes, no newline.
func compact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// pretty is jq's default output: two-space indent, a final newline.
func pretty(v any) []byte {
	c, _ := compact(v)
	var b bytes.Buffer
	_ = json.Indent(&b, c, "", "  ")
	b.WriteByte('\n')
	return b.Bytes()
}

// line is jq -c output: one line with a newline.
func line(v any) []byte {
	c, _ := compact(v)
	return append(c, '\n')
}

// raw prints a decoded value as jq -r does.
func raw(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	c, _ := compact(v)
	return string(c)
}

// sortedCompact is jq -Sc: keys sorted at every depth, one line.
func sortedCompact(v any) string {
	c, _ := compact(v) // maps marshal with sorted keys
	return string(c)
}

// decodeFile reads and decodes a JSON file (no checks; callers check).
func decodeFile(p string) (any, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return inbox.Decode(b)
}

// toAny turns a value into decoded JSON (maps, json.Number), for Equal.
func toAny(v any) any {
	b, _ := json.Marshal(v)
	out, _ := inbox.Decode(b)
	return out
}
