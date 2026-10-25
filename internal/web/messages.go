package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/internal/inbox"
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
	kinds  = map[string]bool{"brief": true, "observation": true, "question": true, "result": true, "cancel": true, "user_answer": true, "receipt": true, "side_agent_data": true}
	states = []string{"new", "ready", "cur", "quarantine"}
)

// emptyChannel is a channel with every list present, so the app never meets null.
func emptyChannel() Channel {
	return Channel{Peers: []ChannelPeer{}, Health: []ChannelHealth{}, Flags: []string{}, Holds: []ChannelHold{}, Boxes: []ChannelBox{}, Messages: []ChannelMessage{}, Alerts: []ChannelAlert{}}
}

// staleDir reports whether p is a directory older than after seconds, as a held lock is.
func staleDir(p string, now time.Time, after int64) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir() && int64(now.Sub(st.ModTime())/time.Second) > after
}

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

// ChannelHold is one hold or resume intent. A hold written by `message.sh
// hold` keeps one entry per owner, `{"holds":{"<owner>":{"reason","time"}}}`;
// the target stays held while any entry stays. Other files carry no record.
type ChannelHold struct {
	Name   string           `json:"name"`
	Owners []ChannelHolding `json:"owners,omitempty"`
}

// ChannelHolding is one owner's entry in a hold.
type ChannelHolding struct {
	By     string `json:"by"`
	Reason string `json:"reason"`
}

// holdOwners reads a hold record in the owner format, owners sorted; nil
// for any other content, which the channel leaves for a human to inspect.
func holdOwners(b []byte) []ChannelHolding {
	var rec struct {
		Holds map[string]json.RawMessage `json:"holds"`
	}
	if inbox.StrictJSON(b) != nil || !inbox.KeysExactly(b, "holds") || json.Unmarshal(b, &rec) != nil || rec.Holds == nil {
		return nil
	}
	var out []ChannelHolding
	for by, raw := range rec.Holds {
		var e struct {
			Reason *string  `json:"reason"`
			Time   *float64 `json:"time"`
		}
		if !inbox.NameShape(by) || !inbox.KeysExactly(raw, "reason", "time") || json.Unmarshal(raw, &e) != nil || e.Reason == nil || e.Time == nil {
			return nil
		}
		out = append(out, ChannelHolding{By: by, Reason: *e.Reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].By < out[j].By })
	return out
}

// Channel is the inbox as the Messages tab shows it.
type Channel struct {
	Configured  bool            `json:"configured"`
	Error       string          `json:"error,omitempty"`
	Approved    bool            `json:"approved"`
	Pending     bool            `json:"pending"`
	Peers       []ChannelPeer   `json:"peers"`
	Health      []ChannelHealth `json:"health"`
	HealthAfter int64           `json:"health_after"`
	// Flags are the health problems `message status` reports beyond the heartbeats:
	// timing-invalid, mutation-lock-stale, channel-lock-stale, alert-lock-stale, automation-held.
	Flags []string `json:"flags"`
	// Holds are the hold and resume intents in state/: route-paused, route-frozen, hold-*, resume-*.sent, resume-held-*.
	Holds    []ChannelHold    `json:"holds"`
	Boxes    []ChannelBox     `json:"boxes"`
	Messages []ChannelMessage `json:"messages"`
	Alerts   []ChannelAlert   `json:"alerts"`
}

