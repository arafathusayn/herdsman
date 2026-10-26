package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// FileSum is one checksummed file of the version 2 setup proof.
type FileSum struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Probe is one negative probe result the human reports at setup.
type Probe struct {
	Identity            json.RawMessage `json:"identity"`
	InboxWriteDenied    bool            `json:"inbox_write_denied"`
	SocketConnectDenied bool            `json:"socket_connect_denied"`
	RecordedBy          string          `json:"recorded_by"`
}

// SetupV2 is setup.json version 2 (4.6).
type SetupV2 struct {
	Version        int             `json:"version"`
	Inbox          string          `json:"inbox"`
	PolicyVerified bool            `json:"policy_verified"`
	Rules          []FileSum       `json:"rules"`
	Binary         FileSum         `json:"binary"`
	Boundary       json.RawMessage `json:"boundary"`
	WatchTargets   json.RawMessage `json:"watch_targets"`
	ResumeTargets  json.RawMessage `json:"resume_targets"`
	Probes         []Probe         `json:"probes"`
}

// SetupRequest is PUT /v1/routes/{route}/setup: the human's confirmed
// fields. The daemon computes every checksum itself.
type SetupRequest struct {
	Rules          []string        `json:"rules"`
	Binary         string          `json:"binary,omitempty"`
	PolicyVerified bool            `json:"policy_verified"`
	Boundary       json.RawMessage `json:"boundary"`
	WatchTargets   json.RawMessage `json:"watch_targets,omitempty"`
	ResumeTargets  json.RawMessage `json:"resume_targets,omitempty"`
	Probes         []Probe         `json:"probes,omitempty"`
}

// SetupResult is the answer of a setup write.
type SetupResult struct {
	ProofSHA256 string `json:"proof_sha256"`
}

// targetFiles are the target files a proof carries, by proof field.
var targetFiles = []string{"watch-targets.json", "resume-targets.json"}

// proofLatch is the stale latch: once a proof is seen stale, the route
// stays held until the human's next setup, even if the files return (7.3).
const proofLatch = "proof-stale"

// maxHashed bounds a hashed file (a policy or the herdsman binary).
const maxHashed = 1 << 30

// fileStampOf is a file's identity for the hash cache, from Lstat: a link,
// a FIFO or any other non-regular file has none.
func fileStampOf(p string) (fileStamp, bool) { return stampOf(p) }

// hashCache keeps each hashed file's SHA-256 by its own stamp, so a change
// to one file rereads only that file (A24).
type hashCache struct {
	m map[string]hashEntry
}

type hashEntry struct {
	stamp fileStamp
	sum   string
}

// sum is the SHA-256 of a regular file, never following a link and never
// opening a FIFO or device for reading; the file is hashed in bounded
// chunks.
func (c *hashCache) sum(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not absolute", p)
	}
	st, ok := fileStampOf(p)
	if !ok {
		return "", fmt.Errorf("%q is not a regular file", p)
	}
	if e, hit := c.m[p]; hit && e.stamp == st {
		return e.sum, nil
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || inode(fi) != st.ino {
		return "", fmt.Errorf("%q changed while it was opened", p)
	}
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.LimitReader(f, maxHashed+1), make([]byte, 64<<10))
	if err != nil {
		return "", err
	}
	if n > maxHashed {
		return "", fmt.Errorf("%q is larger than %d bytes", p, maxHashed)
	}
	s := hex.EncodeToString(h.Sum(nil))
	if c.m == nil {
		c.m = map[string]hashEntry{}
	}
	c.m[p] = hashEntry{stamp: st, sum: s}
	return s, nil
}

// installedBinary is the installed herdsman executable, links resolved.
func (r *Route) installedBinary() string {
	p, err := filepath.EvalSymlinks(r.d.cfg.Executable)
	if err != nil {
		return r.d.cfg.Executable
	}
	return p
}

