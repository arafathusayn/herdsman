// Package gate is the executor adapter for relayed gos (`herdsman
// approval-gate`, from the skill's approval-gate.sh). It performs no effect
// itself: the owning executor acts only after CONSUME.
//
//	consume <go-file> <action> <target> <revision> <draft-file|->   check every binding, then record consumption
//	complete <request-id> <outcome>                                   record the outcome after the effect
//
// Private state under <inbox>/approvals: interactions/<ref>.json (the
// adjutant's recorded ask-tool answers), cancels/<request-id>.json,
// consumed/<request-id>.json and action.lock. Exit 0: CONSUME or
// completed; 1: refused, a new go is needed; 2: error or busy lock; 3:
// HUMAN boundary; 4: DUPLICATE, the recorded outcome stands; 5: RECONCILE, a
// consumption has no outcome: inspect external state first.
package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Exit codes.
const (
	OK        = 0
	Refused   = 1
	Error     = 2
	Human     = 3
	Duplicate = 4
	Reconcile = 5
)

const (
	consumeUsage  = "usage: herdsman approval-gate consume <go-file> <action> <target> <revision> <draft-file|->"
	completeUsage = "usage: herdsman approval-gate complete <request-id> <outcome>"
	usage         = "usage: herdsman approval-gate consume <go-file> <action> <target> <revision> <draft-file|-> | complete <request-id> <outcome>"
)

var refRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Gate is one run of the gate.
type Gate struct {
	Options inbox.Options
	Client  *herdr.Client
	Stdout  io.Writer
	Stderr  io.Writer

	root    *inbox.Root
	ap      string
	ctx     context.Context
	mu      sync.Mutex // guards locks, stopped and every state write
	locks   []*inbox.Lock
	stopped int // the exit code of a stop (128+signal), 0 while running

	beforeWrite func(target string) // a test pauses here, before a write
}

// errStopped is the write a stopped gate refuses.
var errStopped = errors.New("the gate was stopped")

// Stop ends the run at its next write with the exit code given (128+signal),
// as the scripts' traps end on a signal. It never frees a lock: the running
// gate does that itself when Run returns, so no write can follow a freed
// lock. The caller also ends the run's context, so a Herdr call or a lock
// wait ends at once.
func (g *Gate) Stop(code int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped == 0 {
		g.stopped = code
	}
}

func (g *Gate) halted() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped
}

// quiet drops a stopped gate's lines: after an interrupt it prints nothing
// that could read as a result.
type quiet struct {
	g *Gate
	w io.Writer
}

func (q quiet) Write(p []byte) (int, error) {
	if q.g.halted() != 0 {
		return len(p), nil
	}
	return q.w.Write(p)
}

func (g *Gate) hold(l *inbox.Lock) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.locks = append(g.locks, l)
}

// exit is how a step ends the run: a line on stdout or stderr and a code.
type exit struct{ code int }

func (g *Gate) refuse(msg string) exit {
	fmt.Fprintf(g.Stdout, "REFUSED %s\n", msg)
	return exit{Refused}
}

func (g *Gate) die(msg string) exit {
	fmt.Fprintln(g.Stderr, msg)
	return exit{Error}
}

// Release frees every lock the run holds, as the script's exit trap does.
// Run calls it when the run ends.
func (g *Gate) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := len(g.locks) - 1; i >= 0; i-- {
		_ = g.locks[i].Release()
	}
	g.locks = nil
}

// Run runs the gate with the command's arguments and returns the exit code.
func (g *Gate) Run(ctx context.Context, args []string) int {
	g.ctx = ctx
	g.Stdout, g.Stderr = quiet{g, g.Stdout}, quiet{g, g.Stderr}
	defer g.Release()
	code := g.run(args).code
	if s := g.halted(); s != 0 {
		return s
	}
	return code
}

func (g *Gate) run(args []string) exit {
	o := g.Options
	o.ReadOnly = true
	r, err := inbox.Open(o)
	if err != nil {
		msg, _ := inbox.IsFatal(err)
		return g.die(msg)
	}
	if r.ProofStale {
		return g.die("sandbox policy or protected installation proof stale; repeat human setup")
	}
	g.root = r
	g.ap = r.Path("approvals")
	for _, d := range []string{g.ap, filepath.Join(g.ap, "interactions"), filepath.Join(g.ap, "cancels"), filepath.Join(g.ap, "consumed")} {
		if st, err := os.Lstat(d); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return g.die("symlink approval state: " + d)
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return g.die("cannot create " + d)
		}
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "consume":
		if len(args) != 6 {
			return g.die(consumeUsage)
		}
		return g.consume(args[1], args[2], args[3], args[4], args[5])
	case "complete":
		if len(args) != 3 {
			return g.die(completeUsage)
		}
		return g.complete(args[1], args[2])
	}
	return g.die(usage)
}

