// Package checkpoint is the checkpointer of the orchestrator pane, the port
// of checkpointer.sh and checkpointer-guard.sh: every interval, when the
// orchestrator did work since the last checkpoint, it waits until the
// orchestrator is idle, sends a memory checkpoint prompt, waits for that
// turn, then sends the compact command when the context is at or over the
// limit. In adjutant mode each input runs under the inbox's input lock and
// right after the guard's identity check.
package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/shnum"
)

// DefaultPrompt is the memory checkpoint prompt. A Codex target gets it
// without its leading slash command, unless PROMPT is set.
const DefaultPrompt = "/memory-with-dag Capture all learnings as lessons in your local memory and if needed in kb. Keep index memory file lean, each entry should have timestamps and expand from index root using DAG like structure and knowledge graph. Do it now. Focus on reproduction of the understaning of the current state for this project because I'm going to compact your context."

// WorkingTimeout is how long a cycle waits for the prompted turn to start
// (`herdr agent wait --until working --timeout 30000`).
const WorkingTimeout = 30 * time.Second

// Config is the checkpointer's environment, with the script's defaults
// applied by FromEnv.
type Config struct {
	Target       string // TARGET
	Interval     string // INTERVAL, as sleep reads it
	Limit        string // LIMIT, compared as test does
	Log          string // LOG
	Compact      string // COMPACT
	Prompt       string // PROMPT
	PromptSet    bool   // PROMPT was set and not empty
	Orchestrator string // HERDSMAN_ORCHESTRATOR: adjutant mode when set
	Inbox        inbox.Options
	LockWait     string // LOCK_WAIT for the input lock
	Env          Env
}

// ErrNoTarget is TARGET unset or empty.
var ErrNoTarget = errors.New("TARGET: set TARGET to the orchestrator pane id, e.g. w7:p1")

// ErrNoInbox is adjutant mode without HERDSMAN_INBOX: the script stops
// with status 1 when it takes the input lock.
var ErrNoInbox = errors.New("HERDSMAN_INBOX: set HERDSMAN_INBOX in adjutant mode")

// FromEnv reads the configuration from getenv with the script's defaults.
// Empty values count as unset, as ${VAR:-default} does.
func FromEnv(getenv func(string) string, pwd string) (Config, error) {
	or := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	c := Config{
		Target:       getenv("TARGET"),
		Interval:     or("INTERVAL", "900"),
		Limit:        or("LIMIT", "250000"),
		Log:          or("LOG", or("TMPDIR", "/tmp")+"/checkpointer.log"),
		Compact:      or("COMPACT", "/compact"),
		Prompt:       or("PROMPT", DefaultPrompt),
		PromptSet:    getenv("PROMPT") != "",
		Orchestrator: getenv("HERDSMAN_ORCHESTRATOR"),
		LockWait:     or("LOCK_WAIT", "6"),
		Inbox: inbox.Options{
			Dir:         getenv("HERDSMAN_INBOX"),
			Preflight:   getenv("HERDSMAN_PREFLIGHT"),
			Installed:   getenv("HERDSMAN_MESSAGES_DIR"),
			HealthAfter: getenv("HEALTH_AFTER"),
			LockWait:    getenv("LOCK_WAIT"),
		},
		Env: Env{Transcript: getenv("TRANSCRIPT"), Home: getenv("HOME"), CodexHome: getenv("CODEX_HOME"), PWD: pwd},
	}
	if c.Target == "" {
		return c, ErrNoTarget
	}
	return c, nil
}

