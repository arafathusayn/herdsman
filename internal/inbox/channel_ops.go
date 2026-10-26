package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The write side of the channel, as message-lib.sh: atomic state files,
// unique moves, mailboxes, the diagnostic log, the envelope and ack
// checks and the queue counts. Every function keeps the script's paths and
// formats, so the scripts and the binary can share one inbox.

// FileOps are the file system calls of the channel's state changes that a
// test may replace to inject a fault (a failing or a pretended rename, a
// failing age read), as the shell suite's faultbin does.
type FileOps struct {
	Rename func(oldpath, newpath string) error
	// Mtime is a file's modification time in seconds (stat %Y), not
	// following a final link, as the scripts' mtime.
	Mtime func(p string) (int64, error)
}

func osMtime(p string) (int64, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return 0, err
	}
	return st.ModTime().Unix(), nil
}

var ops = FileOps{Rename: os.Rename, Mtime: osMtime}

// SetFileOps replaces the channel's file calls (nil fields keep the
// current ones) and returns a function that restores them. For tests.
func SetFileOps(o FileOps) (restore func()) {
	old := ops
	if o.Rename != nil {
		ops.Rename = o.Rename
	}
	if o.Mtime != nil {
		ops.Mtime = o.Mtime
	}
	return func() { ops = old }
}

// Mtime is the scripts' mtime: the modification time in seconds.
func Mtime(p string) (int64, error) { return ops.Mtime(p) }

// Pause sleeps with the root's sleep (injected in tests).
func (r *Root) Pause(d time.Duration) { r.sleep(d) }

// Unix is the root's clock in seconds, the scripts' now.
func (r *Root) Unix() int64 { return r.now().Unix() }

// ErrWrite is an atomic write that did not commit: an empty or invalid
// producer output, or a failed rename. The target keeps its old bytes.
var ErrWrite = errors.New("state write not committed")

// strictTarget reports whether atomic checks a target as strict JSON.
func strictTarget(target string) bool {
	b := filepath.Base(target)
	return strings.HasSuffix(target, ".json") || b == "peers" || b == "peers.approved"
}

// Atomic is the scripts' atomic: data is written to a temporary file under
// state/ and renamed over target. Empty data, data that is not strict JSON
// for a .json or registry target, and a failed rename leave target as it
// was (ErrWrite). A target that is not a safe leaf is a *Fatal.
func (r *Root) Atomic(target string, data []byte) error {
	if err := SafeLeaf(r.Dir, target); err != nil {
		return &Fatal{Msg: err.Error()}
	}
	if len(data) == 0 || (strictTarget(target) && StrictJSON(data) != nil) {
		return ErrWrite
	}
	tmp, err := os.CreateTemp(r.Path("state"), ".write.")
	if err != nil {
		return ErrWrite
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return ErrWrite
	}
	if err := ops.Rename(name, target); err != nil || Exists(name) || !Regular(r.Dir, target) {
		_ = os.Remove(name)
		return ErrWrite
	}
	return nil
}

// WriteText is the scripts' write_text: one line, atomically.
func (r *Root) WriteText(target, text string) error {
	return r.Atomic(target, []byte(text+"\n"))
}

// MoveUnique is the scripts' move_unique: src moves to dst, which must not
// exist; afterwards src is gone and dst is a regular file. Every failure is
// a *Fatal with the scripts' message.
func (r *Root) MoveUnique(src, dst string) error {
	if Exists(dst) {
		return &Fatal{Msg: "destination exists: " + dst}
	}
	_ = ops.Rename(src, dst)
	if Exists(src) || !Regular(r.Dir, dst) {
		return &Fatal{Msg: "move failed: " + src}
	}
	return nil
}

// Mailbox folders, as box_init creates them.
var boxDirs = []string{"tmp", "new", "ready", "cur", "quarantine", "ack", "notices"}

