// Package gitrefs reads a repository's refs and worktree heads without forking
// git per branch.
//
// In a files-backend repository the loose refs, packed-refs, every HEAD and the
// config are read directly. Whatever the reader does not recognize (a reftable
// repository, an unknown extension, a symlinked ref, a malformed file) is
// answered by one git process instead, so an unusual repository costs a fork
// and never a wrong answer.
//
// A ref's object is not opened: the reader reports what the ref stores, as
// git for-each-ref does for %(objectname).
package gitrefs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
)

// ErrNotRepository reports a path that is neither a working tree root nor a
// git directory. Open does not search parent directories.
var ErrNotRepository = errors.New("gitrefs: not a git repository")

// Ref is one reference as git for-each-ref reports it.
type Ref struct {
	Name   string // Full name, such as refs/heads/main.
	Object string // Lowercase hex object id the ref resolves to.
	Symref string // Final target when Name is symbolic; otherwise empty.
}

// HeadState says what a worktree's HEAD points at.
type HeadState int

const (
	HeadAttached HeadState = iota + 1 // On a branch that exists.
	HeadUnborn                        // On a branch with no commit yet.
	HeadDetached                      // At a commit, on no branch.
	HeadBare                          // The main entry of a bare repository.
)

// Head is one worktree of the repository.
type Head struct {
	Path   string // The worktree's root; the git directory when bare.
	State  HeadState
	Branch string // Full name of the branch; empty when detached or bare.
	Object string // Commit HEAD resolves to; empty when unborn, bare or broken.
}

// Reader reads one repository, from the point of view of the worktree it was
// opened on. It is safe for concurrent use.
type Reader struct {
	gitDir    string
	commonDir string
	linked    bool
	hexLen    int
	bare      bool
	packed    *packedCache
	fallback  atomic.Pointer[string]
}

// Open returns a Reader for a working tree root or a git directory.
func Open(path string) (*Reader, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		resolved, err = filepath.Abs(resolved)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotRepository, path)
	}
	gitDir, ok := findGitDir(resolved)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotRepository, path)
	}
	commonDir := findCommonDir(gitDir)
	r := &Reader{gitDir: gitDir, commonDir: commonDir, linked: gitDir != commonDir}
	config, err := loadRepoConfig(filepath.Join(commonDir, "config"))
	if err != nil {
		r.fallBack(err)
		return r, nil
	}
	r.hexLen, r.bare = config.hexLen, config.bare
	r.packed = &packedCache{path: filepath.Join(commonDir, "packed-refs"), hexLen: config.hexLen}
	return r, nil
}

func findGitDir(path string) (string, bool) {
	dotGit := filepath.Join(path, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return path, isGitDir(path)
	}
	if info.IsDir() {
		return dotGit, isGitDir(dotGit)
	}
	data, _ := os.ReadFile(dotGit)
	target, ok := strings.CutPrefix(string(data), "gitdir:")
	if !ok {
		return "", false
	}
	gitDir := joinFrom(path, strings.TrimSpace(target))
	return gitDir, isGitDir(gitDir)
}

func findCommonDir(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	return joinFrom(gitDir, strings.TrimSpace(string(data)))
}

