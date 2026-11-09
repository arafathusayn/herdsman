package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// SmokeFunc runs a candidate binary and returns what `version` printed.
type SmokeFunc func(ctx context.Context, path string) (string, error)

// Smoke runs `<path> version` with a time limit.
func Smoke(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// managedDirs are folders owned by a package manager: replacing a file there
// would leave the manager's records wrong.
var managedDirs = []string{"Caskroom", "Cellar", "nix", ".nix-profile"}

func managed(path string) bool {
	return slices.ContainsFunc(strings.Split(filepath.ToSlash(path), "/"), func(p string) bool {
		return slices.Contains(managedDirs, p)
	})
}

// Replace installs binary over the file at exe, following links to the real
// file. The candidate is written beside it and must report version before an
// atomic rename, so a failure at any step leaves the old binary in place.
func Replace(ctx context.Context, exe string, binary []byte, version string, smoke SmokeFunc) (err error) {
	target, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("find the running binary: %w", err)
	}
	if managed(target) {
		return fmt.Errorf("%w (%s): upgrade it there, for example with brew upgrade --cask herdsman", ErrManaged, target)
	}
	st, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("find the running binary: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".herdsman-update-*")
	if err != nil {
		return fmt.Errorf("write beside the binary: %w", err)
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	_, err = tmp.Write(binary)
	if err == nil {
		err = tmp.Chmod(st.Mode().Perm())
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write the new binary: %w", err)
	}
	out, err := smoke(ctx, tmp.Name())
	if err != nil {
		return fmt.Errorf("the new binary does not run: %w", err)
	}
	if !reportsVersion(out, version) {
		err = fmt.Errorf("the new binary reports %q, not version %s", strings.TrimSpace(out), version)
		return err
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("replace the binary: %w", err)
	}
	return syncDir(filepath.Dir(target))
}

func reportsVersion(out, version string) bool {
	return regexp.MustCompile(`(^|\s)` + regexp.QuoteMeta(version) + `(\s|$)`).MatchString(out)
}

// syncDir makes the rename durable. Some filesystems refuse to sync a folder;
// the rename has happened by then, so only a real I/O error counts.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}
