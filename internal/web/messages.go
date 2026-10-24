package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// The message channel: the private inbox where the adjutant and the
// orchestrator leave each other herdsman messages (the skill's
// references/messages.md). The web app reads it the way `message status`
// does: no locks, no writes, no herdr calls. Bodies come one at a time, on
// request, and never from new/ or quarantine/, which hold unverified bytes.

const (
	envelopeMax       = 131072
	defaultHealthSecs = 180
	maxListedMessages = 400
)

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	idRe   = regexp.MustCompile(`^m[0-9]{12}$`)
	kinds  = map[string]bool{"brief": true, "observation": true, "question": true, "result": true, "cancel": true, "user_answer": true, "receipt": true}
	states = []string{"new", "ready", "cur", "quarantine"}
)

type identity struct {
	Pane    string `json:"pane"`
	Session struct {
		Agent  string `json:"agent"`
		Kind   string `json:"kind"`
		Source string `json:"source"`
		Value  string `json:"value"`
	} `json:"session"`
}

type envelope struct {
	Version      int      `json:"version"`
	ID           string   `json:"id"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	FromIdentity identity `json:"from_identity"`
	ToIdentity   identity `json:"to_identity"`
	Time         int64    `json:"time"`
	Kind         string   `json:"kind"`
	ReplyTo      string   `json:"reply_to"`
	Body         string   `json:"body"`
}

// ChannelPeer is one approved party.
type ChannelPeer struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Pane string `json:"pane"`
}

// ChannelHealth is one service's heartbeat.
type ChannelHealth struct {
	Name string `json:"name"`
	Age  int64  `json:"age"` // seconds, or -1 with no heartbeat
	OK   bool   `json:"ok"`
}

// ChannelBox is one party's mailbox.
type ChannelBox struct {
	Name       string         `json:"name"`
	Orphan     bool           `json:"orphan"`
	Counts     map[string]int `json:"counts"`
	Unacked    int            `json:"unacked"`
	Claim      string         `json:"claim"` // "held", "incomplete" or "none"
	Suspicious int            `json:"suspicious"`
}

// ChannelMessage is one envelope's header.
type ChannelMessage struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Kind    string `json:"kind"`
	ReplyTo string `json:"reply_to"`
	Time    int64  `json:"time"`
	State   string `json:"state"`
	Acked   bool   `json:"acknowledged"`
}

// ChannelAlert is a pending alert the courier could not show yet.
type ChannelAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Channel is the inbox as the Messages tab shows it.
type Channel struct {
	Configured  bool             `json:"configured"`
	Error       string           `json:"error,omitempty"`
	Approved    bool             `json:"approved"`
	Pending     bool             `json:"pending"`
	Peers       []ChannelPeer    `json:"peers"`
	Health      []ChannelHealth  `json:"health"`
	HealthAfter int64            `json:"health_after"`
	Boxes       []ChannelBox     `json:"boxes"`
	Messages    []ChannelMessage `json:"messages"`
	Alerts      []ChannelAlert   `json:"alerts"`
}

// readBounded reads a regular, non-link file of at most max bytes.
func readBounded(p string, max int64) ([]byte, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("not a bounded regular file")
	}
	return os.ReadFile(p)
}

// strictJSON fails on malformed JSON, trailing data and duplicate keys at
// any depth, as the channel's own `jq --stream` check does.
func strictJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := strictValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func strictValue(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		keys := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			ks, _ := k.(string)
			if keys[ks] {
				return fmt.Errorf("duplicate key %q", ks)
			}
			keys[ks] = true
			if err := strictValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case json.Delim('['):
		for dec.More() {
			if err := strictValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	}
	return nil
}

// keysExactly checks an object's key set.
func keysExactly(raw json.RawMessage, want ...string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) != len(want) {
		return false
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

var sessionValueRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func identityOK(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if !keysExactly(raw, "pane", "session") || json.Unmarshal(raw, &m) != nil {
		return false
	}
	if !keysExactly(m["session"], "agent", "kind", "source", "value") {
		return false
	}
	var id identity
	if json.Unmarshal(raw, &id) != nil {
		return false
	}
	s := id.Session
	return id.Pane != "" && s.Kind == "id" && sessionValueRe.MatchString(s.Value) && s.Agent != "" && s.Source != ""
}

// readEnvelope reads and checks one envelope file, as the channel's own
// envelope check does.
func readEnvelope(p string) (*envelope, error) {
	b, err := readBounded(p, envelopeMax)
	if err != nil {
		return nil, err
	}
	if err := strictJSON(b); err != nil {
		return nil, err
	}
	raw := json.RawMessage(b)
	if !keysExactly(raw, "body", "from", "from_identity", "id", "kind", "reply_to", "time", "to", "to_identity", "version") {
		return nil, errors.New("envelope keys")
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if !identityOK(m["from_identity"]) || !identityOK(m["to_identity"]) {
		return nil, errors.New("envelope identity")
	}
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	base := filepath.Base(p)
	if e.Version != 1 || e.ID+".md" != base || !idRe.MatchString(e.ID) || !nameRe.MatchString(e.From) ||
		!nameRe.MatchString(e.To) || !kinds[e.Kind] || (e.ReplyTo != "" && !idRe.MatchString(e.ReplyTo)) ||
		e.Time < 0 || len(e.Body) > 65536 {
		return nil, errors.New("envelope fields")
	}
	return &e, nil
}

// cksum is POSIX cksum's CRC, the channel's envelope checksum: "crc-size".
func cksum(b []byte) string {
	var crc uint32
	step := func(c byte) {
		crc ^= uint32(c) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	for _, c := range b {
		step(c)
	}
	for n := len(b); n > 0; n >>= 8 {
		step(byte(n))
	}
	return strconv.FormatUint(uint64(^crc), 10) + "-" + strconv.Itoa(len(b))
}

// ackValid mirrors the channel's ack check: an intake record bound to the
// envelope's bytes and the claim that read it.
func ackValid(root, box, id string) bool {
	b, err := readBounded(filepath.Join(root, box, "ack", id+".md.json"), envelopeMax)
	if err != nil || strictJSON(b) != nil {
		return false
	}
	var a struct {
		ID       string   `json:"id"`
		Claim    string   `json:"claim"`
		Time     *float64 `json:"time"`
		Record   string   `json:"processing_record"`
		Checksum *string  `json:"envelope_checksum"`
	}
	if json.Unmarshal(b, &a) != nil || a.ID != id || a.Claim == "" || a.Time == nil || a.Record == "" || a.Checksum == nil {
		return false
	}
	env, err := readBounded(filepath.Join(root, box, "cur", id+".md"), envelopeMax)
	if err != nil || cksum(env) != *a.Checksum {
		return false
	}
	claim, err := readBounded(filepath.Join(root, "ids", id, "claim"), 4096)
	return err == nil && string(bytes.TrimRight(claim, "\n")) == a.Claim
}

// openInbox checks the folder the way the channel does before it trusts it.
func openInbox(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("the inbox path must be absolute")
	}
	st, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("cannot open the inbox: %w", err)
	}
	if !st.IsDir() {
		return errors.New("the inbox is not a folder (or is a link)")
	}
	if st.Mode().Perm() != 0o700 {
		return fmt.Errorf("the inbox mode is %o, not 700", st.Mode().Perm())
	}
	return nil
}

func readChannel(root string, now time.Time) Channel {
	ch := Channel{Configured: true, Peers: []ChannelPeer{}, Health: []ChannelHealth{}, Boxes: []ChannelBox{}, Messages: []ChannelMessage{}, Alerts: []ChannelAlert{}}
	if err := openInbox(root); err != nil {
		ch.Error = err.Error()
		return ch
	}
	ch.HealthAfter = defaultHealthSecs
	if b, err := readBounded(filepath.Join(root, "state", "timing.json"), envelopeMax); err == nil && strictJSON(b) == nil {
		var t struct {
			Health int64 `json:"health"`
		}
		if json.Unmarshal(b, &t) == nil && t.Health >= 1 && t.Health <= 86400 {
			ch.HealthAfter = t.Health
		}
	}
	for _, s := range []struct{ name, file string }{{"courier", "heartbeat"}, {"watcher", "watch-heartbeat"}} {
		h := ChannelHealth{Name: s.name, Age: -1}
		if st, err := os.Lstat(filepath.Join(root, "state", s.file)); err == nil && st.Mode().IsRegular() {
			h.Age = int64(now.Sub(st.ModTime()) / time.Second)
			h.OK = h.Age <= ch.HealthAfter
		}
		ch.Health = append(ch.Health, h)
	}
	peers, perr := readBounded(filepath.Join(root, "peers"), envelopeMax)
	approved, aerr := readBounded(filepath.Join(root, "peers.approved"), envelopeMax)
	names := map[string]bool{}
	if aerr == nil && strictJSON(approved) == nil {
		var reg map[string]struct {
			Role     string   `json:"role"`
			Identity identity `json:"identity"`
		}
		if json.Unmarshal(approved, &reg) == nil {
			for name, p := range reg {
				if !nameRe.MatchString(name) || (p.Role != "adjutant" && p.Role != "orchestrator") {
					continue
				}
				names[name] = true
				ch.Peers = append(ch.Peers, ChannelPeer{Name: name, Role: p.Role, Pane: p.Identity.Pane})
			}
		}
		ch.Approved = perr == nil && bytes.Equal(peers, approved) && len(ch.Peers) > 0
	}
	ch.Pending = !ch.Approved
	sort.Slice(ch.Peers, func(i, j int) bool { return ch.Peers[i].Role < ch.Peers[j].Role })

	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		box := e.Name()
		if !e.IsDir() || !nameRe.MatchString(box) {
			continue
		}
		if st, err := os.Lstat(filepath.Join(root, box, "new")); err != nil || !st.IsDir() {
			continue
		}
		b := ChannelBox{Name: box, Orphan: !names[box], Counts: map[string]int{}, Claim: "none"}
		if st, err := os.Lstat(filepath.Join(root, box, "claim")); err == nil && st.IsDir() {
			b.Claim = "incomplete"
			if t, err := os.Lstat(filepath.Join(root, box, "claim", "token")); err == nil && t.Mode().IsRegular() {
				b.Claim = "held"
			}
		}
		for _, state := range states {
			files, _ := filepath.Glob(filepath.Join(root, box, state, "*.md"))
			b.Counts[state] = len(files)
			for _, f := range files {
				env, err := readEnvelope(f)
				if err != nil {
					b.Suspicious++
					continue
				}
				acked := state == "cur" && ackValid(root, box, env.ID)
				if (state == "ready" || state == "cur") && !acked {
					b.Unacked++
				}
				ch.Messages = append(ch.Messages, ChannelMessage{
					ID: env.ID, From: env.From, To: env.To, Kind: env.Kind, ReplyTo: env.ReplyTo,
					Time: env.Time, State: state, Acked: acked,
				})
			}
		}
		ch.Boxes = append(ch.Boxes, b)
	}
	sort.Slice(ch.Messages, func(i, j int) bool { return ch.Messages[i].ID < ch.Messages[j].ID })
	if len(ch.Messages) > maxListedMessages {
		ch.Messages = ch.Messages[len(ch.Messages)-maxListedMessages:]
	}
	alerts, _ := filepath.Glob(filepath.Join(root, "alerts", "*.pending"))
	for _, f := range alerts {
		b, err := readBounded(f, envelopeMax)
		if err != nil || strictJSON(b) != nil {
			continue
		}
		var a ChannelAlert
		if json.Unmarshal(b, &a) == nil && a.Title != "" {
			ch.Alerts = append(ch.Alerts, a)
		}
	}
	return ch
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Inbox == "" {
		writeJSON(w, http.StatusOK, Channel{Peers: []ChannelPeer{}, Health: []ChannelHealth{}, Boxes: []ChannelBox{}, Messages: []ChannelMessage{}, Alerts: []ChannelAlert{}})
		return
	}
	writeJSON(w, http.StatusOK, readChannel(s.cfg.Inbox, time.Now()))
}

// messageBody returns one verified message's body: from ready/ or cur/ only.
func (s *Server) messageBody(w http.ResponseWriter, r *http.Request) {
	box, id := r.PathValue("box"), r.PathValue("id")
	if s.cfg.Inbox == "" || openInbox(s.cfg.Inbox) != nil || !nameRe.MatchString(box) || !idRe.MatchString(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such message"})
		return
	}
	for _, state := range []string{"cur", "ready"} {
		env, err := readEnvelope(filepath.Join(s.cfg.Inbox, box, state, id+".md"))
		if err == nil && env.To == box {
			writeJSON(w, http.StatusOK, map[string]any{"id": env.ID, "body": env.Body})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no verified message with that id"})
}