// lockAction takes the gate's own lock, once.
func (g *Gate) lockAction() *exit {
	l := inbox.TryDir(filepath.Join(g.ap, inbox.ActionLock), 1, nil)
	if l == nil {
		e := g.die("action lock busy; retry later, never auto-remove")
		return &e
	}
	g.hold(l)
	return nil
}

// goFields are a go's required string fields.
var goFields = []string{"question_id", "request_id", "action", "target", "revision", "draft_hash", "text", "human_interaction"}

func (g *Gate) consume(goFile, action, target, revision, draft string) exit {
	root := g.root.Dir
	b, ok := inbox.ReadStrict(root, goFile)
	if !ok {
		return g.refuse("go is not strict JSON")
	}
	v, _ := inbox.Decode(b)
	gm := inbox.Object(v)
	ver, vok := inbox.Int(gm, "version")
	expires, eok := inbox.Int(gm, "expires_at")
	fieldsOK := gm != nil && vok && ver == 1 && gm["type"] == "user_go" && eok
	for _, k := range goFields {
		if !inbox.NonEmpty(gm, k) {
			fieldsOK = false
		}
	}
	if !fieldsOK {
		return g.refuse("go fields missing or mistyped")
	}
	field := func(k string) string { s, _ := inbox.String(gm, k); return s }
	req, ref := field("request_id"), field("human_interaction")
	if !refRe.MatchString(req) {
		return g.refuse("invalid request id")
	}
	if !refRe.MatchString(ref) {
		return g.refuse("invalid interaction reference")
	}
	if field("action") != action {
		return g.refuse("action differs from the go")
	}
	if field("target") != target {
		return g.refuse("target differs from the go")
	}
	if field("revision") != revision {
		return g.refuse("revision differs from the go")
	}
	hash := "none"
	if draft != "-" {
		if !inbox.Regular(root, draft) {
			return g.refuse("draft unavailable")
		}
		sum, err := inbox.DigestFile(draft)
		if err != nil {
			return g.refuse("draft unavailable")
		}
		hash = sum
	}
	if field("draft_hash") != hash {
		return g.refuse("draft differs from the reviewed draft")
	}
	if expires <= g.root.Now().Unix() {
		return g.refuse("go expired")
	}
	ib, ok := inbox.ReadStrict(root, filepath.Join(g.ap, "interactions", ref+".json"))
	if !ok {
		return g.refuse("no trusted interaction record for this go")
	}
	iv, _ := inbox.Decode(ib)
	im := inbox.Object(iv)
	same := func(k string) bool { return im != nil && inbox.Equal(im[k], gm[k]) }
	if im == nil || im["source"] != "ask-tool" || !same("question_id") || !same("request_id") || !same("revision") {
		return g.refuse("trusted interaction does not match this go")
	}
	// Correlation alone is not consent: the recorded answer must approve exactly this action, target and draft.
	if im["approved"] != true {
		return g.refuse("trusted interaction is not an affirmative answer")
	}
	if !same("action") || !same("target") || !same("draft_hash") {
		return g.refuse("trusted interaction approves another action, target or draft")
	}
	if e := g.lockAction(); e != nil {
		return *e
	}
	// Admission and exposure writers use the shared input lock. Check the
	// boundary under it and record the consumption before releasing it, so
	// no admission falls between the check and the consumption.
	mutation := g.root.Lock(inbox.MutationLock)
	if mutation == nil {
		return g.die("input lock busy; retry later, never auto-remove")
	}
	g.hold(mutation)
	// One check in this process, the lock owner (a0a1517): an agent that
	// this observation saw is recorded from the same observation, so a
	// retry after that agent exits stays held until human setup (A26).
	if ok, fault, unknown := g.root.BoundaryOK(g.ctx, g.Client); !ok {
		if err := g.recordExposure(fault, unknown); err != nil {
			return g.die("cannot record the exposure: " + err.Error())
		}
		fmt.Fprintln(g.Stdout, inbox.HumanLine)
		return exit{Human}
	}
	// The go can expire while the gate waits for the lock or the authority check: check it again right here.
	if expires <= g.root.Now().Unix() {
		return g.refuse("go expired")
	}
	cancel := filepath.Join(g.ap, "cancels", req+".json")
	if err := inbox.SafeLeaf(root, cancel); err != nil {
		return g.die(err.Error())
	}
	if inbox.Exists(cancel) {
		return g.refuse("request cancelled")
	}
	rec := filepath.Join(g.ap, "consumed", req+".json")
	if inbox.Exists(rec) {
		rb, ok := inbox.ReadStrict(root, rec)
		if !ok {
			return g.die("consumed record unreadable; inspect it")
		}
		rv, _ := inbox.Decode(rb)
		rm := inbox.Object(rv)
		if rm == nil || rm["action"] != action || rm["target"] != target || rm["revision"] != revision || rm["draft_hash"] != hash {
			return g.refuse("request already consumed with another binding; a changed draft or target needs a new go")
		}
		if rm["state"] == "done" {
			fmt.Fprintf(g.Stdout, "DUPLICATE %s outcome=%s\n", req, jqRaw(rm["outcome"]))
			return exit{Duplicate}
		}
		fmt.Fprintf(g.Stdout, "RECONCILE %s consumed without an outcome: inspect external state before any retry\n", req)
		return exit{Reconcile}
	}
	record := consumedRecord{RequestID: req, Action: action, Target: target, Revision: revision, DraftHash: hash, State: "consuming", Outcome: "", At: g.root.Now().Unix()}
	out, err := pretty(record)
	if err != nil || g.atomic(rec, out) != nil {
		return g.die("cannot record consumption")
	}
	g.mu.Lock()
	_ = mutation.Release()
	g.locks = g.locks[:len(g.locks)-1]
	g.mu.Unlock()
	fmt.Fprintf(g.Stdout, "CONSUME %s: act once, then run complete\n", req)
	return exit{OK}
}