// Interval reads INTERVAL as sleep does: a non-negative number of seconds,
// with an optional fraction and an optional s, m, h or d suffix.
func Interval(s string) (time.Duration, bool) {
	mult := time.Second
	if s != "" {
		switch s[len(s)-1] {
		case 's':
			s = s[:len(s)-1]
		case 'm':
			mult, s = time.Minute, s[:len(s)-1]
		case 'h':
			mult, s = time.Hour, s[:len(s)-1]
		case 'd':
			mult, s = 24*time.Hour, s[:len(s)-1]
		}
	}
	if s == "" || strings.ContainsAny(s, "+-eEnNiIxXpP_") {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f*float64(mult) > float64(1<<62) {
		return 0, false
	}
	return time.Duration(f * float64(mult)), true
}

// Checkpointer runs checkpoints on one target.
type Checkpointer struct {
	Config Config
	Client *herdr.Client
	Stdout io.Writer
	Stderr io.Writer

	// Now, Sleep and Wait are the clock, the pause and the agent status
	// wait; nil takes time.Now, time.Sleep and Client.WaitAgentStatus.
	Now   func() time.Time
	Sleep func(time.Duration)
	Wait  func(ctx context.Context, target string, want []herdr.Status, timeout time.Duration) (herdr.Status, error)
	// WorkingTimeout bounds the wait for the prompted turn (0: 30 s).
	WorkingTimeout time.Duration

	agent string // the harness herdr reports for the target

	mu   sync.Mutex
	lock *inbox.Lock
}

func (c *Checkpointer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checkpointer) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (c *Checkpointer) wait(ctx context.Context, want []herdr.Status, timeout time.Duration) {
	if c.Wait != nil {
		_, _ = c.Wait(ctx, c.Config.Target, want, timeout)
		return
	}
	_, _ = c.Client.WaitAgentStatus(ctx, c.Config.Target, want, timeout)
}

// waitIdle waits for idle or done only: blocked means a permission dialog,
// and typing into it would answer it.
func (c *Checkpointer) waitIdle(ctx context.Context) {
	c.wait(ctx, []herdr.Status{"idle", "done"}, 0)
}

func (c *Checkpointer) waitWorking(ctx context.Context) {
	t := c.WorkingTimeout
	if t == 0 {
		t = WorkingTimeout
	}
	c.wait(ctx, []herdr.Status{"working"}, t)
}

// log writes "HH:MM:SS msg" to stdout and appends it to the log file, as
// the script's echo | tee -a.
func (c *Checkpointer) log(msg string) {
	line := c.now().Format("15:04:05") + " " + msg + "\n"
	_, _ = io.WriteString(c.Stdout, line)
	f, err := os.OpenFile(c.Config.Log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err == nil {
		_, err = f.WriteString(line)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintf(c.Stderr, "herdsman checkpointer: log: %v\n", err)
	}
}

// session reads what herdr reports about the target.
func (c *Checkpointer) session(ctx context.Context) Session {
	a, err := c.Client.AgentGet(ctx, c.Config.Target)
	if err != nil {
		// The herdr CLI prints an error answer as JSON on stdout, where
		// the script's jq reads a null session value; with no answer at
		// all it reads nothing.
		if _, ok := errors.AsType[*herdr.Error](err); ok {
			return Session{SID: "null", Kind: "id"}
		}
		return Session{Kind: "id"}
	}
	v, _ := inbox.Decode(a.Raw)
	m := inbox.Object(v)
	s := Session{Agent: jqField(m["agent"], ""), Kind: "id"}
	sess, isObj := m["agent_session"].(map[string]any)
	if !isObj {
		// A null session reads as null; any other value is a jq error,
		// which prints nothing.
		if m["agent_session"] == nil {
			s.SID = "null"
		}
		return s
	}
	s.SID = jqField(sess["value"], "null")
	s.Kind = jqField(sess["kind"], "id")
	return s
}

// jqField is a field as jq -r prints it, with def for null (and false,
// for the script's `// default`; a missing value field prints null).
func jqField(v any, def string) string {
	if v == nil || (v == false && def != "null") {
		return def
	}
	return jqText(v)
}

// transcript makes the choice and records the harness for the prompt.
func (c *Checkpointer) transcript(ctx context.Context) Choice {
	s := c.session(ctx)
	c.agent = s.Agent
	return Choose(s, c.Config.Env)
}

// memoryPrompt is the prompt for the target: Codex gets the default
// without its leading slash command.
func (c *Checkpointer) memoryPrompt() string {
	if c.agent == "codex" && !c.Config.PromptSet {
		_, rest, _ := strings.Cut(DefaultPrompt, " ")
		return rest
	}
	return c.Config.Prompt
}

// guard runs the identity check in adjutant mode; ordinary mode has none.
func (c *Checkpointer) guard(ctx context.Context) bool {
	if c.Config.Orchestrator == "" {
		return true
	}
	_, errText, code := Guard(ctx, c.Client, c.Config.Inbox, []string{c.Config.Orchestrator, c.Config.Target})
	if code == 0 {
		return true
	}
	c.log("identity check failed, no input sent: " + strings.TrimRight(errText, "\n"))
	return false
}

// inputLock takes the inbox's input lock in adjutant mode, with LOCK_WAIT
// attempts a second apart. A busy lock sends nothing and is never removed.
func (c *Checkpointer) inputLock() (bool, error) {
	if c.Config.Orchestrator == "" {
		return true, nil
	}
	if c.Config.Inbox.Dir == "" {
		return false, ErrNoInbox
	}
	// [ "$tries" -lt "$LOCK_WAIT" ]: a value test refuses allows one try.
	tries := 1
	if n, ok := shnum.Test(c.Config.LockWait); ok && n > 1 {
		tries = int(min(n, 1<<30))
	}
	l := inbox.TryDir(filepath.Join(c.Config.Inbox.Dir, inbox.MutationLock), tries, c.sleep)
	if l == nil {
		c.log("input lock busy, no input sent")
		return false, nil
	}
	c.mu.Lock()
	c.lock = l
	c.mu.Unlock()
	return true, nil
}

// Release frees the input lock when this checkpointer holds it (the
// script's EXIT trap).
func (c *Checkpointer) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lock != nil {
		_ = c.lock.Release()
		c.lock = nil
	}
}

// input takes the lock, checks the identity, logs what it sends and sends
// it. It reports whether it tried to send, the prompt's error, and
// ErrNoInbox.
func (c *Checkpointer) input(ctx context.Context, what, text string) (sent bool, promptErr, fatal error) {
	ok, err := c.inputLock()
	if err != nil || !ok {
		return false, nil, err
	}
	defer c.Release()
	if !c.guard(ctx) {
		return false, nil, nil
	}
	c.log("sending " + what)
	return true, c.Client.AgentPrompt(ctx, c.Config.Target, text), nil
}

// Cycle is one checkpoint on the transcript f: the memory prompt, then the
// compact command when the context is at or over the limit. It returns
// the script's status (0 or 1) or ErrNoInbox.
func (c *Checkpointer) Cycle(ctx context.Context, f string) (int, error) {
	c.waitIdle(ctx)
	sent, perr, err := c.input(ctx, "memory checkpoint", c.memoryPrompt())
	if err != nil || !sent {
		return 1, err
	}
	if perr != nil {
		c.log("prompt failed; retry next interval")
		return 1, nil
	}
	c.waitWorking(ctx)
	c.waitIdle(ctx)
	tokens := ContextTokens(f)
	shown := tokens
	if shown == "" {
		shown = "?"
	}
	c.log("memory checkpoint done; context=" + shown)
	if tokens == "" || !atLeast(tokens, c.Config.Limit) {
		return 0, nil
	}
	sent, _, err = c.input(ctx, c.Config.Compact, c.Config.Compact)
	if err != nil || !sent {
		return 1, err
	}
	c.waitWorking(ctx)
	c.waitIdle(ctx)
	c.log("compact done; context=" + ContextTokens(f))
	return 0, nil
}

// atLeast is [ "$a" -ge "$b" ]: false when test refuses either value.
func atLeast(a, b string) bool {
	x, ok1 := shnum.Test(a)
	y, ok2 := shnum.Test(b)
	return ok1 && ok2 && x >= y
}

// Probe prints which transcript the checkpointer reads and why, a hint
// when a newer one exists, and its context. It sends nothing.
func (c *Checkpointer) Probe(ctx context.Context) int {
	ch := c.transcript(ctx)
	fmt.Fprintf(c.Stdout, "transcript: %s\n", ch.Why)
	if ch.Hint != "" {
		fmt.Fprintf(c.Stdout, "hint: %s\n", ch.Hint)
	}
	if ch.File != "" {
		fmt.Fprintf(c.Stdout, "%s context=%s\n", ch.File, ContextTokens(ch.File))
	}
	return 0
}

// Once runs one cycle now: 1 when there is no transcript.
func (c *Checkpointer) Once(ctx context.Context) (int, error) {
	ch := c.transcript(ctx)
	if ch.File == "" {
		c.log("no transcript found")
		return 1, nil
	}
	return c.Cycle(ctx, ch.File)
}

// fileSize is the script's file_size: the size as text, "" when stat fails.
func fileSize(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(st.Size(), 10)
}

// Loop runs a checkpoint every interval while the transcript grew since the
// last one, until ctx ends or a cycle stops with ErrNoInbox.
func (c *Checkpointer) Loop(ctx context.Context, interval time.Duration) error {
	last := "0"
	c.log(fmt.Sprintf("checkpointer on %s: every %ss, compact at >= %s tokens", c.Config.Target, c.Config.Interval, c.Config.Limit))
	for {
		c.sleep(interval)
		if err := ctx.Err(); err != nil {
			return err
		}
		ch := c.transcript(ctx)
		if ch.File == "" {
			c.log("no transcript found; skip")
			continue
		}
		if fileSize(ch.File) == last {
			c.log("no activity since last checkpoint; skip")
			continue
		}
		code, err := c.Cycle(ctx, ch.File)
		if err != nil {
			return err
		}
		if code != 0 {
			continue
		}
		last = fileSize(ch.File)
	}
}
