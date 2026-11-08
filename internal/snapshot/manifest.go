// Package snapshot captures a worktree so it can be restored on a machine
// that has never seen the repository: staged and unstaged state as two git
// trees in a complete bundle, ignored files in preserved.tar, and a manifest
// that says exactly what is inside and what was left out.
package snapshot

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// SchemaVersion is the SemVer of the manifest format.
	SchemaVersion = "1.0.0"

	ManifestName  = "snapshot.json"
	BundleName    = "bundle.git"
	PreservedName = "preserved.tar"
	// FolderArtifact names the snapshot folder itself in a VerifyError.
	FolderArtifact = "snapshot folder"

	workDirName = ".work"

	// SourceTree marks a file held in the working tree of the bundle.
	SourceTree = "tree"
	// SourcePreserved marks a file held in preserved.tar.
	SourcePreserved = "preserved"

	ReasonRebuildable      = "rebuildable"
	ReasonOverCap          = "over_file_cap"
	ReasonSubmodule        = "submodule"
	ReasonNestedRepository = "nested_repository"
	ReasonSpecial          = "special_file"
)

var (
	// ErrDestExists means the snapshot folder is already there.
	ErrDestExists = errors.New("snapshot: destination already exists")
	// ErrUnmerged means the index has conflicts and has no single staged tree.
	ErrUnmerged = errors.New("snapshot: index has unmerged paths")
	// ErrTargetOccupied means a restore would overwrite something.
	ErrTargetOccupied = errors.New("snapshot: restore target is occupied")
	// ErrUnsafePath means a stored path could leave the restore folder.
	ErrUnsafePath = errors.New("snapshot: unsafe path")
)

// Path is a repository-relative path. Git paths are bytes, and JSON strings
// cannot hold invalid UTF-8, so such a path is written as {"base64": "..."}.
type Path string

// MarshalJSON keeps every path byte.
func (p Path) MarshalJSON() ([]byte, error) {
	if utf8.ValidString(string(p)) {
		return json.Marshal(string(p))
	}
	return json.Marshal(encodedPath{Base64: base64.StdEncoding.EncodeToString([]byte(p))})
}

// UnmarshalJSON reads both forms MarshalJSON writes.
func (p *Path) UnmarshalJSON(raw []byte) error {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		*p = Path(plain)
		return nil
	}
	var enc encodedPath
	if err := json.Unmarshal(raw, &enc); err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.DecodeString(enc.Base64)
	if err != nil {
		return err
	}
	*p = Path(decoded)
	return nil
}

type encodedPath struct {
	Base64 string `json:"base64"`
}

// Artifact is one stored data file and its checksum.
type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// FileRecord is one captured path. Mode uses git's numbering: 0100644,
// 0100755, 0120000 for a symlink, and for preserved entries 0100000 or
// 0040000 plus the exact permission bits. Perm keeps a tree file's permission
// bits, which git reduces to executable or not: a private untracked file must
// not come back readable by others.
type FileRecord struct {
	Path    Path   `json:"path"`
	Mode    uint32 `json:"mode"`
	Perm    uint32 `json:"perm,omitempty"`
	Size    int64  `json:"size"`
	MTimeNS int64  `json:"mtime_ns"`
	Source  string `json:"source"`
}

// Submodule records a gitlink. Head is empty when it is not checked out.
type Submodule struct {
	Path        Path   `json:"path"`
	IndexCommit string `json:"index_commit"`
	Head        string `json:"head"`
}

// Exclusion is a path deliberately left out of the snapshot.
type Exclusion struct {
	Path    Path   `json:"path"`
	Reason  string `json:"reason"`
	Pattern string `json:"pattern,omitempty"`
	Size    int64  `json:"size,omitempty"`
}

// Manifest says exactly what a snapshot holds.
type Manifest struct {
	Schema         string       `json:"schema"`
	Task           string       `json:"task"`
	Op             string       `json:"op"`
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     time.Time    `json:"finished_at"`
	Worktree       string       `json:"worktree"`
	WorktreeMode   uint32       `json:"worktree_mode"`
	ObjectFormat   string       `json:"object_format"`
	Head           string       `json:"head"`
	Branch         string       `json:"branch"`
	SnapshotRef    string       `json:"snapshot_ref"`
	SnapshotCommit string       `json:"snapshot_commit"`
	StagedTree     string       `json:"staged_tree"`
	WorkingTree    string       `json:"working_tree"`
	Bundle         Artifact     `json:"bundle"`
	Preserved      Artifact     `json:"preserved"`
	FileCap        int64        `json:"file_cap"`
	Rebuildable    []string     `json:"rebuildable"`
	Files          []FileRecord `json:"files"`
	Submodules     []Submodule  `json:"submodules"`
	Excluded       []Exclusion  `json:"excluded"`
}

func marshalManifest(m *Manifest) ([]byte, error) {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// writeManifest publishes the manifest by rename, so its presence means the
// data files beside it were complete when it was written. The caller syncs
// those files first; the manifest and the folder entry are synced here.
func writeManifest(dir string, m *Manifest) error {
	raw, err := marshalManifest(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ManifestName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := syncPath(tmp); err != nil {
		return err
	}
	if err := osRename(tmp, filepath.Join(dir, ManifestName)); err != nil {
		return err
	}
	return syncPath(dir)
}

// ReadManifest loads a snapshot's manifest and refuses a format of another
// major version.
func ReadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(m.Schema, "1.") {
		return nil, fmt.Errorf("unsupported manifest schema %q", m.Schema)
	}
	return &m, nil
}
