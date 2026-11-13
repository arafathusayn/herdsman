// Package oncore is the checkpointer of a herdr-core route, which the daemon
// runs as a service it owns (spec sections 10 and 13). It types nothing: the
// checkpoint prompt is a notice without its leading slash command, and no
// compact command is ever sent. Both qualified harnesses compact by themselves
// in the middle of a turn, so the checkpointer issues an early checkpoint below
// the harness's own compaction level, and raises needs_attention with the
// reason compaction_due only where a harness does not compact by itself or the
// user turned that off.
//
// The transcript is found from the execution's native conversation evidence,
// recorded with its provenance. With none, the checkpointer refuses to guess:
// it never takes the newest transcript of a folder for the agent's.
//
// A checkpoint belongs to one execution generation. A notice issued to a
// generation that was replaced since is stale: its late report is rejected and
// nothing is issued for the new generation on its account.
package oncore

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/checkpoint"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// Prompt is the checkpoint prompt on core: the memory prompt without its
// leading slash command, which a notice cannot carry (a control line is a
// person's action on core).
var Prompt = func() string {
	_, p, _ := strings.Cut(checkpoint.DefaultPrompt, " ")
	return p
}()

// Issuer names the checkpointer as a notice's issuer.
const Issuer = "checkpoint"

// DefaultEarlyMargin is how far below the harness's own compaction level the
// early checkpoint is issued, so that the memory is written before the harness
// compacts, when the agent comes back to its wait in time.
const DefaultEarlyMargin int64 = 20000

// Source is what the checkpointer reads and does on core.
type Source interface {
	// Current is the target agent's execution that Herdsman wants running
	// now, its latest generation.
	Current(ctx context.Context) (runtime.Execution, bool, error)
	// Issue issues a checkpoint notice to e once per key.
	Issue(ctx context.Context, key string, e runtime.Execution, text string) (notice.Notice, error)
	// ByKey reads the notice a key issued.
	ByKey(key string) (notice.Notice, bool, error)
	// Emit raises an event on the runtime topic.
	Emit(ev notice.Event)
}

// Config is one checkpointer's settings.
type Config struct {
	// Interval is the checkpoint interval, as INTERVAL on the TUI.
	Interval time.Duration
	// Limit is the context size at which a harness that does not compact by
	// itself needs a person's compact command (LIMIT on the TUI).
	Limit int64
	// Kind and Args are the target's harness and its launch command, for
	// the compaction level the launch sets.
	Kind string
	Args []string
	// Window is the context window (0: the adapter's).
	Window int64
	// EarlyMargin is how far below the harness's compaction level the early
	// checkpoint is issued (0: DefaultEarlyMargin).
	EarlyMargin int64
	// AutoCompactOff says the user turned the harness's own compaction off.
	AutoCompactOff bool
	// Home and CodexHome locate the harnesses' transcripts.
	Home, CodexHome string
	// Prompt replaces the checkpoint prompt; empty takes Prompt.
	Prompt string
}

// The outcomes of a pass.
const (
	PassNoExecution  = "no_execution"
	PassNoEvidence   = "no_evidence"
	PassNoTranscript = "no_transcript"
	PassPending      = "pending"
	PassIdle         = "idle"
	PassIssued       = "checkpoint_issued"
	PassEarly        = "early_checkpoint_issued"
	PassDue          = "compaction_due"
	PassStale        = "stale_report_rejected"
	PassDone         = "checkpoint_done"
)

// Pass is what one pass found and did.
type Pass struct {
	Outcomes []string
	// Tokens is the context read, when one was.
	Tokens int64
	// Detail says why, for a log line.
	Detail string
}

func (p *Pass) add(outcome, format string, a ...any) {
	p.Outcomes = append(p.Outcomes, outcome)
	if format != "" {
		line := fmt.Sprintf(format, a...)
		if p.Detail != "" {
			p.Detail += "; "
		}
		p.Detail += line
	}
}

