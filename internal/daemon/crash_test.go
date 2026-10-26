package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// state is a summary of a route's files for comparing restarts.
func (x *direct) state() string {
	var b strings.Builder
	_ = filepath.Walk(x.dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || strings.Contains(p, "state/events") || strings.Contains(p, "state/checkpoints") || strings.HasSuffix(p, "route.lock") {
			return nil
		}
		rel, _ := filepath.Rel(x.dir, p)
		if fi.Mode().IsRegular() {
			data, _ := os.ReadFile(p)
			b.WriteString(rel + " " + sha256Hex(data)[:8] + "\n")
		} else {
			b.WriteString(rel + "/\n")
		}
		return nil
	})
	return b.String()
}

func (x *direct) send(body, k string) (SendResult, *APIError) {
	return x.r.send("adj", SendRequest{To: "orch", Kind: "brief", Body: body, Key: k})
}

// TestCrashReplay stops a send and an ack at each injected point, as a
// killed daemon stops, then restarts and checks the files, the journal,
// that a waiter with the pre-crash cursor gets each committed event once,
// and that a second restart changes nothing.
func TestCrashReplay(t *testing.T) {
	for _, point := range []string{"send.envelope", "send.record", "send.queue"} {
		t.Run(point, func(t *testing.T) {
			x := newDirect(t)
			cursor := x.r.visible
			k := key()
			if !x.at(point, func() { _, _ = x.send("hello", k) }) {
				t.Fatal("no crash")
			}
			x.kill()
			x.open()
			committed := point != "send.envelope"
			ready := x.path("orch", "ready", "m000000000001.md")
			if committed != exists(ready) {
				t.Fatalf("committed %v, ready file %v", committed, exists(ready))
			}
			if !committed {
				b, _ := os.ReadFile(x.path("ids", "m000000000001", "terminal.json"))
				if !strings.Contains(string(b), "abandoned") {
					t.Fatalf("uncommitted send not abandoned: %s", b)
				}
				// A retry with the same key allocates a new id.
				res, e := x.send("hello", k)
				if e != nil || res.ID != "m000000000002" {
					t.Fatalf("retry %+v %v", res, e)
				}
			} else {
				res, e := x.send("hello", k)
				if e != nil || res.ID != "m000000000001" || !res.Duplicate {
					t.Fatalf("retry after commit %+v %v", res, e)
				}
			}
			s := newSubscriber("r", "orch")
			frames, e := x.r.subscribe(s, x.r.epoch+"-"+itoa(cursor))
			if e != nil {
				t.Fatal(e)
			}
			n := 0
			for _, f := range frames {
				if f.rec != nil && f.rec.Event.Type == "message" && f.rec.ID == "m000000000001" {
					n++
				}
			}
			if committed && n != 1 {
				t.Fatalf("committed message replayed %d times", n)
			}
			before := x.state()
			x.kill()
			x.open()
			if after := x.state(); after != before {
				t.Fatalf("second restart changed files:\n%s\n---\n%s", before, after)
			}
		})
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestAckCrashPayload(t *testing.T) {
	for _, point := range []string{"ack.record", "ack.file", "ack.terminal"} {
		t.Run(point, func(t *testing.T) {
			x := newDirect(t)
			res, _ := x.send("hello", key())
			cl, e := x.r.claim("orch", ClaimRequest{ReadKey: key()})
			if e != nil {
				t.Fatal(e)
			}
			if !x.at(point, func() { _, _ = x.r.ack("orch", cl.Claim, AckRequest{ID: res.ID, Record: "note"}) }) {
				t.Fatal("no crash")
			}
			x.kill()
			x.open()
			payload, _ := os.ReadFile(x.path("ids", res.ID, "ack.json"))
			ackFile, _ := os.ReadFile(x.path("orch", "ack", res.ID+".md.json"))
			if len(payload) == 0 || !bytes.Equal(payload, ackFile) {
				t.Fatalf("rebuilt ack differs from its payload:\n%s\n%s", payload, ackFile)
			}
			if !(&inbox.Root{Dir: x.dir}).AckValid(x.path("orch"), res.ID) || !exists(x.path("ids", res.ID, "terminal.json")) {
				t.Fatal("ack or terminal not rebuilt")
			}
		})
	}
}

func TestAckPayloadMismatchHolds(t *testing.T) {
	x := newDirect(t)
	res, _ := x.send("hello", key())
	cl, _ := x.r.claim("orch", ClaimRequest{ReadKey: key()})
	x.at("ack.record", func() { _, _ = x.r.ack("orch", cl.Claim, AckRequest{ID: res.ID, Record: "note"}) })
	x.kill()
	_ = os.WriteFile(x.path("ids", res.ID, "ack.json"), []byte("tampered"), 0o600)
	r := newRoute(x.d, "r", x.dir)
	if err := r.open(); err == nil || !strings.Contains(err.Error(), "held for inspection") {
		t.Fatalf("got %v", err)
	}
	r.closeFiles()
	if exists(x.path("orch", "ack", res.ID+".md.json")) {
		t.Fatal("an ack was invented from a mismatched payload")
	}
}

func TestCrashClaim(t *testing.T) {
	for _, point := range []string{"claim.record", "claim.staged", "claim.published", "claim.bound", "claim.moved"} {
		t.Run(point, func(t *testing.T) {
			x := newDirect(t)
			a, _ := x.send("one", key())
			b, _ := x.send("two", key())
			k := key()
			if !x.at(point, func() { _, _ = x.r.claim("orch", ClaimRequest{ReadKey: k}) }) {
				t.Fatal("no crash")
			}
			x.kill()
			x.open()
			cl, e := x.r.claim("orch", ClaimRequest{ReadKey: k})
			if e != nil || len(cl.Manifest) != 2 || cl.Manifest[0].ID != a.ID || cl.Manifest[1].ID != b.ID {
				t.Fatalf("after %s: %+v %v", point, cl, e)
			}
			if inbox.CountState(x.path("orch", "ready")) != 0 {
				t.Fatal("ids left in ready")
			}
		})
	}
}

func TestReleaseCrash(t *testing.T) {
	for _, point := range []string{"release.record", "release.renamed"} {
		t.Run(point, func(t *testing.T) {
			x := newDirect(t)
			a, _ := x.send("one", key())
			cl, _ := x.r.claim("orch", ClaimRequest{ReadKey: key()})
			if _, e := x.r.ack("orch", cl.Claim, AckRequest{ID: a.ID, Record: "n"}); e != nil {
				t.Fatal(e)
			}
			if !x.at(point, func() { _ = x.r.release("orch", cl.Claim) }) {
				t.Fatal("no crash")
			}
			x.kill()
			x.open()
			if exists(x.path("orch", "claim")) || exists(x.path("orch", "tmp", ".released."+cl.Claim)) {
				t.Fatal("release not finished by replay")
			}
		})
	}
}

func TestSendKeyDedup(t *testing.T) {
	old := journalRotate
	journalRotate = 2048
	t.Cleanup(func() { journalRotate = old })
	x := newDirect(t)
	k := key()
	first, _ := x.send("hello", k)
	for range 20 {
		x.rotateIfNeeded()
		_, _ = x.send("filler", key())
	}
	x.kill()
	x.open()
	res, e := x.send("hello", k)
	if e != nil || res.ID != first.ID || !res.Duplicate {
		t.Fatalf("key lost across rotation and restart: %+v %v", res, e)
	}
	if x.count("checkpoint") < 2 {
		t.Fatal("no rotation happened")
	}
}

// rotateIfNeeded is the actor's rotation between operations.
func (x *direct) rotateIfNeeded() {
	if x.r.j.needsRotation() {
		if err := x.r.writeCheckpoint(true); err != nil {
			x.t.Fatal(err)
		}
	}
}

func TestRotationNoReplay(t *testing.T) {
	old := journalRotate
	journalRotate = 2048
	t.Cleanup(func() { journalRotate = old })
	x := newDirect(t)
	a, _ := x.send("one", key())
	cl, _ := x.r.claim("orch", ClaimRequest{ReadKey: key()})
	_, _ = x.r.ack("orch", cl.Claim, AckRequest{ID: a.ID, Record: "n"})
	_ = x.r.release("orch", cl.Claim)
	for range 20 {
		x.rotateIfNeeded()
		_, _ = x.send("filler", key())
	}
	receipts := x.count("ack")
	x.kill()
	x.open()
	x.kill()
	x.open()
	if x.count("ack") != receipts {
		t.Fatal("a restart recreated a receipt")
	}
	if exists(x.path("orch", "claim")) {
		t.Fatal("an old claim was republished")
	}
}

func TestCrashAtRotation(t *testing.T) {
	old := journalRotate
	journalRotate = 1024
	t.Cleanup(func() { journalRotate = old })
	for _, point := range []string{"checkpoint.payload", "checkpoint.rotated", "checkpoint.record"} {
		t.Run(point, func(t *testing.T) {
			x := newDirect(t)
			for range 5 {
				_, _ = x.send("x", key())
			}
			k := key()
			last, _ := x.send("last", k)
			if !x.at(point, func() { x.rotateIfNeeded() }) {
				t.Skip("no rotation was due")
			}
			x.kill()
			x.open()
			res, e := x.send("last", k)
			if e != nil || res.ID != last.ID {
				t.Fatalf("%+v %v", res, e)
			}
		})
	}
}

func TestReplayTornLine(t *testing.T) {
	x := newDirect(t)
	_, _ = x.send("one", key())
	x.kill()
	p := x.path("state", "events", journalCurrent)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"seq":99,"epoch":"`)
	_ = f.Close()
	x.open()
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), `{"seq":99,"epoch":"`+"\n") || !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("torn line not cut: %q", b[len(b)-40:])
	}
}

