package daemon

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// writeDurable writes data to path atomically and durably: a temporary file
// in the same folder, fsync, rename over path, fsync of the folder (design
// 4.2 steps 2 and 4). The folder must exist and be a real folder.
func writeDurable(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("write %s: folder missing or not a folder", path)
	}
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return fmt.Errorf("write %s: not a regular file", path)
	}
	f, err := os.CreateTemp(dir, ".daemon.")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Chmod(perm)
	}
	if werr == nil {
		werr = fsync(f)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	if err := rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

// fsync makes a file's or a folder's data durable. Only the memory and
// goroutine loop tests (A24.3), which measure neither, replace it.
var fsync = (*os.File).Sync

// syncDir fsyncs a folder, so a rename or a new entry in it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = fsync(d)
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// mkdirs creates folders with mode 700, refusing a link on the way below
// root.
func mkdirs(root string, rel ...string) error {
	for _, r := range rel {
		p := root
		for part := range strings.SplitSeq(r, "/") {
			p = filepath.Join(p, part)
			st, err := os.Lstat(p)
			if err == nil {
				if !st.IsDir() {
					return fmt.Errorf("%s: not a real folder", p)
				}
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Mkdir(p, 0o700); err != nil {
				if errors.Is(err, os.ErrExist) {
					continue
				}
				return err
			}
			// The new folder's entry is durable in its parent.
			if err := syncDir(filepath.Dir(p)); err != nil {
				return err
			}
		}
	}
	return nil
}

// rename is os.Rename; tests replace it to inject a failure or a crash.
var rename = os.Rename

// renameDurable renames and fsyncs the folders of both sides, so the move
// survives a power loss once it returns.
func renameDurable(src, dst string) error {
	if err := rename(src, dst); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	if filepath.Dir(src) != filepath.Dir(dst) {
		return syncDir(filepath.Dir(src))
	}
	return nil
}

// mkdirDurable creates one folder, failing if it exists, and fsyncs its
// parent.
func mkdirDurable(p string) error {
	if err := os.Mkdir(p, 0o700); err != nil {
		return err
	}
	return syncDir(filepath.Dir(p))
}

// exists reports whether p is present (a link included).
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// regular reports whether p is a regular file, not a link.
func regular(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

// readSmall reads a regular file of at most max bytes.
func readSmall(p string, max int64) ([]byte, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", p)
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%s: larger than %d bytes", p, max)
	}
	return os.ReadFile(p)
}

// randomHex is n random bytes as hex.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sha256Hex is the SHA-256 of b in hex.
func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// encode is compact JSON without HTML escaping and without a newline.
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// prettyJSON is jq's default output, as the file-mode commands write the
// queue files: two-space indent and a final newline.
func prettyJSON(v any) []byte {
	c, _ := encode(v)
	var b bytes.Buffer
	_ = json.Indent(&b, c, "", "  ")
	b.WriteByte('\n')
	return b.Bytes()
}

// ordered is a JSON object that keeps its key order.
type ordered []kvPair

type kvPair struct {
	K string
	V any
}

func obj(pairs ...any) ordered {
	o := make(ordered, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		o = append(o, kvPair{pairs[i].(string), pairs[i+1]})
	}
	return o
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := encode(p.K)
		b.Write(k)
		b.WriteByte(':')
		v, err := encode(p.V)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// strictDecode decodes strict JSON (duplicate keys and trailing data
// refused, as inbox.StrictJSON) into v, refusing unknown fields.
func strictDecode(b []byte, v any) error {
	if err := inbox.StrictJSON(b); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// inode is a file's inode number (0 when the system gives none).
func inode(st os.FileInfo) uint64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Ino)
	}
	return 0
}
