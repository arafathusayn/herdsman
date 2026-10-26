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
	"github.com/arafathusayn/herdsman/internal/settings"
)

// DefaultPrompt is the memory checkpoint prompt. A Codex target gets it
// without its leading slash command, unless PROMPT is set.
const DefaultPrompt = "/memory-with-dag Capture all learnings as lessons in your local memory and if needed in kb. Keep index memory file lean, each entry should have timestamps and expand from index root using DAG like structure and knowledge graph. Do it now. Focus on reproduction of the understaning of the current state for this project because I'm going to compact your context."

// WorkingTimeout is how long a cycle waits for the prompted turn to start
// (`herdr agent wait --until working --timeout 30000`).
const WorkingTimeout = 30 * time.Second

// Config is the checkpointer's environment, read and checked once by
// FromEnv with the script's defaults.
type Config struct {
	Target       string        // TARGET
	Interval     time.Duration // INTERVAL; also the limit of each wait for an idle orchestrator
	Limit        int64         // LIMIT
	Log          string        // LOG
	Compact      string        // COMPACT
	Prompt       string        // PROMPT
	PromptSet    bool          // PROMPT was set and not empty
	Orchestrator string        // HERDSMAN_ORCHESTRATOR: adjutant mode when set
	Inbox        inbox.Options
	LockWait     int // LOCK_WAIT for the input lock
	Env          Env
}

// FromEnv reads the configuration from getenv with the script's defaults
// (empty values count as unset, as ${VAR:-default}) and checks every
// setting before anything runs with the shared parser: a bad one is a
// *settings.Error.
func FromEnv(getenv func(string) string, pwd string) (Config, error) {
	or := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	c := Config{
		Target:       getenv("TARGET"),
		Log:          or("LOG", or("TMPDIR", "/tmp")+"/checkpointer.log"),
		Compact:      or("COMPACT", "/compact"),
		Prompt:       or("PROMPT", DefaultPrompt),
		PromptSet:    getenv("PROMPT") != "",
		Orchestrator: getenv("HERDSMAN_ORCHESTRATOR"),
		Env:          Env{Transcript: getenv("TRANSCRIPT"), Home: getenv("HOME"), CodexHome: getenv("CODEX_HOME"), PWD: pwd},
	}
	if c.Target == "" {
		return c, settings.Missing("TARGET")
	}
	var err error
	if c.Interval, err = interval(getenv("INTERVAL")); err != nil {
		return c, err
	}
	if c.Limit, err = settings.Int("LIMIT", getenv("LIMIT"), 250000, 0, 1<<53); err != nil {
		return c, err
	}
	if c.Inbox, err = InboxSettings(getenv); err != nil {
		return c, err
	}
	c.LockWait, _ = strconv.Atoi(c.Inbox.LockWait)
	if c.Orchestrator != "" && c.Inbox.Dir == "" {
		return c, settings.Missing("HERDSMAN_INBOX")
	}
	return c, nil
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
	c.pause(context.Background(), d)
}