// Has reports whether the pass had an outcome.
func (p Pass) Has(outcome string) bool {
	for _, o := range p.Outcomes {
		if o == outcome {
			return true
		}
	}
	return false
}

// pending is the checkpoint notice the checkpointer waits on, with the
// generation it was issued to.
type pending struct {
	key, exec string
	gen       int64
}

// Checkpointer checkpoints one agent of a core route.
type Checkpointer struct {
	Config Config
	Source Source
	Now    func() time.Time

	exec       string // the execution the state below belongs to
	gen        int64
	n          int       // checkpoints issued to this generation
	last       time.Time // when the last checkpoint was issued
	stamp      string    // the transcript's stamp at the last checkpoint
	earlyArmed bool      // the context is below the early level again
	due        bool      // compaction_due was raised and not cleared
	wait       *pending
}

func (c *Checkpointer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// reset starts the state of a new execution generation. A checkpoint still
// outstanding for the old one stays in wait, to be rejected as stale.
func (c *Checkpointer) reset(e runtime.Execution) {
	c.exec, c.gen, c.n, c.stamp, c.earlyArmed, c.due = e.ID, e.Generation, 0, "", true, false
	c.last = c.now()
}

// Transcript finds the transcript of an execution from its conversation
// evidence, and refuses to guess when there is none: no newest file of a
// folder stands in for the agent's own.
func Transcript(e runtime.Execution, home, codexHome string) (checkpoint.Choice, bool) {
	if e.Conversation == nil || strings.TrimSpace(e.Conversation.Value) == "" {
		return checkpoint.Choice{Why: "execution " + e.ID + " has no conversation evidence; the checkpointer does not guess its transcript"}, false
	}
	s := checkpoint.Session{Agent: e.HarnessKind, SID: e.Conversation.Value, Kind: "id"}
	if strings.HasSuffix(e.Conversation.Provenance, ":path") {
		s.Kind = "path"
	}
	ch := checkpoint.Choose(s, checkpoint.Env{Home: home, CodexHome: codexHome, PWD: e.Cwd})
	// A hint names another file than the evidence's: it is never read.
	ch.Hint = ""
	return ch, ch.File != ""
}

func stamp(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(st.Size(), 10) + "/" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// earlyLevel is the context size of the early checkpoint, and whether the
// harness has a known level.
func (c *Checkpointer) earlyLevel() (int64, bool) {
	a, err := core.AdapterFor(c.Config.Kind)
	if err != nil || c.Config.AutoCompactOff {
		return 0, false
	}
	level, ok := a.CompactionLevel(c.Config.Args, c.Config.Window)
	if !ok {
		return 0, false
	}
	margin := c.Config.EarlyMargin
	if margin <= 0 {
		margin = DefaultEarlyMargin
	}
	if level <= margin {
		return 0, false
	}
	return level - margin, true
}

// compactsBySelf says whether the target compacts with no person.
func (c *Checkpointer) compactsBySelf() bool {
	a, err := core.AdapterFor(c.Config.Kind)
	return err == nil && a.Facts.Compaction.BySelf && !c.Config.AutoCompactOff
}

// settle looks at the outstanding checkpoint. A report of a generation that
// was replaced is rejected as stale; one of the current generation is done.
// It reports whether a checkpoint is still outstanding for the current one.
func (c *Checkpointer) settle(e runtime.Execution, p *Pass) (bool, error) {
	if c.wait == nil {
		return false, nil
	}
	n, ok, err := c.Source.ByKey(c.wait.key)
	if err != nil {
		return false, err
	}
	current := c.wait.exec == e.ID && c.wait.gen == e.Generation
	if !ok {
		c.wait = nil
		return false, nil
	}
	switch n.State {
	case notice.StateIssued, notice.StateClaimed:
		if current {
			p.add(PassPending, "checkpoint %s is %s", n.ID, n.State)
			return true, nil
		}
		// The old generation's notice cannot be claimed by the new one;
		// nothing is waited on for it.
		p.add(PassStale, "checkpoint %s belongs to execution %s generation %d, which was replaced", n.ID, c.wait.exec, c.wait.gen)
		c.wait = nil
		return false, nil
	}
	if !current || n.Execution != e.ID || n.Generation != e.Generation {
		p.add(PassStale, "the late report of checkpoint %s from execution %s generation %d is rejected", n.ID, n.Execution, n.Generation)
		c.wait = nil
		return false, nil
	}
	if n.State == notice.StateHandled {
		p.add(PassDone, "checkpoint %s %s", n.ID, n.Outcome)
	}
	c.wait = nil
	return false, nil
}

// issue issues one checkpoint notice to the current generation.
func (c *Checkpointer) issue(ctx context.Context, e runtime.Execution, kind string) (notice.Notice, error) {
	c.n++
	key := fmt.Sprintf("checkpoint:%s:%d:%d", e.ID, e.Generation, c.n)
	if kind == PassEarly {
		key += ":early"
	}
	text := c.Config.Prompt
	if text == "" {
		text = Prompt
	}
	n, err := c.Source.Issue(ctx, key, e, text)
	if err != nil {
		c.n--
		return notice.Notice{}, err
	}
	c.wait = &pending{key: key, exec: e.ID, gen: e.Generation}
	c.last = c.now()
	return n, nil
}

// Step is one pass of the checkpointer. Nothing in it types into a terminal.
func (c *Checkpointer) Step(ctx context.Context) (Pass, error) {
	var p Pass
	e, ok, err := c.Source.Current(ctx)
	if err != nil {
		return p, err
	}
	if !ok {
		p.add(PassNoExecution, "the agent has no running execution")
		return p, nil
	}
	if e.ID != c.exec || e.Generation != c.gen {
		c.reset(e)
	}
	outstanding, err := c.settle(e, &p)
	if err != nil {
		return p, err
	}
	ch, ok := Transcript(e, c.Config.Home, c.Config.CodexHome)
	if !ok {
		reason := PassNoTranscript
		if e.Conversation == nil {
			reason = PassNoEvidence
		}
		p.add(reason, "%s", ch.Why)
		return p, nil
	}
	r := checkpoint.ContextTokens(ch.File)
	if r.OK {
		p.Tokens = r.Tokens
	}
	// compaction_due: a harness that does not compact by itself, or whose
	// compaction the user turned off, needs a person's compact command.
	if !c.compactsBySelf() && r.OK {
		switch {
		case r.Tokens >= c.Config.Limit && !c.due:
			c.due = true
			c.Source.Emit(notice.Event{Kind: notice.EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Reason: core.AttentionCompactionDue,
				Detail: fmt.Sprintf("the context is %d tokens, at or over the checkpointer's limit of %d, and the harness does not compact by itself; a person submits its compact command after the checkpoint", r.Tokens, c.Config.Limit)})
			p.add(PassDue, "context %d >= limit %d", r.Tokens, c.Config.Limit)
		case r.Tokens < c.Config.Limit:
			c.due = false
		}
	}
	level, known := c.earlyLevel()
	if known && r.OK && r.Tokens < level {
		// The harness compacted, or the context never got there: the next
		// crossing gets its early checkpoint.
		c.earlyArmed = true
	}
	if outstanding {
		return p, nil
	}
	if known && r.OK && r.Tokens >= level && c.earlyArmed {
		n, err := c.issue(ctx, e, PassEarly)
		if err != nil {
			return p, err
		}
		c.earlyArmed, c.stamp = false, stamp(ch.File)
		p.add(PassEarly, "context %d >= early level %d: checkpoint %s", r.Tokens, level, n.ID)
		return p, nil
	}
	st := stamp(ch.File)
	if c.now().Sub(c.last) < c.Config.Interval {
		return p, nil
	}
	if st == c.stamp {
		p.add(PassIdle, "no activity since the last checkpoint")
		c.last = c.now()
		return p, nil
	}
	n, err := c.issue(ctx, e, PassIssued)
	if err != nil {
		return p, err
	}
	c.stamp = st
	p.add(PassIssued, "checkpoint %s", n.ID)
	return p, nil
}