// validIdentity reports a full {pane, session} identity object.
func validIdentity(v any) bool {
	m := inbox.Object(v)
	if m == nil || len(m) != 2 {
		return false
	}
	pane, _ := m["pane"].(string)
	s := inbox.Object(m["session"])
	if pane == "" || s == nil {
		return false
	}
	for _, k := range []string{"agent", "kind", "source", "value"} {
		if v, ok := s[k].(string); !ok || v == "" {
			return false
		}
	}
	return true
}

// checkSetupRequest validates the complete version 2 schema before any
// file is written (7.3): a boundary object, watch targets as a list of
// full identities, resume targets as an object of full identities, and
// probes with full identities recorded by the human.
func checkSetupRequest(q SetupRequest) *APIError {
	if len(q.Rules) == 0 {
		return Errf("bad_request", "name at least one sandbox policy file")
	}
	if len(q.Boundary) == 0 || inbox.StrictJSON(q.Boundary) != nil {
		return Errf("bad_request", "the boundary object is required (strict JSON)")
	}
	if b, _ := inbox.Decode(q.Boundary); inbox.Object(b) == nil {
		return Errf("bad_request", "the boundary must be a JSON object")
	}
	if len(q.WatchTargets) > 0 {
		v, err := inbox.Decode(q.WatchTargets)
		list, ok := v.([]any)
		if err != nil || inbox.StrictJSON(q.WatchTargets) != nil || !ok {
			return Errf("bad_request", "watch targets must be a JSON list of {pane, session} identities")
		}
		for _, x := range list {
			if !validIdentity(x) {
				return Errf("bad_request", "every watch target must be a full {pane, session} identity")
			}
		}
	}
	if len(q.ResumeTargets) > 0 {
		v, err := inbox.Decode(q.ResumeTargets)
		m := inbox.Object(v)
		if err != nil || inbox.StrictJSON(q.ResumeTargets) != nil || m == nil {
			return Errf("bad_request", "resume targets must be a JSON object of {pane, session} identities")
		}
		for name, x := range m {
			if !inbox.NameOK(name) || !validIdentity(x) {
				return Errf("bad_request", "resume target %q must be a full {pane, session} identity", name)
			}
		}
	}
	for _, p := range q.Probes {
		v, err := inbox.Decode(p.Identity)
		if err != nil || !validIdentity(v) || p.RecordedBy != "human" {
			return Errf("bad_request", "every probe needs a full identity recorded by the human")
		}
	}
	return nil
}

// setup writes setup.json version 2 (7.3); the caller holds the shared
// input lock. The target files are written (or removed) first and the
// proof last, so the proof's publication is the commit: a failure before
// it leaves the previous proof, which no longer matches the targets and
// is therefore stale until setup succeeds. A successful setup clears the
// stale latch.
func (r *Route) setup(q SetupRequest) (SetupResult, *APIError) {
	if e := checkSetupRequest(q); e != nil {
		return SetupResult{}, e
	}
	exe := r.installedBinary()
	bin := exe
	if q.Binary != "" {
		b, err := filepath.EvalSymlinks(q.Binary)
		if err != nil || b != exe {
			return SetupResult{}, Errf("bad_request", "the binary must be the installed herdsman executable (%s)", exe)
		}
	}
	doc := SetupV2{Version: 2, Inbox: r.inbox, PolicyVerified: q.PolicyVerified, Boundary: q.Boundary,
		WatchTargets: q.WatchTargets, ResumeTargets: q.ResumeTargets, Probes: q.Probes}
	if doc.Probes == nil {
		doc.Probes = []Probe{}
	}
	for i, p := range append(slices.Clone(q.Rules), bin) {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\t\r\n\\") {
			return SetupResult{}, Errf("bad_request", "%q is not an absolute path", p)
		}
		sum, err := r.hashes.sum(p)
		if err != nil {
			return SetupResult{}, Errf("bad_request", "%q is not a readable regular file", p)
		}
		fs := FileSum{Path: p, SHA256: sum}
		if i == len(q.Rules) {
			doc.Binary = fs
		} else {
			doc.Rules = append(doc.Rules, fs)
		}
	}
	data := prettyJSON(doc)
	if len(data) > inbox.MaxSize {
		return SetupResult{}, Errf("bad_request", "setup document too large")
	}
	for i, raw := range []json.RawMessage{doc.WatchTargets, doc.ResumeTargets} {
		p := r.path(targetFiles[i])
		var err error
		if len(raw) > 0 {
			err = writeDurable(p, append(append([]byte{}, raw...), '\n'), 0o600)
		} else if err = os.Remove(p); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if err != nil {
			return SetupResult{}, Errf("internal", "%s: %v", targetFiles[i], err)
		}
	}
	if err := writeDurable(r.path("setup.json"), data, 0o600); err != nil {
		return SetupResult{}, Errf("internal", "setup: %v", err)
	}
	if err := os.Remove(r.path("state", proofLatch)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SetupResult{}, Errf("internal", "clearing the stale latch: %v", err)
	}
	r.healthChanged()
	return SetupResult{ProofSHA256: sha256Hex(data)}, nil
}