func TestReplayCorruptHolds(t *testing.T) {
	x := newDirect(t)
	_, _ = x.send("one", key())
	_, _ = x.send("two", key())
	x.kill()
	p := x.path("state", "events", journalCurrent)
	b, _ := os.ReadFile(p)
	lines := strings.SplitAfter(string(b), "\n")
	lines[1] = "garbage\n"
	_ = os.WriteFile(p, []byte(strings.Join(lines, "")), 0o600)
	r := newRoute(x.d, "r", x.dir)
	err := r.open()
	r.closeFiles()
	if err == nil || !strings.Contains(err.Error(), "held for inspection") {
		t.Fatalf("got %v", err)
	}
}

func TestRecordSizeBound(t *testing.T) {
	long := strings.Repeat("a", 64)
	rec := &Record{Seq: 1 << 62, Epoch: strings.Repeat("f", 32), Time: 1 << 40, Op: "send", ID: "m999999999999", Key: strings.Repeat("e", 32),
		From: long[:32], To: long[:32], Kind: "side_agent_data", ReplyTo: "m999999999999", BodySHA256: long, Payload: nil}
	reply := "m999999999999"
	rec.Event = &Event{Type: "message", ID: rec.ID, From: rec.From, To: rec.To, Kind: rec.Kind, ReplyTo: &reply}
	if _, err := encodeRecord(rec); err != nil {
		t.Fatalf("a maximum send record: %v", err)
	}
	rec.Interaction = strings.Repeat("x", 3000)
	if _, err := encodeRecord(rec); err == nil {
		t.Fatal("a record over 2 KiB was accepted")
	}
}

