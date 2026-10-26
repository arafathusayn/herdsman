package checkpoint

import (
	"context"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// GuardUsage is the guard's usage line.
const GuardUsage = "usage: herdsman checkpointer guard <orchestrator-name> <pane>"

// Guard is checkpointer-guard.sh: a read-only check, right before each
// input in adjutant mode, that the approved registry names pane as the
// orchestrator name and that its live session still matches. It returns
// the stdout line ("OK name pane") and 0, or the stderr text and 2.
func Guard(ctx context.Context, c *herdr.Client, o inbox.Options, args []string) (string, string, int) {
	fail := func(lines ...string) (string, string, int) {
		return "", strings.Join(lines, "\n") + "\n", 2
	}
	if len(args) != 2 {
		return fail(GuardUsage)
	}
	name, pane := args[0], args[1]
	if !inbox.NameOK(name) {
		return fail("invalid orchestrator name")
	}
	o.ReadOnly = true
	r, err := inbox.Open(o)
	if err != nil {
		msg, _ := inbox.IsFatal(err)
		return fail(msg)
	}
	if r.ProofStale {
		return fail("sandbox policy or protected installation proof stale; no input" + r.OldProofHint())
	}
	if _, ok := r.Registry(); !ok {
		return fail("registry change awaiting human approval; no input")
	}
	p, ok := r.Peer(name)
	if !ok {
		return fail("unknown orchestrator name; no input")
	}
	if p.Role != "orchestrator" {
		return fail("target is not the approved orchestrator; never type into the adjutant")
	}
	if p.Identity.Pane != pane {
		return fail("pane is not the approved orchestrator pane; no input")
	}
	observed, rc, note := inbox.LiveIdentity(ctx, c, pane)
	var pre []string
	if note != "" {
		pre = append(pre, note)
	}
	switch {
	case rc == inbox.LiveGone:
		return fail(append(pre, "orchestrator pane closed; no input")...)
	case rc != inbox.LiveOK:
		return fail(append(pre, "orchestrator identity unavailable; no input")...)
	case !observed.Same(p.Identity):
		return fail("orchestrator session changed; enroll a new generation before checkpoints")
	}
	return "OK " + name + " " + pane + "\n", "", 0
}
