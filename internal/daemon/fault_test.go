package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

var errInjected = errors.New("injected filesystem failure")

// faults counts the filesystem steps of an operation (journal writes,
// fsyncs and renames) and fails or crashes at one of them.
type faults struct {
	n, at int
	crash bool
	log   []string
}

func (f *faults) step(what string) error {
	f.n++
	f.log = append(f.log, what)
	if f.n == f.at {
		if f.crash {
			panic(crashSignal{what})
		}
		return errInjected
	}
	return nil
}

// inject routes every journal write, fsync and rename through f until
// the returned restore runs.
func inject(f *faults) (restore func()) {
	oldSync, oldRename, oldWrite := fsync, rename, journalWrite
	fsync = func(fl *os.File) error {
		if err := f.step("fsync " + fl.Name()); err != nil {
			return err
		}
		return oldSync(fl)
	}
	rename = func(a, b string) error {
		if err := f.step("rename " + b); err != nil {
			return err
		}
		return oldRename(a, b)
	}
	journalWrite = func(fl *os.File, b []byte) (int, error) {
		if err := f.step("write " + fl.Name()); err != nil {
			return 0, err
		}
		return oldWrite(fl, b)
	}
	return func() { fsync, rename, journalWrite = oldSync, oldRename, oldWrite }
}

// op runs fn as the actor runs an operation.
func (x *direct) op(fn func() *APIError) *APIError { return x.r.runOp(fn) }

func mdCount(dir string) int {
	m, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	return len(m)
}

// faultCase is one operation of the fault matrix: setup brings the route
// to the operation, run performs it (and is retried with the same keys),
// check asserts the files after a successful retry.
type faultCase struct {
	name  string
	setup func(x *direct) *faultState
	run   func(x *direct, s *faultState) *APIError
	check func(t *testing.T, x *direct, s *faultState)
}

type faultState struct {
	key, readKey, token, id string
}

var faultCases = []faultCase{
	{
		name:  "send",
		setup: func(x *direct) *faultState { return &faultState{key: key()} },
		run: func(x *direct, s *faultState) *APIError {
			_, e := x.send("one", s.key)
			return e
		},
		check: func(t *testing.T, x *direct, s *faultState) {
			n := 0
			for _, r := range x.journal() {
				if r.Op == "send" && r.Key == s.key {
					n++
				}
			}
			if n != 1 || mdCount(x.path("orch", "ready")) != 1 {
				t.Fatalf("send records %d, ready %d", n, mdCount(x.path("orch", "ready")))
			}
		},
	},
	{
		name: "claim",
		setup: func(x *direct) *faultState {
			m, _ := x.send("one", key())
			return &faultState{readKey: key(), id: m.ID}
		},
		run: func(x *direct, s *faultState) *APIError {
			cl, e := x.r.claim("orch", ClaimRequest{ReadKey: s.readKey})
			if e == nil && (len(cl.Manifest) != 1 || cl.Manifest[0].ID != s.id) {
				return Errf("internal", "manifest %+v", cl.Manifest)
			}
			return e
		},
		check: func(t *testing.T, x *direct, s *faultState) {
			if mdCount(x.path("orch", "ready")) != 0 || mdCount(x.path("orch", "cur")) != 1 || x.count("claim") != 1 {
				t.Fatalf("ready %d cur %d claims %d", mdCount(x.path("orch", "ready")), mdCount(x.path("orch", "cur")), x.count("claim"))
			}
		},
	},
	{
		name: "ack",
		setup: func(x *direct) *faultState {
			m, _ := x.send("one", key())
			cl, _ := x.r.claim("orch", ClaimRequest{ReadKey: key()})
			return &faultState{id: m.ID, token: cl.Claim}
		},
		run: func(x *direct, s *faultState) *APIError {
			_, e := x.r.ack("orch", s.token, AckRequest{ID: s.id, Record: "done"})
			return e
		},
		check: func(t *testing.T, x *direct, s *faultState) {
			if !x.r.root().AckValid(x.path("orch"), s.id) || x.count("ack") != 1 {
				t.Fatalf("ack valid %v, records %d", x.r.root().AckValid(x.path("orch"), s.id), x.count("ack"))
			}
		},
	},
	{
		name: "release",
		setup: func(x *direct) *faultState {
			m, _ := x.send("one", key())
			cl, _ := x.r.claim("orch", ClaimRequest{ReadKey: key()})
			_, _ = x.r.ack("orch", cl.Claim, AckRequest{ID: m.ID, Record: "done"})
			return &faultState{token: cl.Claim}
		},
		run: func(x *direct, s *faultState) *APIError {
			e := x.r.release("orch", s.token)
			if e != nil && e.Code == "no_claim" && !exists(x.path("orch", "claim")) {
				return nil // released by the attempt that failed after its commit
			}
			return e
		},
		check: func(t *testing.T, x *direct, s *faultState) {
			gone, _ := filepath.Glob(x.path("orch", "tmp", ".released.*"))
			if exists(x.path("orch", "claim")) || len(gone) != 0 || x.count("release") != 1 {
				t.Fatalf("claim %v, released folders %v, records %d", exists(x.path("orch", "claim")), gone, x.count("release"))
			}
		},
	},
}

