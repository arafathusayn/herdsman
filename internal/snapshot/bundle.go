package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// gitBinary is a variable so tests can plant a failing git.
var gitBinary = "git"

// These operating-system calls are variables so tests can make each failure
// happen: this code authorizes deletion, so every failure path is exercised.
var (
	osAbs       = filepath.Abs
	osChmod     = os.Chmod
	osLstat     = os.Lstat
	osMkdir     = os.Mkdir
	osOpen      = os.Open
	osOpenRoot  = os.OpenRoot
	osReadlink  = os.Readlink
	osRemoveAll = os.RemoveAll
	osRename    = os.Rename
	osRmdir     = syscall.Rmdir
	osStat      = os.Stat
	syncPath    = syncToDisk
)

// syncToDisk flushes a file or a folder's entries to stable storage.
func syncToDisk(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// repoEnvVars locate a repository. A hook or a parent git process may set
// them, and they would point these commands at the wrong one.
var repoEnvVars = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_COMMON_DIR": true, "GIT_CONFIG": true,
	"GIT_CONFIG_COUNT": true, "GIT_CONFIG_PARAMETERS": true, "GIT_DIR": true, "GIT_GRAFT_FILE": true,
	"GIT_IMPLICIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_NAMESPACE": true,
	"GIT_NO_REPLACE_OBJECTS": true, "GIT_OBJECT_DIRECTORY": true, "GIT_PREFIX": true,
	"GIT_REPLACE_REF_BASE": true, "GIT_SHALLOW_FILE": true, "GIT_WORK_TREE": true,
}

func gitEnviron(extra []string) []string {
	env := make([]string, 0, len(extra)+16)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !repoEnvVars[name] {
			env = append(env, kv)
		}
	}
	return append(append(env, "LC_ALL=C"), extra...)
}

func gitCommand(ctx context.Context, dir string, extraEnv []string, args ...string) *exec.Cmd {
	// A replacement ref would make git read different bytes than were stored.
	full := append([]string{"--no-replace-objects", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, gitBinary, full...)
	cmd.Dir = dir
	cmd.Env = gitEnviron(extraEnv)
	return cmd
}

func runGit(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, dir, extraEnv, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func gitText(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := runGit(ctx, dir, nil, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// gitPath returns a path result. A path may end in whitespace, so only git's
// own line terminator is removed.
func gitPath(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := runGit(ctx, dir, nil, nil, args...)
	return strings.TrimSuffix(string(out), "\n"), err
}

// splitNUL parses `-z` output, whose records each end in NUL.
func splitNUL(raw []byte) []string {
	text := strings.TrimSuffix(string(raw), "\x00")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\x00")
}

// cQuote quotes a path for git's line-based --stdin-paths input, whose reader
// ends a line at a newline and strips a carriage return before it. Every
// control character is quoted so no name depends on such line handling.
func cQuote(p string) string {
	var out strings.Builder
	quoted := false
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '"' || c == '\\':
			out.WriteByte('\\')
			out.WriteByte(c)
			quoted = true
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&out, `\%03o`, c)
			quoted = true
		default:
			out.WriteByte(c)
		}
	}
	if !quoted {
		return p
	}
	return `"` + out.String() + `"`
}

// createBundle writes the complete bundle: every ref and object, no basis.
// Older snapshot refs of the repository stay out, each has its own bundle.
func createBundle(ctx context.Context, root, file, snapshotRef string) error {
	_, err := runGit(ctx, root, nil, nil, "bundle", "create", "-q", file,
		"--exclude=refs/herdsman/snapshot/*", "--all", snapshotRef)
	return err
}

// checksum returns the SHA-256 and size of a file.
func checksum(file string) (string, int64, error) {
	f, err := osOpen(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func artifactFor(file, name string) (Artifact, error) {
	sum, size, err := checksum(file)
	return Artifact{Name: name, SHA256: sum, Size: size}, err
}
