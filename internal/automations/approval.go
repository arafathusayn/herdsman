package automations

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/approvals"
	"github.com/arafathusayn/herdsman/sdk/config"
)

// ApprovalsFile is the store's name inside the user configuration folder. It
// is apart from the hook approvals, so that approving a hook whose text happens
// to look like a checksum can never approve an automation.
const ApprovalsFile = "automation-approvals.toml"

// Checksum identifies what a rule does: its event, kind and body. Each field
// is length-prefixed, so no two rules can share an encoding. The timeout is
// left out because it does not change what runs.
func Checksum(rule Rule) string {
	hash := sha256.New()
	for _, field := range []string{"herdsman-automation-v1", rule.Event, string(rule.Kind), rule.Body} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		hash.Write(size[:])
		hash.Write([]byte(field))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// Approvals records the rule checksums a person accepted, by project. It lives
// in the user's own folder, where a repository's configuration cannot write.
type Approvals struct {
	store *approvals.Store
}

// OpenApprovals returns the store at path. The file need not exist yet.
func OpenApprovals(path string) *Approvals {
	return &Approvals{store: approvals.Open(path)}
}

// DefaultApprovals returns the store in the user configuration folder.
func DefaultApprovals(environ []string) (*Approvals, error) {
	dir := config.UserDir(environ)
	if dir == "" {
		return nil, errors.New("automations: no user configuration folder: HOME is unset")
	}
	return OpenApprovals(filepath.Join(dir, ApprovalsFile)), nil
}

// Approve accepts the rule's current checksum for the project.
func (a *Approvals) Approve(project string, rule Rule) error {
	if project == "" {
		return errors.New("automations: an approval needs a project")
	}
	return a.store.Approve(project, Checksum(rule))
}

// approved answers from the file on every call, so a revoked approval stops
// the next event. No project is never approved.
func (a *Approvals) approved(project, checksum string) (bool, error) {
	if a == nil {
		return false, errors.New("automations: no approvals store")
	}
	if project == "" {
		return false, nil
	}
	return a.store.Approved(project, checksum)
}