func isGitDir(path string) bool {
	return exists(filepath.Join(path, "HEAD")) &&
		(exists(filepath.Join(path, "refs")) || exists(filepath.Join(path, "commondir")))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func joinFrom(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}

// FallbackReason is empty while the reader reads the repository directly, and
// otherwise says why it now answers through git.
func (r *Reader) FallbackReason() string {
	if reason := r.fallback.Load(); reason != nil {
		return *reason
	}
	return ""
}

func (r *Reader) fallBack(cause error) {
	reason := cause.Error()
	r.fallback.Store(&reason)
}

// Refs lists every ref in byte order of name, with the content git
// for-each-ref shows from this worktree. Broken refs are left out.
func (r *Reader) Refs(ctx context.Context) ([]Ref, error) {
	if r.FallbackReason() == "" {
		refs, err := r.readRefs()
		if err == nil {
			return refs, nil
		}
		r.fallBack(err)
	}
	return r.forkRefs(ctx)
}

// Heads lists the repository's worktrees, the main one first and the linked
// ones by path, as git worktree list does.
func (r *Reader) Heads(ctx context.Context) ([]Head, error) {
	if r.FallbackReason() == "" {
		heads, err := r.readHeads()
		if err == nil {
			return heads, nil
		}
		r.fallBack(err)
	}
	return r.forkHeads(ctx)
}

func (r *Reader) readHeads() ([]Head, error) {
	var heads []Head
	mainPath := strings.TrimSuffix(r.commonDir, "/.git")
	if r.bare {
		heads = append(heads, Head{Path: mainPath, State: HeadBare})
	} else {
		head, ok, err := r.readHead(r.commonDir, mainPath)
		if err != nil {
			return nil, err
		}
		if ok {
			heads = append(heads, head)
		}
	}
	linked, err := r.readLinkedHeads()
	if err != nil {
		return nil, err
	}
	return append(heads, linked...), nil
}

func (r *Reader) readLinkedHeads() ([]Head, error) {
	root := filepath.Join(r.commonDir, "worktrees")
	entries, err := os.ReadDir(root)
	if isAbsent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var heads []Head
	for _, entry := range entries {
		gitDir := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
		if err != nil {
			continue
		}
		dotGit := joinFrom(gitDir, strings.TrimSpace(string(data)))
		head, ok, err := r.readHead(gitDir, filepath.Dir(dotGit))
		if err != nil {
			return nil, err
		}
		if ok {
			heads = append(heads, head)
		}
	}
	slices.SortFunc(heads, func(a, b Head) int { return strings.Compare(a.Path, b.Path) })
	return heads, nil
}

// readHead reports false when the worktree has no HEAD file, which is a
// worktree being removed.
func (r *Reader) readHead(gitDir, path string) (Head, bool, error) {
	final, object, result, err := r.storeFor(gitDir).resolve("HEAD")
	switch {
	case err != nil:
		return Head{}, false, err
	case result == refBroken:
		return Head{}, false, fmt.Errorf("gitrefs: broken HEAD in %s", gitDir)
	case result == refAbsent && final == "HEAD":
		return Head{}, false, nil
	case result == refAbsent:
		return Head{Path: path, State: HeadUnborn, Branch: final}, true, nil
	case final == "HEAD":
		return Head{Path: path, State: HeadDetached, Object: object}, true, nil
	}
	return Head{Path: path, State: HeadAttached, Branch: final, Object: object}, true, nil
}

// repoConfig is what the reader needs from the repository's config file.
type repoConfig struct {
	hexLen int
	bare   bool
}

func loadRepoConfig(path string) (repoConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return repoConfig{}, fmt.Errorf("gitrefs: cannot read config: %w", err)
	}
	return parseRepoConfig(string(data))
}

// parseRepoConfig accepts only what it fully understands. git itself reads the
// repository format from this one file without following includes, but
// core.bare can come from an include, so an include is a reason to fall back.
func parseRepoConfig(data string) (repoConfig, error) {
	settings, err := scanConfig(data)
	if err != nil {
		return repoConfig{}, err
	}
	bare, ok := settings["core.bare"]
	if !ok {
		return repoConfig{}, errors.New("gitrefs: core.bare is unset")
	}
	if _, set := settings["core.worktree"]; set {
		return repoConfig{}, errors.New("gitrefs: core.worktree is set")
	}
	config := repoConfig{hexLen: 40}
	if config.bare, ok = parseBool(bare); !ok {
		return repoConfig{}, fmt.Errorf("gitrefs: core.bare is %q", bare)
	}
	version := settings["core.repositoryformatversion"]
	if version != "" && version != "0" && version != "1" {
		return repoConfig{}, fmt.Errorf("gitrefs: core.repositoryformatversion is %q", version)
	}
	for key, value := range settings {
		name, isExtension := strings.CutPrefix(key, "extensions.")
		if !isExtension {
			continue
		}
		switch name {
		case "noop", "preciousobjects", "partialclone", "relativeworktrees":
		case "refstorage":
			if value != "files" {
				return repoConfig{}, fmt.Errorf("gitrefs: ref storage is %s", value)
			}
		case "objectformat":
			if version != "1" || (value != "sha1" && value != "sha256") {
				return repoConfig{}, fmt.Errorf("gitrefs: objectformat %q at version %q", value, version)
			}
			if value == "sha256" {
				config.hexLen = 64
			}
		default:
			return repoConfig{}, fmt.Errorf("gitrefs: unknown %s", key)
		}
	}
	return config, nil
}

// scanConfig returns the last value of each key in [core] and [extensions].
func scanConfig(data string) (map[string]string, error) {
	settings := map[string]string{}
	section := ""
	for raw := range strings.SplitSeq(data, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasSuffix(line, "\\") {
			return nil, errors.New("gitrefs: config line continuation")
		}
		if strings.HasPrefix(line, "[") {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return nil, errors.New("gitrefs: config section is not closed")
			}
			section = strings.ToLower(strings.TrimSpace(line[1:end]))
			if strings.HasPrefix(section, "include") {
				return nil, errors.New("gitrefs: config uses include")
			}
			line = strings.TrimSpace(line[end+1:])
		}
		if line == "" || line[0] == '#' || line[0] == ';' || (section != "core" && section != "extensions") {
			continue
		}
		key, value, hasValue := strings.Cut(line, "=")
		if !hasValue {
			value = "true"
		}
		settings[section+"."+strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return settings, nil
}

func parseBool(value string) (result, ok bool) {
	switch strings.ToLower(value) {
	case "true", "yes", "on", "1":
		return true, true
	case "false", "no", "off", "0":
		return false, true
	}
	return false, false
}
