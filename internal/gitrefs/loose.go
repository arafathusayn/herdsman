package gitrefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// maxSymrefReads matches git's SYMREF_MAXDEPTH: a name is read at most this
// many times while following symbolic refs.
const maxSymrefReads = 5

var perWorktreeRoots = []string{"refs/bisect", "refs/rewritten", "refs/worktree"}

// looseValue is the parsed content of one loose ref file.
type looseValue struct {
	object string
	symref string
	broken bool
}

type refResult int

const (
	refResolved refResult = iota
	refAbsent
	refBroken
)

// refStore reads refs from the point of view of one worktree.
type refStore struct {
	gitDir    string
	commonDir string
	hexLen    int
	packed    *packedCache
}

func (r *Reader) storeFor(state *repoState, gitDir string) refStore {
	return refStore{gitDir: gitDir, commonDir: r.commonDir, hexLen: state.hexLen, packed: state.packed}
}

// readRefs reads loose refs before packed-refs, and that order is what makes
// it safe against a running pack-refs: git writes the new packed-refs before
// it removes a loose file, so a ref missing from the loose walk is in any
// packed-refs read afterwards.
func (r *Reader) readRefs(state *repoState) ([]Ref, error) {
	store := r.storeFor(state, r.gitDir)
	loose, err := r.scanLoose(state.hexLen)
	if err != nil {
		return nil, err
	}
	packed, err := state.packed.current()
	if err != nil {
		return nil, err
	}
	refs := make([]Ref, 0, len(loose)+len(packed))
	for name, value := range loose {
		if value.broken || !validRefname(name) {
			continue
		}
		if value.symref == "" {
			refs = append(refs, Ref{Name: name, Object: value.object})
			continue
		}
		final, object, result, err := store.resolve(name)
		if err != nil {
			return nil, err
		}
		if result == refResolved {
			ref := Ref{Name: name, Object: object}
			if final != name {
				ref.Symref = final
			}
			refs = append(refs, ref)
		}
	}
	for _, entry := range packed {
		if _, shadowed := loose[entry.name]; !shadowed {
			refs = append(refs, Ref{Name: entry.name, Object: entry.object})
		}
	}
	slices.SortFunc(refs, func(a, b Ref) int { return strings.Compare(a.Name, b.Name) })
	return refs, nil
}

func (r *Reader) scanLoose(hexLen int) (map[string]looseValue, error) {
	found := map[string]looseValue{}
	if err := walkLoose(r.commonDir, "refs", hexLen, r.linked, found); err != nil {
		return nil, err
	}
	if !r.linked {
		return found, nil
	}
	for _, root := range perWorktreeRoots {
		if err := walkLoose(r.gitDir, root, hexLen, false, found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

// walkLoose follows git's rules for a refs directory: dot files and *.lock are
// not refs, and a file that vanishes mid-walk has been packed or deleted. With
// skipPerWorktree the private folders are left out; a plain file with one of
// those names is an ordinary shared ref.
func walkLoose(root, dir string, hexLen int, skipPerWorktree bool, found map[string]looseValue) error {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if isAbsent(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		base := entry.Name()
		name := dir + "/" + base
		if base[0] == '.' || strings.HasSuffix(base, ".lock") {
			continue
		}
		if entry.IsDir() {
			if skipPerWorktree && slices.Contains(perWorktreeRoots, name) {
				continue
			}
			if err := walkLoose(root, name, hexLen, skipPerWorktree, found); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("gitrefs: %s is a symlink or special file", name)
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil && !isAbsent(err) {
			return err
		}
		if err == nil {
			found[name] = parseLoose(data, hexLen)
		}
	}
	return nil
}

func isPerWorktree(name string) bool {
	for _, root := range perWorktreeRoots {
		if strings.HasPrefix(name, root+"/") {
			return true
		}
	}
	return false
}

// isAbsent reports a path that does not hold a file: never created, removed
// by a concurrent git, or a directory in the place of a ref.
func isAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.EISDIR)
}

func parseLoose(data []byte, hexLen int) looseValue {
	content := strings.TrimRight(string(data), gitSpace)
	if target, ok := strings.CutPrefix(content, "ref:"); ok {
		target = strings.TrimLeft(target, gitSpace)
		return looseValue{symref: target, broken: target == ""}
	}
	if len(content) < hexLen || !isObjectID(content[:hexLen]) {
		return looseValue{broken: true}
	}
	if len(content) > hexLen && strings.IndexByte(gitSpace, content[hexLen]) < 0 {
		return looseValue{broken: true}
	}
	return looseValue{object: strings.ToLower(content[:hexLen])}
}

// isObjectID reports hex that names an object. The null id does not: git
// treats it as repository corruption.
func isObjectID(hex string) bool {
	return isHex(hex) && strings.Trim(hex, "0") != ""
}

func isHex(value string) bool {
	for i := range len(value) {
		c := value[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// resolve follows name to a final ref. It returns the last name reached, which
// on refAbsent is the unborn branch or dangling target.
func (s refStore) resolve(name string) (string, string, refResult, error) {
	for range maxSymrefReads {
		if !validRefname(name) {
			return name, "", refBroken, nil
		}
		value, found, err := s.readLoose(name)
		if err != nil {
			return name, "", refBroken, err
		}
		if !found {
			// Consulted after the loose read, for the reason on readRefs.
			packed, err := s.packed.current()
			if err != nil {
				return name, "", refBroken, err
			}
			if object, ok := lookupPacked(packed, name); ok {
				return name, object, refResolved, nil
			}
			return name, "", refAbsent, nil
		}
		if value.broken {
			return name, "", refBroken, nil
		}
		if value.symref == "" {
			return name, value.object, refResolved, nil
		}
		name = value.symref
	}
	return name, "", refBroken, nil
}

func (s refStore) readLoose(name string) (looseValue, bool, error) {
	path, err := s.pathOf(name)
	if err != nil {
		return looseValue{}, false, err
	}
	data, err := readNoFollow(path)
	if isAbsent(err) {
		return looseValue{}, false, nil
	}
	if errors.Is(err, syscall.ELOOP) {
		return looseValue{}, false, fmt.Errorf("gitrefs: %s is a symlink", name)
	}
	if err != nil {
		return looseValue{}, false, err
	}
	return parseLoose(data, s.hexLen), true, nil
}

// readNoFollow fails on a symlink: git gives a symlinked ref its own meaning,
// so the caller must fall back rather than read through it.
func readNoFollow(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func (s refStore) pathOf(name string) (string, error) {
	if strings.HasPrefix(name, "main-worktree/") || strings.HasPrefix(name, "worktrees/") {
		return "", fmt.Errorf("gitrefs: %s names another worktree's ref", name)
	}
	if !strings.HasPrefix(name, "refs/") || isPerWorktree(name) {
		return filepath.Join(s.gitDir, name), nil
	}
	return filepath.Join(s.commonDir, name), nil
}

// validRefname applies git's check_refname_format with one-level names allowed.
func validRefname(name string) bool {
	if name == "" || name == "@" || strings.HasSuffix(name, ".") ||
		strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c <= ' ' || c == 0x7f || strings.IndexByte("~^:?*[\\", c) >= 0 {
			return false
		}
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || part[0] == '.' || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
