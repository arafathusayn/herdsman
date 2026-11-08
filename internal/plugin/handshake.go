package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	sdkplugin "github.com/arafathusayn/herdsman/sdk/plugin"
)

// ErrNotApproved reports a plugin whose checksum no person has accepted.
var ErrNotApproved = errors.New("not approved")

// Snapshot copies the plugin's executable into a private folder, hashes the
// bytes it copied, and returns the copy's path if that hash is the approved
// one. Running the copy, never the original, binds the approval to what
// executes: a file swapped after discovery is either refused here or not
// looked at again. The caller runs the returned cleanup when the process ends.
//
// The copy holds only the executable, so a plugin cannot find files next to its
// own path. The folder lives beside the plugins, where programs can already run.
func (h *Host) Snapshot(found Found) (string, func(), error) {
	root := filepath.Join(h.ConfigDir, "plugin-snapshots")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, fmt.Errorf("plugin: snapshot folder: %w", err)
	}
	dir, err := os.MkdirTemp(root, "s")
	if err != nil {
		return "", nil, fmt.Errorf("plugin: snapshot folder: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, filepath.Base(found.Path))
	checksum, err := copyAndHash(found.Path, path)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("plugin: snapshot %s: %w", found.Name, err)
	}
	approved, err := h.Approvals().Approved(found.Name, checksum)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if !approved {
		cleanup()
		return "", nil, fmt.Errorf("%w: run herdsman plugin --experimental-plugins install %s on a terminal", ErrNotApproved, found.Name)
	}
	return path, cleanup, nil
}

func copyAndHash(source, target string) (string, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o500)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, hash), in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Start runs one plugin from a snapshot of its approved bytes. The SDK completes
// the handshake and checks the protocol major.
func (h *Host) Start(ctx context.Context, found Found) (*sdkplugin.Process, error) {
	path, cleanup, err := h.Snapshot(found)
	if err != nil {
		return nil, err
	}
	process, err := sdkplugin.Host{Name: found.Name, Timeout: h.Timeout, Env: h.Env}.Start(ctx, path)
	if err != nil {
		cleanup()
		// Name the file the user knows, not the temporary copy.
		if failure, ok := errors.AsType[*sdkplugin.HandshakeError](err); ok {
			return nil, &sdkplugin.HandshakeError{Path: found.Path, Reason: failure.Reason}
		}
		return nil, err
	}
	process.AfterClose = cleanup
	return process, nil
}