func TestMaxClaimRecord(t *testing.T) {
	x := newDirect(t)
	_ = mkdirs(x.dir, "orch/ready")
	for i := 1; i <= claimMaxIDs; i++ {
		id := "m" + strings.Repeat("0", 12-len(itoa(int64(i)))) + itoa(int64(i))
		_ = os.WriteFile(x.path("orch", "ready", id+".md"), []byte("{}"), 0o600)
	}
	ids := x.r.readyIDs("orch")
	data, _ := encode(claimPayload{Token: strings.Repeat("a", 32), N: 1, IDs: ids})
	if len(ids) != claimMaxIDs || len(data) > 20<<10 {
		t.Fatalf("%d ids, %d bytes", len(ids), len(data))
	}
	rec := &Record{Seq: 1 << 40, Epoch: strings.Repeat("f", 32), Time: 1 << 40, Op: "claim", Box: strings.Repeat("a", 32), Token: strings.Repeat("a", 32),
		ReadKey: strings.Repeat("b", 32), N: 1, Payload: &Payload{Path: "state/claims/" + strings.Repeat("a", 32) + "-1.json", SHA256: strings.Repeat("c", 64), Bytes: int64(len(data))}}
	if _, err := encodeRecord(rec); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerRequired(t *testing.T) {
	x := newDirect(t)
	x.kill()
	_ = os.Remove(x.path("state", "owner.json"))
	r := newRoute(x.d, "r", x.dir)
	err := r.open()
	r.closeFiles()
	if err == nil || !strings.Contains(err.Error(), "no daemon owner record") {
		t.Fatalf("got %v", err)
	}
}

func TestRouteLockExcludes(t *testing.T) {
	x := newDirect(t)
	r := newRoute(x.d, "r", x.dir)
	err := r.open()
	if err == nil {
		r.closeFiles()
		t.Fatal("a second owner opened a held route")
	}
	if !strings.Contains(err.Error(), "route lock") {
		t.Fatalf("got %v", err)
	}
}

// TestSendKeyCapacityRetry: the 513th send inside the retry window is
// refused with key_window_full; a lost-response retry inside the window
// returns the first ID across the overflow, a rotation and a restart; once
// older keys leave the window the refused send succeeds (5.4).
func TestSendKeyCapacityRetry(t *testing.T) {
	old := journalRotate
	journalRotate = 64 << 10
	t.Cleanup(func() { journalRotate = old })
	x := newDirect(t)
	first := key()
	r1, e := x.send("first", first)
	if e != nil {
		t.Fatal(e)
	}
	for range keyWindowMax - 1 {
		if _, e := x.send("x", key()); e != nil {
			t.Fatal(e)
		}
		x.rotateIfNeeded()
	}
	late := key()
	if _, e := x.send("late", late); e == nil || e.Code != "key_window_full" {
		t.Fatalf("513th: %v", e)
	}
	if res, e := x.send("first", first); e != nil || res.ID != r1.ID || !res.Duplicate {
		t.Fatalf("retry across overflow: %+v %v", res, e)
	}
	x.kill()
	x.open()
	if res, e := x.send("first", first); e != nil || res.ID != r1.ID || !res.Duplicate {
		t.Fatalf("retry after restart: %+v %v", res, e)
	}
	x.clock.Add(keyWindow)
	if _, e := x.send("late", late); e != nil {
		t.Fatalf("after the window: %v", e)
	}
	// Past the window and with no room, the oldest key is dropped: at most
	// 512 keys are remembered.
	if n := len(x.r.sendKeys); n > keyWindowMax {
		t.Fatalf("%d keys remembered", n)
	}
	if _, ok := x.r.sendKeys[first]; ok {
		t.Fatal("the oldest key past the window was kept with no room")
	}
}

// TestFullKeyCheckpoint: 512 canonical-maximum send keys and two claims
// with 64 extend keys each encode under the checkpoint bound, at rotation
// and at restart; a 65th extension inside the window is refused (4.2).
// The active question index is Phase B; its 16 KiB share stays free.
func TestFullKeyCheckpoint(t *testing.T) {
	x := newDirect(t)
	now := x.clock.Now().Unix()
	name := strings.Repeat("n", 32)
	hex := func(i, n int) string { return fmt.Sprintf("%0*x", n, i) }
	for i := range keyWindowMax {
		k := hex(i, 32)
		x.r.sendKeys[k] = &sendKey{Key: k, ID: fmt.Sprintf("m%012d", 999999000000+i), From: name, To: name, Kind: "side_agent_data",
			ReplyTo: "m999999999999", BodySHA256: hex(i, 64), Time: now, Seq: 9_000_000_000 + int64(i)}
	}
	// One read key and 64 extend keys for each of two open claims.
	for _, box := range []string{"adj", "orch"} {
		cl, e := x.r.claim(box, ClaimRequest{ReadKey: hex(len(box), 32)})
		if e != nil {
			t.Fatal(e)
		}
		for i := range extendKeyMax {
			if _, e := x.r.claim(box, ClaimRequest{Token: cl.Claim, ExtendKey: hex(1000*len(box)+i, 32)}); e != nil {
				t.Fatalf("extension %d: %v", i, e)
			}
		}
		if _, e := x.r.claim(box, ClaimRequest{Token: cl.Claim, ExtendKey: hex(9999, 32)}); e == nil || e.Code != "key_window_full" {
			t.Fatalf("65th extension: %v", e)
		}
	}
	if err := x.r.writeCheckpoint(true); err != nil {
		t.Fatal(err)
	}
	cps, _ := filepath.Glob(x.path("state", "checkpoints", "*.json"))
	if len(cps) != 1 {
		t.Fatalf("checkpoints %v", cps)
	}
	st, _ := os.Stat(cps[0])
	// The send-key entries also carry from, kind, reply_to and seq (the
	// whole content binding of 5.4), so they are larger than the design's
	// 210 bytes; with the 16 KiB question index they stay under the bound.
	if st.Size()+16<<10 > checkpointMax {
		t.Fatalf("checkpoint of %d bytes leaves no room for the question index", st.Size())
	}
	t.Logf("full checkpoint: %d bytes", st.Size())
	x.kill()
	x.open()
	if len(x.r.sendKeys) != keyWindowMax || len(x.r.extendKeys) != 2*extendKeyMax || len(x.r.readKeys) != 2 {
		t.Fatalf("restored %d send, %d extend, %d read keys", len(x.r.sendKeys), len(x.r.extendKeys), len(x.r.readKeys))
	}
}

// TestAckCacheFollowsFiles: an ack found valid is remembered while the
// message, its ack and its claim record are unchanged; a changed message
// is checked again and counts as unacknowledged; a full cache starts over.
func TestAckCacheFollowsFiles(t *testing.T) {
	x := newDirect(t)
	a, _ := x.send("one", key())
	cl, e := x.r.claim("orch", ClaimRequest{ReadKey: key()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.ack("orch", cl.Claim, AckRequest{ID: a.ID, Record: "n"}); e != nil {
		t.Fatal(e)
	}
	box := x.path("orch")
	if u := x.r.unacked(box); len(u) != 0 {
		t.Fatalf("unacked %v", u)
	}
	if len(x.r.acks.m) != 1 {
		t.Fatalf("cache %v", x.r.acks.m)
	}
	if u := x.r.unacked(box); len(u) != 0 {
		t.Fatalf("cached: unacked %v", u)
	}
	f := x.path("orch", "cur", a.ID+".md")
	b, _ := os.ReadFile(f)
	if err := os.WriteFile(f, append(b, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if u := x.r.unacked(box); len(u) != 1 {
		t.Fatalf("a changed message must be checked again: %v", u)
	}
	_ = os.WriteFile(f, b, 0o600)
	x.r.acks.m = map[string]ackEntry{}
	for i := range ackCacheMax {
		x.r.acks.m[fmt.Sprint(i)] = ackEntry{}
	}
	if u := x.r.unacked(box); len(u) != 0 || len(x.r.acks.m) != ackCacheMax {
		t.Fatalf("full cache: %v, %d entries", u, len(x.r.acks.m))
	}
	// A full cache evicts one entry for each new one, never all of them,
	// so a history one past the bound is not checked again from scratch.
	kept := 0
	for i := range ackCacheMax {
		if _, ok := x.r.acks.m[fmt.Sprint(i)]; ok {
			kept++
		}
	}
	if kept != ackCacheMax-1 {
		t.Fatalf("a full cache kept %d of %d entries", kept, ackCacheMax)
	}
}
