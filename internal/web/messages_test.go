package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCksumMatchesPOSIX(t *testing.T) {
	// From `printf 'hello world\n' | cksum` and `printf '' | cksum`.
	for in, want := range map[string]string{"hello world\n": "3733384285-12", "": "4294967295-0"} {
		if got := cksum([]byte(in)); got != want {
			t.Errorf("cksum(%q) = %s, want %s", in, got, want)
		}
	}
}

const identityJSON = `{"pane":"%s","session":{"agent":"claude","kind":"id","source":"herdr","value":"%s"}}`

func envelopeJSON(id, from, to, kind, reply string) string {
	return fmt.Sprintf(`{"version":1,"id":"%s","from":"%s","to":"%s","from_identity":`+identityJSON+`,"to_identity":`+identityJSON+`,"time":1761300000,"kind":"%s","reply_to":"%s","body":"{\"version\":1,\"text\":\"hello\"}"}`,
		id, from, to, "w1:p6", "s1", "w1:p1", "s2", kind, reply)
}

// newInbox builds a channel with an approved pair, one acknowledged brief,
// one waiting question, one quarantined entry and a pending alert.
func newInbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	peers := `{"front":{"identity":` + fmt.Sprintf(identityJSON, "w1:p6", "s1") + `,"role":"adjutant"},"boss":{"identity":` + fmt.Sprintf(identityJSON, "w1:p1", "s2") + `,"role":"orchestrator"}}`
	writeFile(t, filepath.Join(root, "peers"), peers)
	writeFile(t, filepath.Join(root, "peers.approved"), peers)
	writeFile(t, filepath.Join(root, "state", "timing.json"), `{"health":60}`)
	writeFile(t, filepath.Join(root, "state", "heartbeat"), "")
	for _, d := range []string{"boss/new", "boss/ready", "boss/quarantine", "front/new", "front/ready", "front/cur", "front/quarantine"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	brief := envelopeJSON("m000000000001", "front", "boss", "brief", "")
	writeFile(t, filepath.Join(root, "boss", "cur", "m000000000001.md"), brief)
	writeFile(t, filepath.Join(root, "ids", "m000000000001", "claim"), "tok\n")
	writeFile(t, filepath.Join(root, "boss", "ack", "m000000000001.md.json"),
		`{"id":"m000000000001","claim":"tok","time":1,"processing_record":"route log","envelope_checksum":"`+cksum([]byte(brief))+`"}`)
	writeFile(t, filepath.Join(root, "front", "ready", "m000000000002.md"), envelopeJSON("m000000000002", "boss", "front", "question", "m000000000001"))
	writeFile(t, filepath.Join(root, "front", "quarantine", "m000000000003.md"), `{"id":"m000000000003","id":"dup"}`)
	writeFile(t, filepath.Join(root, "alerts", "watch-down.pending"), `{"title":"Message watch unavailable","body":"Inspect heartbeat"}`)
	return root
}

func TestReadChannel(t *testing.T) {
	root := newInbox(t)
	ch := readChannel(root, time.Now())
	if ch.Error != "" || !ch.Approved || ch.Pending || len(ch.Peers) != 2 || ch.Peers[0].Role != "adjutant" {
		t.Fatalf("registry: %+v", ch)
	}
	if !ch.Health[0].OK || ch.Health[1].OK || ch.Health[1].Age != -1 || ch.HealthAfter != 60 {
		t.Fatalf("health: %+v", ch.Health)
	}
	if len(ch.Messages) != 2 || !ch.Messages[0].Acked || ch.Messages[0].State != "cur" ||
		ch.Messages[1].Acked || ch.Messages[1].Kind != "question" || ch.Messages[1].ReplyTo != "m000000000001" {
		t.Fatalf("messages: %+v", ch.Messages)
	}
	boxes := map[string]ChannelBox{}
	for _, b := range ch.Boxes {
		boxes[b.Name] = b
	}
	if boxes["front"].Unacked != 1 || boxes["front"].Suspicious != 1 || boxes["boss"].Unacked != 0 || boxes["front"].Orphan {
		t.Fatalf("boxes: %+v", ch.Boxes)
	}
	if len(ch.Alerts) != 1 || ch.Alerts[0].Title != "Message watch unavailable" {
		t.Fatalf("alerts: %+v", ch.Alerts)
	}

	// A changed pending registry holds the pair, and a tampered envelope
	// no longer counts as acknowledged.
	writeFile(t, filepath.Join(root, "peers"), `{}`)
	writeFile(t, filepath.Join(root, "boss", "cur", "m000000000001.md"), envelopeJSON("m000000000001", "front", "boss", "result", ""))
	ch = readChannel(root, time.Now())
	if ch.Approved || !ch.Pending || ch.Messages[0].Acked {
		t.Fatalf("after change: approved=%v acked=%v", ch.Approved, ch.Messages[0].Acked)
	}
}