// BoxInit is the scripts' box_init: the mailbox of name with its folders,
// none of them a link.
func (r *Root) BoxInit(name string) error {
	if !NameOK(name) {
		return &Fatal{Msg: "invalid message name"}
	}
	if !NoLinks(r.Path(name)) {
		return &Fatal{Msg: "mailbox symlink"}
	}
	for _, d := range boxDirs {
		p := r.Path(name, d)
		if !NoLinks(p) {
			return &Fatal{Msg: "mailbox state symlink"}
		}
		if err := os.MkdirAll(p, 0o700); err != nil {
			return &Fatal{Msg: fmt.Sprintf("mkdir: %s: %v", p, err)}
		}
	}
	return nil
}

// SafeTouch is the scripts' safe_touch: a safe leaf created or given the
// current time.
func (r *Root) SafeTouch(p string) error {
	if err := SafeLeaf(r.Dir, p); err != nil {
		return &Fatal{Msg: err.Error()}
	}
	if !Exists(p) {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return &Fatal{Msg: "touch: " + err.Error()}
		}
		_ = f.Close()
	}
	now := r.now()
	if err := os.Chtimes(p, now, now); err != nil {
		return &Fatal{Msg: "touch: " + err.Error()}
	}
	return nil
}

// LogLine is the scripts' log_line: "<now> <line>" appended to this
// process's diagnostic log under state/, which rotates past 1 MiB.
func (r *Root) LogLine(line string) error {
	log := r.Path("state", "diagnostic-"+strconv.Itoa(os.Getpid())+".log")
	for _, p := range []string{log, log + ".previous"} {
		if err := SafeLeaf(r.Dir, p); err != nil {
			return &Fatal{Msg: err.Error()}
		}
	}
	if st, err := os.Lstat(log); err == nil && st.Mode().IsRegular() && st.Size() > 1048576 {
		_ = os.Rename(log, log+".previous")
	}
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		_, err = fmt.Fprintf(f, "%d %s\n", r.Unix(), line)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return &Fatal{Msg: "cannot record courier diagnostics"}
	}
	return nil
}

// Kinds are the message kinds.
var Kinds = []string{"brief", "observation", "question", "result", "cancel", "user_answer", "receipt", "side_agent_data"}

// MaxBody bounds a message body: 64 KiB.
const MaxBody = 65536

// envelopeKeys are the keys of an envelope, sorted.
var envelopeKeys = []string{"body", "from", "from_identity", "id", "kind", "reply_to", "time", "to", "to_identity", "version"}

// Envelope is the scripts' envelope check of a queue file: bounded strict
// JSON with exactly the envelope keys, version 1, the id of its file name,
// party names, a known kind, an empty or valid reply id, an integral time,
// a body of at most 65536 characters, and two typed identities. It returns
// the decoded object.
func Envelope(root, p string) (map[string]any, bool) {
	b, ok := ReadStrict(root, p)
	if !ok {
		return nil, false
	}
	v, _ := Decode(b)
	m := Object(v)
	if m == nil || len(m) != len(envelopeKeys) {
		return nil, false
	}
	for _, k := range envelopeKeys {
		if _, ok := m[k]; !ok {
			return nil, false
		}
	}
	id, _ := String(m, "id")
	from, _ := String(m, "from")
	to, _ := String(m, "to")
	kind, _ := String(m, "kind")
	reply, rok := String(m, "reply_to")
	body, bok := String(m, "body")
	t, tok := Int(m, "time")
	if !Equal(m["version"], jsonOne) || id != strings.TrimSuffix(filepath.Base(p), ".md") || !IDOK(id) ||
		!NameShape(from) || !NameShape(to) || !slices.Contains(Kinds, kind) ||
		!rok || (reply != "" && !IDOK(reply)) || !tok || t < 0 || !bok || utf8.RuneCountInString(body) > MaxBody {
		return nil, false
	}
	for _, k := range []string{"from_identity", "to_identity"} {
		if !envelopeIdentity(m[k]) {
			return nil, false
		}
	}
	return m, true
}

var jsonOne, _ = Decode([]byte("1"))

