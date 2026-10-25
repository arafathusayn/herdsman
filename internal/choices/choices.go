// Package choices keeps the user's last pick of harness, model and effort
// per role (A30), so a later route can offer "the previous choice" first.
// The file holds only what the user picked; herdsman names no model (A28).
package choices

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/catalog"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Runners are where a role's agents run (A31).
var Runners = []string{"herdr", "subagent"}

// Roles are the roles a pick is saved for.
var Roles = []string{"implementer", "integrator", "reviewer", "adjutant", "orchestrator"}

// maxSize bounds the file; a few picks take well under a kilobyte.
const maxSize = 64 << 10

var (
	kindWord   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	effortWord = regexp.MustCompile(`^[a-z]{1,16}$`)
	routeWord  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// Pick is one role's choice.
//
// JSON fields: role, runner ("herdr": the agent runs in a Herdr pane;
// "subagent": a background subagent of the orchestrator's own harness),
// harness (an agent kind), model (as the user picked it),
// effort ("" when the harness takes none), route ("" when none was named)
// and at (RFC 3339 time of the pick).
type Pick struct {
	Role    string    `json:"role"`
	Runner  string    `json:"runner"`
	Harness string    `json:"harness"`
	Model   string    `json:"model"`
	Effort  string    `json:"effort"`
	Route   string    `json:"route"`
	At      time.Time `json:"at"`
}

// File is the store's content and `herdsman models last --json`.
//
// JSON fields: picks (the last pick of each role, in the order of Roles).
type File struct {
	Picks []Pick `json:"picks"`
}

// Check validates a pick.
func (p Pick) Check() error {
	switch {
	case !slices.Contains(Roles, p.Role):
		return fmt.Errorf("role %q is not one of %v", p.Role, Roles)
	case !slices.Contains(Runners, p.Runner):
		return fmt.Errorf("runner %q is not one of %v", p.Runner, Runners)
	case !kindWord.MatchString(p.Harness):
		return fmt.Errorf("harness %q is not an agent kind", p.Harness)
	case p.Model != "" && !catalog.ModelID.MatchString(p.Model):
		return fmt.Errorf("model %q must match %s", p.Model, catalog.ModelID)
	case p.Effort != "" && !effortWord.MatchString(p.Effort):
		return fmt.Errorf("effort %q must be lower-case letters", p.Effort)
	case p.Route != "" && !routeWord.MatchString(p.Route):
		return fmt.Errorf("route %q must be letters, digits, . _ or -", p.Route)
	}
	return nil
}

// renameFile replaces the file; tests make it fail.
var renameFile = os.Rename

// Store is the choices file.
type Store struct{ Path string }

// Default is the store in the user's config folder.
func Default() (*Store, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(d, "herdsman", "choices.json")}, nil
}

// Load reads the picks; a missing file has none. The file is strict JSON:
// duplicate keys, unknown fields, wrong types and invalid picks fail.
func (s *Store) Load() (File, error) {
	b, err := inbox.ReadBounded(s.Path, maxSize)
	if errors.Is(err, os.ErrNotExist) {
		return File{Picks: []Pick{}}, nil
	}
	if err != nil {
		return File{}, err
	}
	if err := inbox.StrictJSON(b); err != nil {
		return File{}, fmt.Errorf("%s: %w", s.Path, err)
	}
	var f File
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("%s: %w", s.Path, err)
	}
	seen := map[string]bool{}
	for _, p := range f.Picks {
		if err := p.Check(); err != nil {
			return File{}, fmt.Errorf("%s: %w", s.Path, err)
		}
		if seen[p.Role] {
			return File{}, fmt.Errorf("%s: role %s listed twice", s.Path, p.Role)
		}
		seen[p.Role] = true
	}
	if f.Picks == nil {
		f.Picks = []Pick{}
	}
	return f, nil
}

// Save records picks, each replacing its role's last pick, under a lock and
// with an atomic rename, so a reader never sees half a file.
func (s *Store) Save(picks ...Pick) error {
	for _, p := range picks {
		if err := p.Check(); err != nil {
			return err
		}
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := private(dir); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".choices.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	f, err := s.Load()
	if err != nil {
		return err
	}
	byRole := map[string]Pick{}
	for _, p := range append(f.Picks, picks...) {
		byRole[p.Role] = p
	}
	out := File{Picks: []Pick{}}
	for _, r := range Roles {
		if p, ok := byRole[r]; ok {
			out.Picks = append(out.Picks, p)
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".choices-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(b, '\n'))
	err = errors.Join(werr, tmp.Chmod(0o600), tmp.Close())
	if err == nil {
		err = renameFile(tmp.Name(), s.Path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// private makes an existing store folder private: a real folder owned by
// this user, mode 700.
func private(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || !ok || int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s must be a folder owned by this user", dir)
	}
	if st.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}
