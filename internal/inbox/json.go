// Package inbox is the core of the adjutant message channel, the private
// folder where the adjutant and the orchestrator leave each other messages
// (the skill's references/messages.md): strict JSON, the size bound, the
// POSIX cksum digest, the name and id rules, the path and symlink guards,
// the mkdir+pid locks, the registry, the live identity check, the setup
// proof and the authority decision. It keeps the rules, paths and formats
// of the old channel library, so the old scripts and the herdsman
// binary can share one inbox.
//
// The read side is safe for any reader (the web app uses it). The write
// paths of the channel (send, read, ack, courier) are not here yet.
package inbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// MaxSize bounds every state file the channel reads: 128 KiB, as the
// scripts' `bounded` does.
const MaxSize = 131072

// beforeOpen runs just before ReadBounded opens its file; tests replace or
// grow the file there.
var beforeOpen func(p string)

// ReadBounded reads a regular, non-link file of at most max bytes. The
// checks hold for the file actually read: it is opened once without
// following a link (and without blocking on a fifo), its type is checked
// on the open file, and at most max+1 bytes are read, so a file replaced
// or grown after an earlier check of its path is refused, never read whole.
func ReadBounded(p string, max int64) ([]byte, error) {
	if beforeOpen != nil {
		beforeOpen(p)
	}
	f, err := openRegular(p, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errNotBounded
	}
	return b, nil
}

var errNotBounded = errors.New("not a bounded regular file")

// openRegular opens p for reading without blocking on a fifo and refuses
// what the opened descriptor shows is not a regular file, so a path
// replaced after an earlier check is refused, never waited on. follow
// lets a link through, as cat and cksum do; otherwise a link is refused.
func openRegular(p string, follow bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if !follow {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(p, flags, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = errNotBounded
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ReadFile is os.ReadFile of a regular file, after links: a fifo, a device
// or a folder is refused, never waited on.
func ReadFile(p string) ([]byte, error) {
	f, err := openRegular(p, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// readState is a state file checked earlier, read again: bounded and
// regular on the file read, nil when it is not.
func readState(p string) []byte {
	b, err := ReadBounded(p, MaxSize)
	if err != nil {
		return nil
	}
	return b
}

// StrictJSON fails on malformed JSON, trailing data, an empty input and
// duplicate keys at any depth (escaped spellings of one key included), as
// the channel's `jq --stream` check does.
func StrictJSON(b []byte) error {
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

// KeysExactly reports whether raw is an object with exactly these keys.
func KeysExactly(raw json.RawMessage, want ...string) bool {
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

// Decode reads JSON with numbers kept as json.Number, so integer checks
// and equality see the written value.
func Decode(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// Object returns v as an object, or nil.
func Object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// String returns the string at key k of m, and whether it is one.
func String(m map[string]any, k string) (string, bool) {
	s, ok := m[k].(string)
	return s, ok
}

// NonEmpty reports whether m[k] is a non-empty string.
func NonEmpty(m map[string]any, k string) bool {
	s, ok := m[k].(string)
	return ok && s != ""
}

// True reports whether m[k] is the boolean true.
func True(m map[string]any, k string) bool {
	b, ok := m[k].(bool)
	return ok && b
}

// Int returns m[k] when it is an integral number (as jq's `floor==.`).
func Int(m map[string]any, k string) (int64, bool) {
	n, ok := m[k].(json.Number)
	if !ok {
		return 0, false
	}
	if i, err := n.Int64(); err == nil {
		return i, true
	}
	f, err := n.Float64()
	if err != nil || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}

// Equal compares two decoded JSON values as jq's == does: objects by keys
// and values whatever their order, numbers by value.
func Equal(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !Equal(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		fx, ex := x.Float64()
		fy, ey := y.Float64()
		return ex == nil && ey == nil && fx == fy
	default:
		return a == b
	}
}

// CheckDir checks a folder the way a reader does before it trusts it as an
// inbox: absolute, a real folder, mode 700.
func CheckDir(root string) error {
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