// pause sleeps d, or until ctx ends.
func (c *Checkpointer) pause(ctx context.Context, d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// seconds is the interval as the log lines show it: the effective number of
// seconds, after the suffix and the clamp.
func (c *Checkpointer) seconds() string {
	return strconv.FormatInt(int64(c.Config.Interval/time.Second), 10) + "s"
}

func (c *Checkpointer) wait(ctx context.Context, want []herdr.Status, timeout time.Duration) error {
	if c.Wait != nil {
		_, err := c.Wait(ctx, c.Config.Target, want, timeout)
		return err
	}
	_, err := c.Client.WaitAgentStatus(ctx, c.Config.Target, want, timeout)
	return err
}

// waitIdle waits for idle or done only (blocked means a permission dialog,
// and typing into it would answer it), at most one interval. A wait that
// fails or runs out is logged and ends the cycle: no input goes to an
// orchestrator that is not idle.
func (c *Checkpointer) waitIdle(ctx context.Context, then string) bool {
	err := c.wait(ctx, []herdr.Status{"idle", "done"}, c.Config.Interval)
	switch {
	case err == nil:
		return true
	case errors.Is(err, herdr.ErrWaitTimeout):
		c.log(fmt.Sprintf("orchestrator not idle within %s; %s", c.seconds(), then))
	default:
		c.log(fmt.Sprintf("agent wait failed: %v; %s", err, then))
	}
	return false
}

// waitWorking waits for the prompted turn to start. Its timeout is
// tolerated: a turn that starts and ends before the wait reads it is
// missed, and the idle wait after it still holds. Any other failure (herdr
// unreachable, a cancelled run) is logged and ends the cycle.
func (c *Checkpointer) waitWorking(ctx context.Context) bool {
	t := c.WorkingTimeout
	if t == 0 {
		t = WorkingTimeout
	}
	err := c.wait(ctx, []herdr.Status{"working"}, t)
	if err == nil || errors.Is(err, herdr.ErrWaitTimeout) {
		return true
	}
	c.log(fmt.Sprintf("agent wait failed: %v; no context read; retry next interval", err))
	return false
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

// session reads what herdr reports about the target. A failed read or a
// record without a string session id gives no session id, never a guessed
// one.
func (c *Checkpointer) session(ctx context.Context) Session {
	a, err := c.Client.AgentGet(ctx, c.Config.Target)
	if err != nil {
		return Session{Kind: "id", Missing: "agent get: " + err.Error()}
	}
	v, _ := inbox.Decode(a.Raw)
	m := inbox.Object(v)
	s := Session{Kind: "id"}
	s.Agent, _ = m["agent"].(string)
	sess := inbox.Object(m["agent_session"])
	if k, ok := sess["kind"].(string); ok && k != "" {
		s.Kind = k
	}
	s.SID, _ = sess["value"].(string)
	if s.SID == "" {
		s.Missing = "no agent_session value"
	}
	return s
}

// transcript makes the choice and records the harness for the prompt.
func (c *Checkpointer) transcript(ctx context.Context) Choice {
	s := c.session(ctx)
	c.agent = s.Agent
	return Choose(s, c.Config.Env)
}

// memoryPrompt is the prompt for the target: Codex gets the default
// without its leading slash command. Trailing newlines are dropped, as the
// script's "$(memory_prompt)" drops them.
func (c *Checkpointer) memoryPrompt() string {
	p := c.Config.Prompt
	if c.agent == "codex" && !c.Config.PromptSet {
		_, p, _ = strings.Cut(DefaultPrompt, " ")
	}
	return strings.TrimRight(p, "\n")
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
func (c *Checkpointer) inputLock() bool {
	if c.Config.Orchestrator == "" {
		return true
	}
	l := inbox.TryDir(filepath.Join(c.Config.Inbox.Dir, inbox.MutationLock), c.Config.LockWait, c.sleep)
	if l == nil {
		c.log("input lock busy, no input sent")
		return false
	}
	c.mu.Lock()
	c.lock = l
	c.mu.Unlock()
	return true
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
// it. It reports whether it sent the text.
func (c *Checkpointer) input(ctx context.Context, what, text, failed string) bool {
	if !c.inputLock() {
		return false
	}
	defer c.Release()
	if !c.guard(ctx) {
		return false
	}
	c.log("sending " + what)
	if err := c.Client.AgentPrompt(ctx, c.Config.Target, text); err != nil {
		c.log(failed)
		return false
	}
	return true
}

// reading reads the context of f and logs the records it passed over.
func (c *Checkpointer) reading(f string) Reading {
	r := ContextTokens(f)
	if r.Skipped > 0 {
		c.log(fmt.Sprintf("passed over %d unreadable records of %s", r.Skipped, f))
	}
	return r
}

// Cycle is one checkpoint on the transcript f: the memory prompt, then the
// compact command when the context is at or over the limit. It returns 0,
// or 1 when an input was not sent or a wait failed (retry next interval).
func (c *Checkpointer) Cycle(ctx context.Context, f string) int {
	const retry = "no input sent; retry next interval"
	if !c.waitIdle(ctx, retry) {
		return 1
	}
	if !c.input(ctx, "memory checkpoint", c.memoryPrompt(), "prompt failed; retry next interval") {
		return 1
	}
	if !c.waitWorking(ctx) || !c.waitIdle(ctx, "no context read; retry next interval") {
		return 1
	}
	r := c.reading(f)
	shown := r.String()
	if shown == "" {
		shown = "?"
	}
	c.log("memory checkpoint done; context=" + shown)
	if !r.OK || r.Tokens < c.Config.Limit {
		return 0
	}
	if !c.input(ctx, c.Config.Compact, c.Config.Compact, "compact failed; retry next interval") {
		return 1
	}
	if !c.waitWorking(ctx) || !c.waitIdle(ctx, "no context read; retry next interval") {
		return 1
	}
	c.log("compact done; context=" + c.reading(f).String())
	return 0
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
func (c *Checkpointer) Once(ctx context.Context) int {
	ch := c.transcript(ctx)
	if ch.File == "" {
		c.log("no transcript found")
		return 1
	}
	return c.Cycle(ctx, ch.File)
}

// stamp is a transcript's size and modification time, "" when stat fails:
// a transcript rewritten to the same size still counts as activity.
func stamp(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(st.Size(), 10) + "/" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// Loop runs a checkpoint every interval while the transcript changed since
// the last one, until ctx ends.
func (c *Checkpointer) Loop(ctx context.Context) error {
	last := ""
	c.log(fmt.Sprintf("checkpointer on %s: every %s, compact at >= %d tokens", c.Config.Target, c.seconds(), c.Config.Limit))
	for {
		c.pause(ctx, c.Config.Interval)
		if err := ctx.Err(); err != nil {
			return err
		}
		ch := c.transcript(ctx)
		if ch.File == "" {
			c.log("no transcript found; skip")
			continue
		}
		if stamp(ch.File) == last {
			c.log("no activity since last checkpoint; skip")
			continue
		}
		if c.Cycle(ctx, ch.File) != 0 {
			continue
		}
		last = stamp(ch.File)
	}
}