func envelopeIdentity(v any) bool {
	id := Object(v)
	if id == nil || len(id) != 2 || !NonEmpty(id, "pane") {
		return false
	}
	s, ok := id["session"].(map[string]any)
	if !ok || len(s) != 4 {
		return false
	}
	for _, k := range []string{"agent", "kind", "source", "value"} {
		if _, ok := s[k]; !ok {
			return false
		}
	}
	value, _ := String(s, "value")
	return s["kind"] == "id" && SessionValueOK(value) && NonEmpty(s, "agent") && NonEmpty(s, "source")
}

// mdEntries lists the names of dir that the glob dir/*.md matches: no
// leading dot, sorted. A missing folder has none.
func mdEntries(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".md") && !strings.HasPrefix(n, ".") {
			out = append(out, n)
		}
	}
	return out
}

// MdFiles is the glob dir/*.md as the scripts loop over it: the matching
// paths, sorted.
func MdFiles(dir string) []string {
	var out []string
	for _, n := range mdEntries(dir) {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// CountState is the scripts' count_state: the .md entries of a folder,
// links included.
func CountState(dir string) int { return len(mdEntries(dir)) }

// Unacked is the scripts' unacked: the ready and cur messages of a mailbox
// without a valid ack.
func (r *Root) Unacked(box string) []string {
	var out []string
	for _, d := range []string{"ready", "cur"} {
		for _, f := range MdFiles(filepath.Join(box, d)) {
			if _, err := os.Stat(f); err != nil {
				continue
			}
			if !r.AckValid(box, strings.TrimSuffix(filepath.Base(f), ".md")) {
				out = append(out, f)
			}
		}
	}
	return out
}

// catText is a file's text as $(cat file) gives it: trailing newlines cut.
func catText(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimRight(string(b), "\n")
}

// CatText reads a file as $(cat file): its text without trailing newlines.
func CatText(p string) string { return catText(p) }

// AckValid is the scripts' ack_valid: a strict ack record for the id with
// a claim, a time, a processing record and the checksum of the message in
// cur, whose claim is the one recorded under ids/<id>/claim; no link on the
// way.
func (r *Root) AckValid(box, id string) bool {
	if !NoLinks(box, filepath.Join(box, "ack"), filepath.Join(box, "cur"), r.Path("ids", id)) {
		return false
	}
	b, ok := ReadStrict(r.Dir, filepath.Join(box, "ack", id+".md.json"))
	if !ok {
		return false
	}
	v, _ := Decode(b)
	a := Object(v)
	if a == nil {
		return false
	}
	_, timeOK := a["time"].(json.Number)
	sum, sumOK := String(a, "envelope_checksum")
	if a["id"] != id || !NonEmpty(a, "claim") || !timeOK || !NonEmpty(a, "processing_record") || !sumOK {
		return false
	}
	cur := filepath.Join(box, "cur", id+".md")
	if !Bounded(r.Dir, cur) {
		return false
	}
	if d, err := DigestFile(cur); err != nil || d != sum {
		return false
	}
	claim := r.Path("ids", id, "claim")
	return Regular(r.Dir, claim) && a["claim"] == catText(claim)
}

// ClaimValid is the scripts' claim_valid: a mailbox's claim folder, not a
// link, with a regular token and owner.
func (r *Root) ClaimValid(box string) bool {
	tok := filepath.Join(box, "claim", "token")
	st, err := os.Lstat(filepath.Join(box, "claim"))
	return GuardPath(r.Dir, tok) && err == nil && st.IsDir() && Regular(r.Dir, tok) && Regular(r.Dir, filepath.Join(box, "claim", "owner"))
}

// The checksum shape of the registry commands: "crc-size".
var checksumRe = regexp.MustCompile(`^[0-9]+-[0-9]+$`)

// ChecksumShape reports whether s looks like a digest.
func ChecksumShape(s string) bool { return checksumRe.MatchString(s) }

// Adopt is the handle of a lock folder the caller created itself (the
// courier's and the watcher's singleton locks), released as any lock.
func Adopt(dir string) *Lock { return &Lock{dir: dir} }