// readSetup reads setup.json version 2, strictly.
func (r *Route) readSetup() (*SetupV2, bool) {
	b, ok := inbox.ReadStrict(r.inbox, r.path("setup.json"))
	if !ok {
		return nil, false
	}
	var doc SetupV2
	if strictDecode(b, &doc) != nil || doc.Version != 2 {
		return nil, false
	}
	return &doc, true
}

// proofValid checks the version 2 proof (7.3): it names this inbox with a
// verified policy; every rule file and the binary are regular files with
// their SHA-256; the binary is the installed executable; the target files
// are the proof's. A proof seen stale stays stale (the latch) until the
// human's next setup.
func (r *Route) proofValid() bool {
	if exists(r.path("state", proofLatch)) {
		return false
	}
	doc, ok := r.readSetup()
	if !ok {
		if exists(r.path("setup.json")) {
			r.latchStale("unreadable setup.json")
		}
		return false
	}
	if why := r.proofFault(doc); why != "" {
		r.latchStale(why)
		return false
	}
	return true
}

// proofFault names what makes a read proof stale ("" when none).
func (r *Route) proofFault(doc *SetupV2) string {
	if doc.Inbox != r.inbox || !doc.PolicyVerified || len(doc.Rules) == 0 {
		return "the proof does not name this inbox with a verified policy"
	}
	if doc.Binary.Path != r.installedBinary() {
		return "the proof's binary is not the installed executable"
	}
	for _, f := range append(slices.Clone(doc.Rules), doc.Binary) {
		if sum, err := r.hashes.sum(f.Path); err != nil || sum != f.SHA256 {
			return "a checksummed file changed: " + f.Path
		}
	}
	for i, raw := range []json.RawMessage{doc.WatchTargets, doc.ResumeTargets} {
		p := r.path(targetFiles[i])
		if len(raw) == 0 || string(raw) == "null" {
			if exists(p) {
				return targetFiles[i] + " exists without the proof"
			}
			continue
		}
		b, err := readSmall(p, inbox.MaxSize)
		if err != nil || !sameJSON(b, raw) {
			return targetFiles[i] + " differs from the proof"
		}
	}
	return ""
}

// latchStale records the stale latch durably and logs why.
func (r *Route) latchStale(why string) {
	if err := writeDurable(r.path("state", proofLatch), []byte(why+"\n"), 0o600); err != nil {
		r.d.logf("route %s: proof stale (%s); the latch could not be written: %v", r.name, why, err)
		return
	}
	r.d.logf("route %s: setup proof stale until the human runs setup again: %s", r.name, why)
}

// sameJSON reports two JSON texts equal after compaction.
func sameJSON(a, b []byte) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}
