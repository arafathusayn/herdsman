// Package plugin finds the executables that extend herdsman, starts the
// approved ones and holds the registry the built-ins and the plugins share.
package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdkplugin "github.com/arafathusayn/herdsman/sdk/plugin"
)

// ExecutablePrefix starts the file name of every plugin.
const ExecutablePrefix = "herdsman-plugin-"

// Host locates plugins and starts them.
type Host struct {
	// ConfigDir is herdsman's folder in the user's configuration.
	ConfigDir string
	// SearchPath is a PATH-style list of folders.
	SearchPath string
	// Timeout bounds each handshake; zero uses the SDK's default.
	Timeout time.Duration
	// Env is passed to every plugin on top of the SDK's small environment.
	Env []string
}

// DefaultHost uses the user's configuration folder and PATH.
func DefaultHost() *Host {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		// With no home folder the path is relative and simply holds no plugins.
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return &Host{ConfigDir: filepath.Join(base, "herdsman"), SearchPath: os.Getenv("PATH")}
}

// PluginsDir holds one folder per installed plugin.
func (h *Host) PluginsDir() string { return filepath.Join(h.ConfigDir, "plugins") }

// InstalledPath is where an installed plugin's executable lives.
func (h *Host) InstalledPath(name string) string {
	return filepath.Join(h.PluginsDir(), name, ExecutablePrefix+name)
}

// Approvals is the store of accepted checksums. It sits beside the plugin
// folder, not in it, so that removing a folder cannot touch it.
func (h *Host) Approvals() Approvals {
	return Approvals{Path: filepath.Join(h.ConfigDir, "plugin-approvals.json")}
}

// Found is an executable that may be a plugin.
type Found struct {
	Name     string
	Path     string
	Checksum string
}

func checksumFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// Discover lists plugins, the installed ones first. On a repeated name the
// first one found is the plugin, as it is for commands on PATH.
func (h *Host) Discover() ([]Found, error) {
	var found []Found
	seen := map[string]bool{}
	add := func(name, path string) {
		if seen[name] || !sdkplugin.ValidName(name) || !isExecutableFile(path) {
			return
		}
		checksum, err := checksumFile(path)
		if err != nil {
			return
		}
		seen[name] = true
		found = append(found, Found{Name: name, Path: path, Checksum: checksum})
	}
	entries, err := os.ReadDir(h.PluginsDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("plugin: read the plugin folder: %w", err)
	}
	for _, entry := range entries {
		add(entry.Name(), h.InstalledPath(entry.Name()))
	}
	for _, dir := range filepath.SplitList(h.SearchPath) {
		if dir == "" {
			continue
		}
		// A folder on PATH that cannot be read holds no plugin, as for a shell.
		files, _ := os.ReadDir(dir)
		for _, file := range files {
			if name, ok := strings.CutPrefix(file.Name(), ExecutablePrefix); ok {
				add(name, filepath.Join(dir, file.Name()))
			}
		}
	}
	return found, nil
}

// Find returns the plugin of that name.
func (h *Host) Find(name string) (Found, bool, error) {
	all, err := h.Discover()
	if err != nil {
		return Found{}, false, err
	}
	for _, found := range all {
		if found.Name == name {
			return found, true, nil
		}
	}
	return Found{}, false, nil
}

// Load starts every approved plugin and adds it to the registry. A plugin that
// cannot be used is recorded by name and the rest go on; only a failure to look
// for plugins at all is returned.
func (h *Host) Load(ctx context.Context, registry *Registry) error {
	found, err := h.Discover()
	if err != nil {
		return err
	}
	for _, candidate := range found {
		process, err := h.Start(ctx, candidate)
		if err != nil {
			registry.Skip(candidate.Name, err.Error())
			continue
		}
		registry.addProcess(process)
	}
	return nil
}