// TestFaultEveryStep fails, then crashes, each operation at every one of
// its filesystem steps (each journal write, fsync and rename). After a
// failure the route finishes or forgets the operation before admitting
// another, and a retry with the same keys completes it exactly once;
// after a crash a restart does the same. Every journal stays contiguous.
func TestFaultEveryStep(t *testing.T) {
	for _, c := range faultCases {
		// Count the steps of a clean run.
		x := newDirect(t)
		s := c.setup(x)
		f := &faults{}
		restore := inject(f)
		e := x.op(func() *APIError { return c.run(x, s) })
		restore()
		if e != nil {
			t.Fatalf("%s: clean run: %v", c.name, e)
		}
		steps := f.n
		if steps < 3 {
			t.Fatalf("%s: only %d steps: %v", c.name, steps, f.log)
		}
		for _, crash := range []bool{false, true} {
			for at := 1; at <= steps; at++ {
				name := fmt.Sprintf("%s/crash=%v/step%d", c.name, crash, at)
				t.Run(name, func(t *testing.T) {
					x := newDirect(t)
					s := c.setup(x)
					f := &faults{at: at, crash: crash}
					restore := inject(f)
					func() {
						defer func() {
							if p := recover(); p != nil {
								if _, ok := p.(crashSignal); !ok {
									panic(p)
								}
							}
						}()
						_ = x.op(func() *APIError { return c.run(x, s) })
					}()
					restore()
					what := f.log[at-1]
					if crash {
						x.kill()
						x.open()
					}
					if e := x.op(func() *APIError { return c.run(x, s) }); e != nil {
						t.Fatalf("after %s: retry: %v", what, e)
					}
					c.check(t, x, s)
					x.kill()
					x.open() // a strict replay of a contiguous journal
					c.check(t, x, s)
					if strings.Contains(what, "current.ndjson") && !crash && len(x.r.unfinished) != 0 {
						t.Fatalf("after %s: unfinished left", what)
					}
				})
			}
		}
	}
}

// failWhere makes every fsync, rename and journal write whose step text
// contains sub fail until the returned restore runs.
func failWhere(sub string) (restore func()) {
	f := &faults{}
	oldSync, oldRename, oldWrite := fsync, rename, journalWrite
	fail := func(what string) bool { f.log = append(f.log, what); return strings.Contains(what, sub) }
	fsync = func(fl *os.File) error {
		if fail("fsync " + fl.Name()) {
			return errInjected
		}
		return oldSync(fl)
	}
	rename = func(a, b string) error {
		if fail("rename " + b) {
			return errInjected
		}
		return oldRename(a, b)
	}
	journalWrite = func(fl *os.File, b []byte) (int, error) {
		if fail("write " + fl.Name()) {
			return 0, errInjected
		}
		return oldWrite(fl, b)
	}
	return func() { fsync, rename, journalWrite = oldSync, oldRename, oldWrite }
}