// recordExposure writes state/boundary-tainted once when the refusing
// check failed on a live agent outside the protected setup
// (FaultInventory), from that check's own result, in the scripts' format:
// reason, agents, time. The caller holds the input lock. Any other fault,
// or a record already there, writes nothing.
func (g *Gate) recordExposure(fault string, unknown []inbox.Unknown) error {
	target := g.root.Path("state", "boundary-tainted")
	if fault != inbox.FaultInventory || inbox.Exists(target) {
		return nil
	}
	if unknown == nil {
		unknown = []inbox.Unknown{}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		Reason string          `json:"reason"`
		Agents []inbox.Unknown `json:"agents"`
		Time   int64           `json:"time"`
	}{"live agent outside the protected setup", unknown, g.root.Now().Unix()})
	return g.atomic(target, b.Bytes())
}

func (g *Gate) complete(req, outcome string) exit {
	if !refRe.MatchString(req) {
		return g.die("invalid request id")
	}
	if !refRe.MatchString(outcome) {
		return g.die("invalid outcome")
	}
	if e := g.lockAction(); e != nil {
		return *e
	}
	rec := filepath.Join(g.ap, "consumed", req+".json")
	b, ok := inbox.ReadStrict(g.root.Dir, rec)
	if !ok {
		return g.die("no consumption recorded for this request")
	}
	v, _ := inbox.Decode(b)
	if inbox.Object(v)["state"] != "consuming" {
		return g.die("outcome already recorded")
	}
	out, err := setFields(b, map[string]string{"state": "done", "outcome": outcome})
	if err != nil || g.atomic(rec, append(out, '\n')) != nil {
		return g.die("cannot record outcome")
	}
	fmt.Fprintf(g.Stdout, "COMPLETED %s outcome=%s\n", req, outcome)
	return exit{OK}
}

type consumedRecord struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Revision  string `json:"revision"`
	DraftHash string `json:"draft_hash"`
	State     string `json:"state"`
	Outcome   string `json:"outcome"`
	At        int64  `json:"at"`
}

// pretty prints JSON as jq does by default: two-space indent, no HTML escapes.
func pretty(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(v)
	return b.Bytes(), err
}

// setFields sets string fields of a JSON object, keeping its key order, and
// prints it compactly, as `jq -c '.k=v'` does.
func setFields(b []byte, set map[string]string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}
	var out bytes.Buffer
	out.WriteByte('{')
	done := map[string]bool{}
	first := true
	put := func(k string, raw []byte) {
		if !first {
			out.WriteByte(',')
		}
		first = false
		kb, _ := marshal(k)
		out.Write(kb)
		out.WriteByte(':')
		out.Write(raw)
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if v, ok := set[k]; ok {
			vb, _ := marshal(v)
			put(k, vb)
			done[k] = true
			continue
		}
		var c bytes.Buffer
		_ = json.Compact(&c, raw) // raw was just decoded: it compacts
		put(k, c.Bytes())
	}
	for _, k := range []string{"state", "outcome"} {
		if v, ok := set[k]; ok && !done[k] {
			vb, _ := marshal(v)
			put(k, vb)
		}
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n"), err
}

// jqRaw prints a value as `jq -r` does.
func jqRaw(v any) string {
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
	b, _ := marshal(v)
	return string(b)
}

// atomic writes a state file as the scripts' atomic does: a temporary file
// under state/, checked as strict JSON for a .json target, then renamed.
func (g *Gate) atomic(target string, data []byte) error {
	if g.beforeWrite != nil {
		g.beforeWrite(target)
	}
	// A write and a stop exclude each other: once Stop returns, no write
	// starts, and a write under way ends before Stop takes effect.
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped != 0 {
		return errStopped
	}
	if err := inbox.SafeLeaf(g.root.Dir, target); err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty")
	}
	if filepath.Ext(target) == ".json" && inbox.StrictJSON(data) != nil {
		return fmt.Errorf("not strict JSON")
	}
	tmp, err := os.CreateTemp(g.root.Path("state"), ".write.")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return err
	}
	if !inbox.Regular(g.root.Dir, target) {
		return fmt.Errorf("not regular after the write")
	}
	return nil
}