// readEnvelope reads and checks one envelope file, as the channel's own
// envelope check does.
func readEnvelope(p string) (*envelope, error) {
	b, err := inbox.ReadBounded(p, envelopeMax)
	if err != nil {
		return nil, err
	}
	if err := inbox.StrictJSON(b); err != nil {
		return nil, err
	}
	raw := json.RawMessage(b)
	if !inbox.KeysExactly(raw, "body", "from", "from_identity", "id", "kind", "reply_to", "time", "to", "to_identity", "version") {
		return nil, errors.New("envelope keys")
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if !inbox.IdentityOK(m["from_identity"]) || !inbox.IdentityOK(m["to_identity"]) {
		return nil, errors.New("envelope identity")
	}
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	base := filepath.Base(p)
	if e.Version != 1 || e.ID+".md" != base || !inbox.IDOK(e.ID) || !inbox.NameShape(e.From) ||
		!inbox.NameShape(e.To) || !kinds[e.Kind] || (e.ReplyTo != "" && !inbox.IDOK(e.ReplyTo)) ||
		e.Time < 0 || utf8.RuneCountInString(e.Body) > 65536 { // jq's length counts code points

		return nil, errors.New("envelope fields")
	}
	return &e, nil
}

// ackValid mirrors the channel's ack check: an intake record bound to the
// envelope's bytes and the claim that read it.
func ackValid(root, box, id string) bool {
	if !inbox.NoLinks(filepath.Join(root, box), filepath.Join(root, box, "ack"), filepath.Join(root, box, "cur"), filepath.Join(root, "ids", id)) {
		return false
	}
	b, err := inbox.ReadBounded(filepath.Join(root, box, "ack", id+".md.json"), envelopeMax)
	if err != nil || inbox.StrictJSON(b) != nil {
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
	env, err := inbox.ReadBounded(filepath.Join(root, box, "cur", id+".md"), envelopeMax)
	if err != nil || inbox.Digest(env) != *a.Checksum {
		return false
	}
	claim, err := inbox.ReadBounded(filepath.Join(root, "ids", id, "claim"), 4096)
	return err == nil && string(bytes.TrimRight(claim, "\n")) == a.Claim
}

func readChannel(root string, now time.Time) Channel {
	ch := emptyChannel()
	ch.Configured = true
	if err := inbox.CheckDir(root); err != nil {
		ch.Error = err.Error()
		return ch
	}
	ch.HealthAfter = defaultHealthSecs
	// A present but invalid timing file keeps the default and is flagged, as read-only status does.
	if timing := filepath.Join(root, "state", "timing.json"); inbox.Exists(timing) {
		valid := false
		if b, err := inbox.ReadBounded(timing, envelopeMax); err == nil && inbox.StrictJSON(b) == nil {
			var t struct {
				Health json.Number `json:"health"`
			}
			if json.Unmarshal(b, &t) == nil {
				if n, err := strconv.ParseInt(t.Health.String(), 10, 64); err == nil && n >= 1 && n <= 86400 {
					ch.HealthAfter, valid = n, true
				}
			}
		}
		if !valid {
			ch.Flags = append(ch.Flags, "timing-invalid")
		}
	}
	for _, l := range []struct{ dir, flag string }{{"herdr-mutation.lock", "mutation-lock-stale"}, {"channel.lock", "channel-lock-stale"}} {
		if staleDir(filepath.Join(root, l.dir), now, ch.HealthAfter) {
			ch.Flags = append(ch.Flags, l.flag)
		}
	}
	if locks, _ := filepath.Glob(filepath.Join(root, "alerts", "*.lock")); slices.ContainsFunc(locks, func(p string) bool { return staleDir(p, now, ch.HealthAfter) }) {
		ch.Flags = append(ch.Flags, "alert-lock-stale")
	}
	if inbox.Exists(filepath.Join(root, "state", "automation-held")) {
		ch.Flags = append(ch.Flags, "automation-held")
	}
	// A name matched by two patterns shows once.
	seen := map[string]bool{}
	for _, pat := range []string{"route-paused", "route-frozen", "hold-*", "resume-*.sent", "resume-held-*"} {
		found, _ := filepath.Glob(filepath.Join(root, "state", pat))
		for _, f := range found {
			h := ChannelHold{Name: filepath.Base(f)}
			if seen[h.Name] {
				continue
			}
			seen[h.Name] = true
			if strings.HasPrefix(h.Name, "hold-") {
				if b, err := inbox.ReadBounded(f, envelopeMax); err == nil {
					h.Owners = holdOwners(b)
				}
			}
			ch.Holds = append(ch.Holds, h)
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
	peers, perr := inbox.ReadBounded(filepath.Join(root, "peers"), envelopeMax)
	approved, aerr := inbox.ReadBounded(filepath.Join(root, "peers.approved"), envelopeMax)
	names := map[string]bool{}
	if reg, ok := webRegistry(approved, aerr); ok {
		ch.Peers = reg
		for _, p := range reg {
			names[p.Name] = true
		}
		_, pok := webRegistry(peers, perr)
		ch.Approved = pok && bytes.Equal(peers, approved) && len(reg) > 0
	}
	ch.Pending = !ch.Approved
	sort.Slice(ch.Peers, func(i, j int) bool { return ch.Peers[i].Role < ch.Peers[j].Role })

	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		box := e.Name()
		if !e.IsDir() || !inbox.NameOK(box) {
			continue
		}
		if st, err := os.Lstat(filepath.Join(root, box, "new")); err != nil || !st.IsDir() {
			continue
		}
		b := ChannelBox{Name: box, Orphan: !names[box], Counts: map[string]int{}, Claim: "none"}
		claim := filepath.Join(root, box, "claim")
		if st, err := os.Stat(claim); err == nil && st.IsDir() {
			// A claim is held only with both its token and its owner record, in
			// a real folder; a folder reached through a link is incomplete, as
			// `message status` shows it.
			b.Claim = "incomplete"
			if lst, err := os.Lstat(claim); err == nil && lst.IsDir() &&
				inbox.RegularFile(filepath.Join(claim, "token")) && inbox.RegularFile(filepath.Join(claim, "owner")) {
				b.Claim = "held"
			}
		}
		for _, state := range states {
			if !inbox.RealDirs(root, box, state) {
				continue
			}
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
		b, err := inbox.ReadBounded(f, envelopeMax)
		if err != nil || inbox.StrictJSON(b) != nil {
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
		writeJSON(w, http.StatusOK, emptyChannel())
		return
	}
	writeJSON(w, http.StatusOK, readChannel(s.cfg.Inbox, time.Now()))
}

// messageBody returns one verified message's body: from ready/ or cur/ only.
func (s *Server) messageBody(w http.ResponseWriter, r *http.Request) {
	box, id := r.PathValue("box"), r.PathValue("id")
	if s.cfg.Inbox == "" || inbox.CheckDir(s.cfg.Inbox) != nil || !inbox.NameShape(box) || !inbox.IDOK(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such message"})
		return
	}
	for _, state := range []string{"cur", "ready"} {
		if !inbox.RealDirs(s.cfg.Inbox, box, state) {
			continue
		}
		env, err := readEnvelope(filepath.Join(s.cfg.Inbox, box, state, id+".md"))
		if err == nil && env.To == box {
			writeJSON(w, http.StatusOK, map[string]any{"id": env.ID, "body": env.Body})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no verified message with that id"})
}

// webRegistry reads a registry file's peers as the app shows them.
func webRegistry(b []byte, readErr error) ([]ChannelPeer, bool) {
	if readErr != nil {
		return nil, false
	}
	reg, ok := inbox.ValidRegistry(b)
	if !ok {
		return nil, false
	}
	out := []ChannelPeer{}
	for _, p := range reg {
		out = append(out, ChannelPeer{Name: p.Name, Role: p.Role, Pane: p.Pane})
	}
	return out, true
}
