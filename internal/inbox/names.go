package inbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	nameRe         = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	idRe           = regexp.MustCompile(`^m[0-9]{12}$`)
	sessionValueRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// The channel's own entries at the inbox root; no peer or mailbox may take these names.
	reserved = map[string]bool{"peers": true, "peers.approved": true, "state": true, "ids": true, "alerts": true, "enrolled": true, "card-events": true,
		"courier.lock": true, "watch.lock": true, "channel.lock": true, "herdr-mutation.lock": true, "setup": true}
)

// NameShape reports whether s has the shape of a party name
// (^[a-z][a-z0-9_-]{0,31}$), reserved names included.
func NameShape(s string) bool { return nameRe.MatchString(s) }

// Reserved reports whether s is one of the inbox's own entries.
func Reserved(s string) bool { return reserved[s] }

// NameOK is the scripts' name_ok: a name of the right shape that is not
// reserved.
func NameOK(s string) bool { return nameRe.MatchString(s) && !reserved[s] }

// IDOK is the scripts' id_ok: a message id, "m" and twelve digits.
func IDOK(s string) bool { return idRe.MatchString(s) }

// SessionValueOK reports whether s is a usable session id.
func SessionValueOK(s string) bool { return sessionValueRe.MatchString(s) }

// Digest is POSIX cksum's CRC and size, "crc-size", the channel's digest.
// It detects accidental drift, never forgery.
func Digest(b []byte) string {
	var crc uint32
	step := func(c byte) {
		crc ^= uint32(c) << 24
		for range 8 {
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

// DigestFile is Digest of a file's bytes.
func DigestFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return Digest(b), nil
}

// Uint is the scripts' uint check: a decimal of at most eight digits, no
// leading zero, between lo and hi.
func Uint(name, v string, lo, hi int) (int, error) {
	ok := v == "0" || (len(v) >= 1 && len(v) <= 8 && v[0] >= '1' && v[0] <= '9')
	for _, c := range v {
		if c < '0' || c > '9' {
			ok = false
		}
	}
	n, _ := strconv.Atoi(v)
	if !ok || n < lo || n > hi {
		return 0, fmt.Errorf("invalid %s: %s", name, v)
	}
	return n, nil
}

// IdentityOK checks an identity object: exactly a pane and a session, the
// session exactly agent, kind, source and value, kind "id" and a usable
// session id.
func IdentityOK(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if !KeysExactly(raw, "pane", "session") || json.Unmarshal(raw, &m) != nil {
		return false
	}
	if !KeysExactly(m["session"], "agent", "kind", "source", "value") {
		return false
	}
	var id struct {
		Pane    string `json:"pane"`
		Session struct {
			Agent  string `json:"agent"`
			Kind   string `json:"kind"`
			Source string `json:"source"`
			Value  string `json:"value"`
		} `json:"session"`
	}
	if json.Unmarshal(raw, &id) != nil {
		return false
	}
	s := id.Session
	return id.Pane != "" && s.Kind == "id" && sessionValueRe.MatchString(s.Value) && s.Agent != "" && s.Source != ""
}

// NoLinks reports whether none of the paths is a symbolic link; a missing
// path passes.
func NoLinks(paths ...string) bool {
	for _, p := range paths {
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// Exists reports whether p is present, a link included.
func Exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// RegularFile reports whether p is a regular file and not a link.
func RegularFile(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

// RealDirs reports whether each folder from root down to the last of parts
// is a folder and not a link, as guard_path checks a file's parents.
func RealDirs(root string, parts ...string) bool {
	p := root
	for _, part := range parts {
		p = filepath.Join(p, part)
		if st, err := os.Lstat(p); err != nil || !st.IsDir() {
			return false
		}
	}
	return true
}

// GuardPath is the scripts' guard_path: a path under root ("<root>/...")
// passes only when every folder between it and root is a real folder, not
// a link; a path elsewhere passes.
func GuardPath(root, p string) bool {
	if root == "" || !strings.HasPrefix(p, root+"/") {
		return true
	}
	dir := p[:strings.LastIndex(p, "/")]
	for dir != root {
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() {
			return false
		}
		// dir keeps the prefix root+"/" until it is root, so it has a slash.
		dir = dir[:strings.LastIndex(dir, "/")]
	}
	return true
}

// Regular is the scripts' regular: a readable regular file that is not a
// link, whose folders under root are real (root "" skips that part).
func Regular(root, p string) bool {
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return GuardPath(root, p)
}

// Bounded is Regular and at most MaxSize bytes.
func Bounded(root, p string) bool {
	if !Regular(root, p) {
		return false
	}
	st, err := os.Lstat(p)
	return err == nil && st.Size() <= MaxSize
}

// ReadStrict reads a file the way the scripts' strict_json accepts one:
// bounded, regular, guarded, and strict JSON. The path checks are
// Regular's; the type and the bound are checked again on the file read
// (ReadBounded), so a file replaced or grown after the checks is refused.
func ReadStrict(root, p string) ([]byte, bool) {
	if !Bounded(root, p) {
		return nil, false
	}
	b, err := ReadBounded(p, MaxSize)
	if err != nil || StrictJSON(b) != nil {
		return nil, false
	}
	return b, true
}

// SafeLeaf is the scripts' safe_leaf: a state file under guarded folders
// that is absent or a regular file. The error is the scripts' message.
func SafeLeaf(root, p string) error {
	if GuardPath(root, p) && (!Exists(p) || Regular(root, p)) {
		return nil
	}
	return fmt.Errorf("nonregular state leaf: %s", p)
}