// The channel's later rules: side-agent cards as their own kind, reserved
// names, claims that need an owner, links that void an ack, and the health
// flags and holds that `message status` prints.
func TestReadChannelFollowsTheChannelRules(t *testing.T) {
	root := newInbox(t)
	writeFile(t, filepath.Join(root, "front", "ready", "m000000000004.md"), envelopeJSON("m000000000004", "boss", "front", "side_agent_data", ""))
	writeFile(t, filepath.Join(root, "front", "claim", "token"), "tok")
	writeFile(t, filepath.Join(root, "state", "timing.json"), `{"health":0}`)
	writeFile(t, filepath.Join(root, "state", "automation-held"), "")
	writeFile(t, filepath.Join(root, "state", "hold-review"), `{"holds":{"front":{"reason":"the user asked","time":1761300001},"boss":{"reason":"stopping the reviewer","time":1761300000}}}`)
	writeFile(t, filepath.Join(root, "state", "hold-old"), `{"by":"boss","reason":"an older record","time":1761300000}`)
	writeFile(t, filepath.Join(root, "state", "route-paused"), "")
	writeFile(t, filepath.Join(root, "state", "resume-held-boss"), "HELD user pause or freeze; holds: front")
	writeFile(t, filepath.Join(root, "state", "resume-held-x.sent"), "") // matches two patterns
	lock := filepath.Join(root, "channel.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	ch := readChannel(root, time.Now())
	// Each owner of a hold shows with its reason; a record in another
	// format still shows as a hold, with no owners. A held resume shows
	// too, and a name two patterns match shows once.
	wantHolds := []ChannelHold{
		{Name: "route-paused"},
		{Name: "hold-old"},
		{Name: "hold-review", Owners: []ChannelHolding{{By: "boss", Reason: "stopping the reviewer"}, {By: "front", Reason: "the user asked"}}},
		{Name: "resume-held-x.sent"},
		{Name: "resume-held-boss"},
	}
	if ch.HealthAfter != defaultHealthSecs || !slices.Equal(ch.Flags, []string{"timing-invalid", "channel-lock-stale", "automation-held"}) ||
		!reflect.DeepEqual(ch.Holds, wantHolds) {
		t.Fatalf("flags %v holds %+v health_after %d", ch.Flags, ch.Holds, ch.HealthAfter)
	}
	if !slices.ContainsFunc(ch.Messages, func(m ChannelMessage) bool { return m.Kind == "side_agent_data" }) {
		t.Fatalf("side_agent_data refused: %+v", ch.Messages)
	}
	for _, b := range ch.Boxes {
		if b.Name == "front" && b.Claim != "incomplete" {
			t.Fatalf("claim without an owner: %s", b.Claim)
		}
	}

	// A claim folder replaced by a link is incomplete, never held, even
	// with both records; a real one with both is held.
	writeFile(t, filepath.Join(root, "boss", "claim", "token"), "tok")
	writeFile(t, filepath.Join(root, "boss", "claim", "owner"), "boss")
	claimState := func() string {
		for _, b := range readChannel(root, time.Now()).Boxes {
			if b.Name == "boss" {
				return b.Claim
			}
		}
		return ""
	}
	if got := claimState(); got != "held" {
		t.Fatalf("real claim folder: %s", got)
	}
	if err := os.Rename(filepath.Join(root, "boss", "claim"), filepath.Join(root, "claim-copy")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "claim-copy"), filepath.Join(root, "boss", "claim")); err != nil {
		t.Fatal(err)
	}
	if got := claimState(); got != "incomplete" {
		t.Fatalf("linked claim folder: %s", got)
	}

	// A link on an ack's path voids it: here the message's ids folder.
	moved := filepath.Join(root, "ids", "m000000000001")
	if err := os.Rename(moved, moved+"-dir"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved+"-dir", moved); err != nil {
		t.Fatal(err)
	}
	if ch := readChannel(root, time.Now()); ch.Messages[0].Acked {
		t.Fatal("an ack through a linked ids folder counted")
	}

	// A reserved name voids the whole registry.
	bad := `{"state":{"identity":` + fmt.Sprintf(identityJSON, "w1:p6", "s1") + `,"role":"adjutant"}}`
	writeFile(t, filepath.Join(root, "peers"), bad)
	writeFile(t, filepath.Join(root, "peers.approved"), bad)
	if ch := readChannel(root, time.Now()); ch.Approved || len(ch.Peers) != 0 {
		t.Fatalf("reserved peer name accepted: %+v", ch.Peers)
	}

	// So do an entry without an identity and two peers in one role, pane
	// or session, which the courier refuses too.
	for name, reg := range map[string]string{
		"no identity": `{"front":{"role":"adjutant"}}`,
		"one role":    `{"front":{"identity":` + fmt.Sprintf(identityJSON, "w1:p6", "s1") + `,"role":"adjutant"},"boss":{"identity":` + fmt.Sprintf(identityJSON, "w1:p1", "s2") + `,"role":"adjutant"}}`,
		"one pane":    `{"front":{"identity":` + fmt.Sprintf(identityJSON, "w1:p6", "s1") + `,"role":"adjutant"},"boss":{"identity":` + fmt.Sprintf(identityJSON, "w1:p6", "s2") + `,"role":"orchestrator"}}`,
		"one session": `{"front":{"identity":` + fmt.Sprintf(identityJSON, "w1:p6", "s1") + `,"role":"adjutant"},"boss":{"identity":` + fmt.Sprintf(identityJSON, "w1:p1", "s1") + `,"role":"orchestrator"}}`,
		"extra field": `{"front":{"identity":` + fmt.Sprintf(identityJSON, "w1:p6", "s1") + `,"role":"adjutant","note":"x"}}`,
	} {
		writeFile(t, filepath.Join(root, "peers"), reg)
		writeFile(t, filepath.Join(root, "peers.approved"), reg)
		if ch := readChannel(root, time.Now()); ch.Approved || len(ch.Peers) != 0 {
			t.Errorf("%s: registry accepted: %+v", name, ch.Peers)
		}
	}
}