// TestStepFourFailureHoldsVisibility: a send whose ready file cannot be
// written is committed but not visible: visible_seq stays, no checkpoint
// passes it, and no other operation is admitted until its file is
// written; then both complete in order. A claim whose move fails is held
// the same way.
func TestStepFourFailureHoldsVisibility(t *testing.T) {
	x := newDirect(t)
	before := x.r.visible
	restore := failWhere("/ready/")
	k := key()
	_, e := x.send("one", k)
	if e == nil || e.Code != "busy" {
		t.Fatalf("send with a failed queue write: %v", e)
	}
	if x.r.visible != before || x.r.seq != before+1 {
		t.Fatalf("visible %d seq %d, before %d", x.r.visible, x.r.seq, before)
	}
	if err := x.r.writeCheckpoint(true); err == nil {
		t.Fatal("a checkpoint passed an unfinished send")
	}
	if e := x.op(func() *APIError { _, e := x.send("two", key()); return e }); e == nil || e.Code != "busy" {
		t.Fatalf("an operation was admitted past an unfinished one: %v", e)
	}
	restore()
	var r2 SendResult
	if e := x.op(func() *APIError { var e *APIError; r2, e = x.send("two", key()); return e }); e != nil {
		t.Fatal(e)
	}
	if mdCount(x.path("orch", "ready")) != 2 || x.r.visible != r2.Seq || len(x.r.unfinished) != 0 {
		t.Fatalf("ready %d visible %d", mdCount(x.path("orch", "ready")), x.r.visible)
	}
	if res, e := x.op2(func() (SendResult, *APIError) { return x.send("one", k) }); e != nil || !res.Duplicate {
		t.Fatalf("retry: %+v %v", res, e)
	}

	// A claim whose ready-to-cur move fails.
	restore = failWhere("/cur/")
	rk := key()
	if _, e := x.r.claim("orch", ClaimRequest{ReadKey: rk}); e == nil || e.Code != "busy" {
		t.Fatalf("claim with a failed move: %v", e)
	}
	if err := x.r.writeCheckpoint(true); err == nil {
		t.Fatal("a checkpoint passed an unfinished claim")
	}
	restore()
	if e := x.op(func() *APIError {
		cl, e := x.r.claim("orch", ClaimRequest{ReadKey: rk})
		if e == nil && len(cl.Manifest) != 2 {
			return Errf("internal", "manifest %+v", cl.Manifest)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}

func (x *direct) op2(fn func() (SendResult, *APIError)) (SendResult, *APIError) {
	var res SendResult
	e := x.op(func() *APIError { var e *APIError; res, e = fn(); return e })
	return res, e
}

// TestJournalFsyncFailure: a record written whose fsync fails is found
// durable on the rescanned tail and kept; the next record takes the next
// sequence (never the same one), and a restart reads a contiguous
// journal. When the recovery's own fsync fails too, the route stops.
func TestJournalFsyncFailure(t *testing.T) {
	x := newDirect(t)
	f := &faults{}
	oldSync := fsync
	fsync = func(fl *os.File) error {
		if strings.HasSuffix(fl.Name(), journalCurrent) {
			f.n++
			if f.n == 1 {
				return errInjected
			}
		}
		return oldSync(fl)
	}
	r1, e := x.send("one", key())
	fsync = oldSync
	if e != nil {
		t.Fatalf("a durable record after a failed fsync: %v", e)
	}
	r2, e := x.send("two", key())
	if e != nil || r2.Seq != r1.Seq+1 {
		t.Fatalf("seq %d after %d: %v", r2.Seq, r1.Seq, e)
	}
	x.kill()
	x.open()
	if x.count("send") != 2 {
		t.Fatalf("%d sends", x.count("send"))
	}
	// Both fsyncs fail: the tail cannot be trusted, the route stops.
	restore := failWhere(journalCurrent)
	if _, e := x.send("three", key()); e == nil {
		t.Fatal("send succeeded with a failing journal")
	}
	restore()
	if !x.r.degraded.Load() {
		t.Fatal("the route kept working with an unknown journal tail")
	}
	if e := x.op(func() *APIError { return nil }); e == nil || e.Code != "busy" {
		t.Fatalf("degraded route admitted work: %v", e)
	}
}

// TestJournalPartialWrite: a record cut short by a failed write is cut
// from the tail before the next operation, which reuses no durable
// sequence; a restart reads a clean journal.
func TestJournalPartialWrite(t *testing.T) {
	x := newDirect(t)
	oldWrite := journalWrite
	journalWrite = func(fl *os.File, b []byte) (int, error) {
		n, _ := oldWrite(fl, b[:len(b)/2])
		return n, errInjected
	}
	if _, e := x.send("one", key()); e == nil {
		t.Fatal("send succeeded on a partial record")
	}
	journalWrite = oldWrite
	b, _ := os.ReadFile(x.path("state", "events", journalCurrent))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		t.Fatal("the torn record was left in the journal")
	}
	r2, e := x.send("two", key())
	if e != nil {
		t.Fatal(e)
	}
	x.kill()
	x.open()
	recs := x.journal()
	if recs[len(recs)-1].Seq < r2.Seq || x.count("send") != 1 || mdCount(x.path("orch", "ready")) != 1 {
		t.Fatalf("journal %+v", recs)
	}
}

// TestDirectoryEntriesDurable: every new folder's parent, both sides of
// every rename and a new journal file's folder are fsynced, and a send's
// allocation folders are durable before its commit record is written.
func TestDirectoryEntriesDurable(t *testing.T) {
	x := newDirect(t)
	x.kill()
	_ = os.Remove(x.path("state", "events", journalCurrent))
	f := &faults{}
	restore := inject(f)
	x.open()
	restore()
	has := func(log []string, s string) int {
		for i, l := range log {
			if l == s {
				return i
			}
		}
		return -1
	}
	if has(f.log, "fsync "+x.path("state", "events")) < 0 {
		t.Fatalf("a new journal file's folder was not fsynced: %v", f.log)
	}
	f = &faults{}
	restore = inject(f)
	m, e := x.send("one", key())
	restore()
	if e != nil {
		t.Fatal(e)
	}
	commit := has(f.log, "write "+x.path("state", "events", journalCurrent))
	for _, d := range []string{x.path(), x.path("orch"), x.path("ids"), x.path("ids", m.ID), x.path("state")} {
		if i := has(f.log, "fsync "+d); i < 0 || i > commit {
			t.Fatalf("%s not fsynced before the commit (%d, commit %d): %v", d, i, commit, f.log)
		}
	}
	if i := has(f.log, "fsync "+x.path("orch", "ready")); i < commit {
		t.Fatalf("ready not fsynced after the commit: %v", f.log)
	}
	f = &faults{}
	restore = inject(f)
	cl, _ := x.r.claim("orch", ClaimRequest{ReadKey: key()})
	_, _ = x.r.ack("orch", cl.Claim, AckRequest{ID: m.ID, Record: "n"})
	_ = x.r.release("orch", cl.Claim)
	restore()
	for _, d := range []string{x.path("orch"), x.path("orch", "tmp"), x.path("orch", "ready"), x.path("orch", "cur")} {
		if has(f.log, "fsync "+d) < 0 {
			t.Fatalf("%s not fsynced around its renames: %v", d, f.log)
		}
	}
}

// TestMissingSequenceNoReuse: a lost state/sequence never restarts the
// ids: the high-water mark comes from the checkpoint, the journal and the
// allocated folders, and an id whose folder exists is never written.
func TestMissingSequenceNoReuse(t *testing.T) {
	x := newDirect(t)
	m1, _ := x.send("one", key())
	env1, _ := os.ReadFile(x.path("ids", m1.ID, "envelope"))
	_ = x.r.writeCheckpoint(true)
	x.kill()
	_ = os.Remove(x.path("state", "sequence"))
	x.open()
	m2, e := x.send("two", key())
	if e != nil || m2.ID == m1.ID {
		t.Fatalf("%s after %s: %v", m2.ID, m1.ID, e)
	}
	if env, _ := os.ReadFile(x.path("ids", m1.ID, "envelope")); string(env) != string(env1) {
		t.Fatal("an archived envelope changed")
	}
	// The checkpoint alone keeps the mark (its ids folders gone too).
	x.kill()
	_ = os.Remove(x.path("state", "sequence"))
	x.open()
	if x.r.lastID < idNumber(m2.ID) {
		t.Fatalf("high-water %d", x.r.lastID)
	}
	// An id allocated by a send that crashed before its record is past
	// the checkpoint and the journal: its folder alone keeps the mark.
	if !x.at("send.envelope", func() { _, _ = x.send("lost", key()) }) {
		t.Fatal("no crash")
	}
	lost := x.r.lastID
	x.kill()
	_ = os.Remove(x.path("state", "sequence"))
	x.open()
	if m4, e := x.send("four", key()); e != nil || idNumber(m4.ID) <= lost {
		t.Fatalf("%s after the lost id %d: %v", m4.ID, lost, e)
	}
	// An allocated id is refused before any file changes.
	x.r.lastID = 0
	seq, _ := os.ReadFile(x.path("state", "sequence"))
	if _, e := x.send("three", key()); e == nil || e.Code != "internal" || !strings.Contains(e.Message, "already allocated") {
		t.Fatalf("collision: %v", e)
	}
	if now, _ := os.ReadFile(x.path("state", "sequence")); string(now) != string(seq) {
		t.Fatal("state/sequence changed on a refused allocation")
	}
	if env, _ := os.ReadFile(x.path("ids", m1.ID, "envelope")); string(env) != string(env1) {
		t.Fatal("an archived envelope changed")
	}
}

// TestCheckpointKeepsHighWater: the checkpoint's last_message_id alone
// keeps the sequence when the sequence file and the journal's sends are
// gone.
func TestCheckpointKeepsHighWater(t *testing.T) {
	x := newDirect(t)
	m, _ := x.send("one", key())
	_ = x.r.writeCheckpoint(true)
	_ = x.r.writeCheckpoint(true) // the send is now only in the checkpoint
	x.kill()
	_ = os.Remove(x.path("state", "sequence"))
	_ = os.Rename(x.path("ids", m.ID), x.path("moved"))
	x.open()
	if x.r.lastID != idNumber(m.ID) {
		t.Fatalf("high-water %d, want %d", x.r.lastID, idNumber(m.ID))
	}
	// A send still in the journal keeps it too.
	m2, _ := x.send("two", key())
	x.kill()
	_ = os.Remove(x.path("state", "sequence"))
	_ = os.Rename(x.path("ids", m2.ID), x.path("moved2"))
	x.open()
	if x.r.lastID != idNumber(m2.ID) {
		t.Fatalf("high-water %d, want %d (journal)", x.r.lastID, idNumber(m2.ID))
	}
}

// TestMissingJournalNewEpoch: an inbox with history whose journal files
// are gone opens under a new durable epoch (old cursors reset), keeps its
// mailboxes, and goes on with the next id.
func TestMissingJournalNewEpoch(t *testing.T) {
	x := newDirect(t)
	m1, _ := x.send("one", key())
	old := x.r.epoch
	x.kill()
	for _, n := range []string{journalCurrent, journalPrev} {
		_ = os.Remove(x.path("state", "events", n))
	}
	x.open()
	o, _ := ReadOwner(x.dir)
	if x.r.epoch == old || o.Epoch != x.r.epoch {
		t.Fatalf("epoch %s (owner %s), old %s", x.r.epoch, o.Epoch, old)
	}
	if lo, ok := daemonLockOwner(x.path(inbox.ChannelLock)); !ok || lo.Epoch != x.r.epoch {
		t.Fatalf("retained lock owner %+v", lo)
	}
	if mdCount(x.path("orch", "ready")) != 1 {
		t.Fatal("the mailbox lost its message")
	}
	m2, e := x.send("two", key())
	if e != nil || idNumber(m2.ID) != idNumber(m1.ID)+1 {
		t.Fatalf("%s: %v", m2.ID, e)
	}
	// A brand-new route (no history) is not a lost journal.
	y := newDirect(t)
	if y.r.journalLost() {
		t.Fatal("a fresh route counted as lost")
	}
}

// TestReplayStrict: replay holds the route on any record it cannot trust:
// a sequence gap, a duplicate key, an unknown field or operation, another
// epoch on any line, a checkpoint completing past itself, and an
// oversized line that is not the torn tail.
func TestReplayStrict(t *testing.T) {
	type edit func(lines []string, epoch string) []string
	set := func(i int, f func(m map[string]any)) edit {
		return func(lines []string, _ string) []string {
			var m map[string]any
			_ = json.Unmarshal([]byte(lines[i]), &m)
			f(m)
			b, _ := json.Marshal(m)
			lines[i] = string(b)
			return lines
		}
	}
	cases := map[string]edit{
		"gap":           set(2, func(m map[string]any) { m["seq"] = m["seq"].(float64) + 5 }),
		"unknown op":    set(1, func(m map[string]any) { m["op"] = "not-an-operation" }),
		"unknown field": set(1, func(m map[string]any) { m["extra"] = 1 }),
		"epoch":         set(1, func(m map[string]any) { m["epoch"] = "0000" }),
		"duplicate key": func(lines []string, _ string) []string {
			lines[1] = strings.Replace(lines[1], `{"seq":`, `{"seq":99,"seq":`, 1)
			return lines
		},
		"checkpoint past itself": set(0, func(m map[string]any) { m["completed_through"] = m["seq"] }),
		"oversized middle line": func(lines []string, _ string) []string {
			lines[1] = `{"x":"` + strings.Repeat("a", 3000) + `"}`
			return lines
		},
	}
	for name, ed := range cases {
		t.Run(name, func(t *testing.T) {
			x := newDirect(t)
			_, _ = x.send("one", key())
			_, _ = x.send("two", key())
			x.kill()
			p := x.path("state", "events", journalCurrent)
			b, _ := os.ReadFile(p)
			lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			if len(lines) < 3 {
				t.Fatalf("journal %q", b)
			}
			lines = ed(lines, "")
			_ = os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
			r := newRoute(x.d, "r", x.dir)
			err := r.open()
			if !errors.Is(err, errHeld) {
				t.Fatalf("opened: %v", err)
			}
		})
	}
}

// TestOversizedTailBounded: a long torn tail (32 MiB, no newline) is cut
// without being held in memory.
func TestOversizedTailBounded(t *testing.T) {
	x := newDirect(t)
	_, _ = x.send("one", key())
	x.kill()
	p := x.path("state", "events", journalCurrent)
	st, _ := os.Stat(p)
	if err := os.Truncate(p, st.Size()+32<<20); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	x.open()
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 4<<20 {
		t.Fatalf("reading the tail allocated %d bytes", grown)
	}
	if st2, _ := os.Stat(p); st2.Size() > st.Size()+4096 {
		t.Fatalf("tail not cut: %d bytes", st2.Size())
	}
	if x.count("send") != 1 {
		t.Fatal("the record before the tail was lost")
	}
}

// TestReleasedClaimKeysBounded: 3000 claim and release cycles leave only
// the open claims' keys, in memory and in the checkpoint, which stays
// under its bound.
func TestReleasedClaimKeysBounded(t *testing.T) {
	old := fsync
	fsync = func(*os.File) error { return nil } // 6000 records; durability is not measured here
	t.Cleanup(func() { fsync = old })
	x := newDirect(t)
	for i := range 3000 {
		if e := x.op(func() *APIError {
			cl, e := x.r.claim("orch", ClaimRequest{ReadKey: key()})
			if e != nil {
				return e
			}
			return x.r.release("orch", cl.Claim)
		}); e != nil {
			t.Fatalf("cycle %d: %v", i, e)
		}
	}
	if len(x.r.readKeys) != 0 || len(x.r.extendKeys) != 0 {
		t.Fatalf("%d read and %d extend keys kept with no open claim", len(x.r.readKeys), len(x.r.extendKeys))
	}
	if err := x.r.writeCheckpoint(true); err != nil {
		t.Fatal(err)
	}
	x.kill()
	x.open()
	if len(x.r.readKeys) != 0 {
		t.Fatalf("%d read keys after restart", len(x.r.readKeys))
	}
}
