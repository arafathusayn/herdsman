package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/snapshot"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

// rebuildableShownKey marks a repository whose first clean has happened.
const rebuildableShownKey = "snapshot-rebuildable-shown"

// snapshotSettings are what a repository's configuration says a snapshot does.
type snapshotSettings struct {
	Rebuildable []string
	FileCap     int64
}

// loadSnapshotSettings reads the snapshot section of the configuration file
// beside the repository. An empty rebuildable list is kept as empty, because
// it says nothing may be left out; an absent one takes the defaults.
func loadSnapshotSettings(folder string) (snapshotSettings, error) {
	settings := snapshotSettings{Rebuildable: snapshot.DefaultRebuildable()}
	path := filepath.Join(folder, "herdsman.config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	var file struct {
		Snapshot struct {
			Rebuildable *[]string `json:"rebuildable"`
			FileCap     int64     `json:"file_cap"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return settings, fmt.Errorf("%s: %w", path, err)
	}
	if file.Snapshot.FileCap < 0 {
		return settings, fmt.Errorf("%s: snapshot file_cap %d is negative", path, file.Snapshot.FileCap)
	}
	settings.FileCap = file.Snapshot.FileCap
	if file.Snapshot.Rebuildable != nil {
		settings.Rebuildable = append([]string{}, *file.Snapshot.Rebuildable...)
	}
	return settings, nil
}

// RecordRestoredTree writes a restored tree back into its repository's task
// records, which a clean removed. A snapshot with no repository has none.
func RecordRestoredTree(ctx context.Context, recovery *ops.Recovery, id, target string) error {
	records, err := recovery.Snapshots()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.ID != id {
			continue
		}
		if record.Repo == "" {
			return nil
		}
		manifest, err := snapshot.ReadManifest(record.Dir)
		if err != nil {
			return err
		}
		state, err := wtree.OpenState(record.Repo)
		if err != nil {
			return err
		}
		defer state.Close()
		return state.PutWorktree(ctx, wtree.Worktree{Task: record.Task, Branch: strings.TrimPrefix(manifest.Branch, "refs/heads/"), Path: target})
	}
	return fmt.Errorf("snapshot %s has no record", id)
}