// The body limit counts code points, as the channel's jq `length` does, not bytes.
func TestEnvelopeBodyLimitCountsCodePoints(t *testing.T) {
	dir := t.TempDir()
	body, _ := json.Marshal(strings.Repeat("é", 40000)) // 80000 bytes, 40000 code points
	env := strings.Replace(envelopeJSON("m000000000009", "boss", "front", "brief", ""), `"{\"version\":1,\"text\":\"hello\"}"`, string(body), 1)
	p := filepath.Join(dir, "m000000000009.md")
	writeFile(t, p, env)
	if _, err := readEnvelope(p); err != nil {
		t.Fatalf("a 40000-character body was refused: %v", err)
	}
}

func TestChannelRefusesAnOpenInbox(t *testing.T) {
	root := newInbox(t)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if ch := readChannel(root, time.Now()); ch.Error == "" || len(ch.Messages) != 0 {
		t.Fatalf("mode 755 inbox read: %+v", ch)
	}
}

func TestMessageBodyOnlyFromVerifiedStates(t *testing.T) {
	f := newFixture(t)
	inbox := newInbox(t)
	srv := NewServer(nil, f.hub, f.queue, Config{Token: "secret-token", Inbox: inbox})
	srv.devices = f.srv.devices // the browser f paired
	dev := strings.TrimPrefix(f.authed["Authorization"], "Bearer ")
	h := srv.Handler()
	get := func(path string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "127.0.0.1"
		req.Header.Set("Authorization", "Bearer "+dev)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	if code, m := get("/api/messages/front/m000000000002"); code != 200 || m["body"] != `{"version":1,"text":"hello"}` {
		t.Fatalf("ready body: %d %v", code, m)
	}
	for _, p := range []string{"/api/messages/front/m000000000003", "/api/messages/boss/m000000000002", "/api/messages/..%2fx/m000000000001", "/api/messages/front/nope"} {
		if code, _ := get(p); code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	if code, m := get("/api/messages"); code != 200 || m["configured"] != true {
		t.Fatalf("channel: %d %v", code, m)
	}

	// A queue folder linked to another passes off nothing: a waiting
	// message behind a linked ready folder is neither served nor listed.
	writeFile(t, filepath.Join(inbox, "front", "new", "m000000000005.md"), envelopeJSON("m000000000005", "boss", "front", "brief", ""))
	ready := filepath.Join(inbox, "front", "ready")
	if err := os.Rename(ready, ready+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(inbox, "front", "new"), ready); err != nil {
		t.Fatal(err)
	}
	if code, _ := get("/api/messages/front/m000000000005"); code != http.StatusNotFound {
		t.Errorf("body through a linked ready folder: %d, want 404", code)
	}
	for _, m := range readChannel(inbox, time.Now()).Messages {
		if m.ID == "m000000000005" && m.State != "new" {
			t.Errorf("listed through a linked ready folder as %s", m.State)
		}
	}
}

// Every mistyped envelope field fails the check, as the courier quarantines it.
func TestEnvelopeRefusesMistypedFields(t *testing.T) {
	dir := t.TempDir()
	good := envelopeJSON("m000000000009", "boss", "front", "brief", "")
	p := filepath.Join(dir, "m000000000009.md")
	writeFile(t, p, good)
	if _, err := readEnvelope(p); err != nil {
		t.Fatalf("a good envelope was refused: %v", err)
	}
	for _, c := range [][2]string{
		{`"version":1`, `"version":"1"`},
		{`"time":1761300000`, `"time":"soon"`},
		{`"kind":"brief"`, `"kind":"bogus"`},
		{`"reply_to":""`, `"reply_to":"m1"`},
		{`"body":"{\"version\":1,\"text\":\"hello\"}"`, `"body":7`},
		{`"kind":"id","source":"herdr","value":"s2"`, `"kind":"name","source":"herdr","value":"s2"`},
		{`"version":1,`, `"version":1,"extra":1,`},
	} {
		bad := strings.Replace(good, c[0], c[1], 1)
		if bad == good {
			t.Fatalf("fixture has no %s", c[0])
		}
		writeFile(t, p, bad)
		if _, err := readEnvelope(p); err == nil {
			t.Errorf("accepted %s", c[1])
		}
	}
}

// A fresh channel's registry is "{}": no pair yet, approval pending, and the
// peers an empty list, never null, so the app can read it.
func TestFreshRegistryHasAnEmptyPeerList(t *testing.T) {
	root := newInbox(t)
	writeFile(t, filepath.Join(root, "peers"), `{}`)
	writeFile(t, filepath.Join(root, "peers.approved"), `{}`)
	ch := readChannel(root, time.Now())
	b, err := json.Marshal(ch)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Approved || !ch.Pending || !strings.Contains(string(b), `"peers":[]`) {
		t.Fatalf("approved %v pending %v json %s", ch.Approved, ch.Pending, b)
	}
}
