package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// ConfigFile is the per-folder configuration file that records a binding.
const ConfigFile = "herdsman.config.toml"

var (
	projectHeader = regexp.MustCompile(`^\[\s*(?:"project"|'project'|project)\s*\]\s*(?:#.*)?$`)
	rootProject   = regexp.MustCompile(`^(?:"project"|'project'|project)\s*[.=]`)
	idKey         = regexp.MustCompile(`^(?:id|"id"|'id')\s*=\s*(.*)$`)
	nameKey       = regexp.MustCompile(`^(?:name|"name"|'name')\s*=\s*(.*)$`)
	escapedKey    = regexp.MustCompile(`^"[^"]*\\`)
	stringValue   = regexp.MustCompile(`^("(?:[^"\\]|\\.)*"|'[^']*')\s*(?:#.*)?$`)
)

// fileEdit is one planned change to a config file, with what undoes it.
type fileEdit struct {
	path    string
	before  string
	existed bool
	after   string
	remove  bool
	mode    os.FileMode
	applied bool
}

// fileEdits collects a transaction's file changes. They are planned while the
// transaction reads, so a config that cannot be edited stops it before any file
// has changed, and applied last.
type fileEdits struct {
	list []*fileEdit
}

// add plans a change of path from before to after; an empty after removes the
// file, unless it is a link, whose target is emptied so the link survives.
func (e *fileEdits) add(path, before string, existed bool, after string) error {
	if existed && before == after {
		return nil
	}
	edit := &fileEdit{path: path, before: before, existed: existed, after: after, mode: 0o644}
	info, err := os.Lstat(path)
	linked := err == nil && info.Mode()&os.ModeSymlink != 0
	edit.remove = after == "" && !linked
	if target, err := filepath.EvalSymlinks(path); err == nil {
		edit.path = target
	}
	if info, err := os.Stat(edit.path); err == nil {
		edit.mode = info.Mode().Perm()
	}
	e.list = append(e.list, edit)
	return nil
}

func (e *fileEdits) apply() error {
	for _, edit := range e.list {
		var err error
		if edit.remove {
			err = os.Remove(edit.path)
		} else {
			err = replaceFile(edit.path, edit.after, edit.mode)
		}
		if err != nil {
			return err
		}
		edit.applied = true
	}
	return nil
}

// undo reverses the applied edits, newest first.
func (e *fileEdits) undo() error {
	var errs []error
	for _, edit := range slices.Backward(e.list) {
		if !edit.applied {
			continue
		}
		if edit.existed {
			errs = append(errs, replaceFile(edit.path, edit.before, edit.mode))
		} else {
			errs = append(errs, os.Remove(edit.path))
		}
	}
	return errors.Join(errs...)
}

// txFiles runs fn in a transaction, applies the file edits it planned as the
// transaction's last work, and undoes them if an edit or the commit fails. A
// crash between the edits and the commit is not covered here.
func (s *Store) txFiles(fn func(store.Conn, *fileEdits) error) error {
	var files fileEdits
	err := s.db.Tx(func(c store.Conn) error {
		if err := fn(c, &files); err != nil {
			return err
		}
		return files.apply()
	})
	if err != nil {
		return errors.Join(err, files.undo())
	}
	return nil
}

// Bind records folder under the project and writes the project's identity to
// the folder's config, both or neither.
func (s *Store) Bind(ref, folder string) (*Project, error) {
	canon, err := existingFolder(folder)
	if err != nil {
		return nil, err
	}
	var out *Project
	err = s.txFiles(func(c store.Conn, files *fileEdits) error {
		p, err := activeProject(c, ref)
		if err != nil {
			return err
		}
		holders, err := column(c, "SELECT p.name FROM project_folders f JOIN projects p ON p.id = f.project_id WHERE f.path = ? AND p.id <> ?", canon, p.ID)
		if err != nil {
			return err
		}
		if len(holders) > 0 {
			return fmt.Errorf("%w: %s is bound to project %s", ErrConflict, canon, holders[0])
		}
		if _, err := c.Exec("INSERT OR IGNORE INTO project_folders (path, project_id) VALUES (?, ?)", canon, p.ID); err != nil {
			return err
		}
		if err := writeBinding(files, canon, p.ID, p.Name); err != nil {
			return err
		}
		out, err = loadProject(c, p.ID)
		return err
	})
	return out, err
}

// Unbind reverses Bind. The folder itself may be gone already.
func (s *Store) Unbind(folder string) (*Project, error) {
	if !filepath.IsAbs(folder) {
		return nil, fmt.Errorf("%w: folder %q is not absolute", ErrInvalid, folder)
	}
	canon := filepath.Clean(folder)
	if resolved, err := filepath.EvalSymlinks(canon); err == nil {
		canon = resolved
	}
	var out *Project
	err := s.txFiles(func(c store.Conn, files *fileEdits) error {
		ids, err := column(c, "SELECT project_id FROM project_folders WHERE path = ?", canon)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return fmt.Errorf("%w: %s is not bound", ErrNotFound, canon)
		}
		if _, err := c.Exec("DELETE FROM project_folders WHERE path = ?", canon); err != nil {
			return err
		}
		if err := clearBinding(files, canon, ids[0]); err != nil {
			return err
		}
		out, err = loadProject(c, ids[0])
		return err
	})
	return out, err
}

func existingFolder(folder string) (string, error) {
	if !filepath.IsAbs(folder) {
		return "", fmt.Errorf("%w: folder %q is not absolute", ErrInvalid, folder)
	}
	canon, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", fmt.Errorf("%w: folder: %v", ErrInvalid, err)
	}
	info, err := os.Stat(canon)
	if err != nil {
		return "", fmt.Errorf("%w: folder: %v", ErrInvalid, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a folder", ErrInvalid, canon)
	}
	return canon, nil
}

// config is a parsed configuration file. Only the [project] section is
// understood; every other line is carried through untouched.
type config struct {
	lines    []string
	start    int // the header line of [project], or -1
	end      int // one past the section's last key
	idLine   int
	nameLine int
	id       string
	name     string
}

// lineScanner tracks whether a line begins at the top level of the file, which
// is what separates a real header from text inside a value.
type lineScanner struct {
	multi string // the closing delimiter of an open multi-line string
	depth int    // open array brackets
}

// topLevel reports whether line begins at the top level, then consumes it.
func (s *lineScanner) topLevel(line string) bool {
	top := s.multi == "" && s.depth == 0
	for i := 0; i < len(line); {
		rest := line[i:]
		if s.multi != "" {
			end := strings.Index(rest, s.multi)
			if end < 0 {
				return top
			}
			i += end + len(s.multi)
			s.multi = ""
			continue
		}
		switch {
		case strings.HasPrefix(rest, `"""`), strings.HasPrefix(rest, `'''`):
			s.multi = rest[:3]
			i += 3
		case rest[0] == '#':
			return top
		case rest[0] == '"', rest[0] == '\'':
			i += stringLength(rest)
		case rest[0] == '[':
			s.depth++
			i++
		case rest[0] == ']':
			s.depth--
			i++
		default:
			i++
		}
	}
	return top
}

// stringLength measures the one-line string that opens rest, with its quotes.
func stringLength(rest string) int {
	quote := rest[0]
	for i := 1; i < len(rest); i++ {
		switch {
		case rest[i] == '\\' && quote == '"':
			i++
		case rest[i] == quote:
			return i + 1
		}
	}
	return len(rest)
}

func parseConfig(path, text string) (*config, error) {
	cfg := &config{start: -1, idLine: -1, nameLine: -1}
	if text != "" {
		cfg.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	var scan lineScanner
	inRoot := true
	for i, raw := range cfg.lines {
		top := scan.topLevel(raw)
		line := strings.TrimSpace(raw)
		if !top {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if cfg.start >= 0 && cfg.end == 0 {
				cfg.end = sectionEnd(cfg.lines, cfg.start, i)
			}
			inRoot = false
			if cfg.start < 0 && projectHeader.MatchString(line) {
				cfg.start = i
			}
			continue
		}
		if inRoot && rootProject.MatchString(line) {
			return nil, fmt.Errorf("%w: %s: project is set outside a [project] section", ErrInvalid, path)
		}
		if cfg.start < 0 || cfg.end != 0 {
			continue
		}
		if err := cfg.readKey(path, i, line); err != nil {
			return nil, err
		}
	}
	if cfg.start >= 0 && cfg.end == 0 {
		cfg.end = sectionEnd(cfg.lines, cfg.start, len(cfg.lines))
	}
	return cfg, nil
}

func (cfg *config) readKey(path string, i int, line string) error {
	if escapedKey.MatchString(line) {
		return fmt.Errorf("%w: %s: [project] has a key with escapes, which cannot be edited", ErrInvalid, path)
	}
	if m := idKey.FindStringSubmatch(line); m != nil {
		if cfg.idLine >= 0 {
			return fmt.Errorf("%w: %s: [project] sets id twice", ErrInvalid, path)
		}
		v, err := parseString(path, "id", m[1])
		if err != nil {
			return err
		}
		cfg.id, cfg.idLine = v, i
	}
	if m := nameKey.FindStringSubmatch(line); m != nil {
		if cfg.nameLine >= 0 {
			return fmt.Errorf("%w: %s: [project] sets name twice", ErrInvalid, path)
		}
		v, err := parseString(path, "name", m[1])
		if err != nil {
			return err
		}
		cfg.name, cfg.nameLine = v, i
	}
	return nil
}

// sectionEnd backs up over blank and comment lines, which read as belonging to
// whatever follows the section.
func sectionEnd(lines []string, start, limit int) int {
	end := limit
	for end > start+1 {
		line := strings.TrimSpace(lines[end-1])
		if line != "" && !strings.HasPrefix(line, "#") {
			break
		}
		end--
	}
	return end
}

func parseString(path, key, value string) (string, error) {
	m := stringValue.FindStringSubmatch(value)
	if m == nil {
		return "", fmt.Errorf("%w: %s: [project] %s is not a string", ErrInvalid, path, key)
	}
	if m[1][0] == '\'' {
		return m[1][1 : len(m[1])-1], nil
	}
	v, err := strconv.Unquote(m[1])
	if err != nil {
		return "", fmt.Errorf("%w: %s: [project] %s: %v", ErrInvalid, path, key, err)
	}
	return v, nil
}

// quoteTOML writes s as a TOML basic string. Project names and ids hold no
// control characters, so only the quote and the backslash need escaping.
func quoteTOML(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// setIdentity writes id and name into the section, adding it when absent.
func (cfg *config) setIdentity(id, name string) {
	idText, nameText := "id = "+quoteTOML(id), "name = "+quoteTOML(name)
	if cfg.start < 0 {
		if len(cfg.lines) > 0 {
			cfg.lines = append(cfg.lines, "")
		}
		cfg.lines = append(cfg.lines, "[project]", idText, nameText)
		return
	}
	if cfg.nameLine >= 0 {
		cfg.lines[cfg.nameLine] = nameText
	}
	at := cfg.idLine
	if at >= 0 {
		cfg.lines[at] = idText
	} else {
		at = cfg.start + 1
		cfg.lines = slices.Insert(cfg.lines, at, idText)
	}
	if cfg.nameLine < 0 {
		cfg.lines = slices.Insert(cfg.lines, at+1, nameText)
	}
}

// removeSection drops the section and the blank lines that set it apart.
func (cfg *config) removeSection() {
	start, end := cfg.start, cfg.end
	if end == len(cfg.lines) {
		for start > 0 && strings.TrimSpace(cfg.lines[start-1]) == "" {
			start--
		}
	} else {
		for end < len(cfg.lines) && strings.TrimSpace(cfg.lines[end]) == "" {
			end++
		}
	}
	cfg.lines = slices.Delete(cfg.lines, start, end)
}

func (cfg *config) text() string {
	if len(cfg.lines) == 0 {
		return ""
	}
	return strings.Join(cfg.lines, "\n") + "\n"
}

// readConfig returns the file's text; a missing file or folder reads as empty.
func readConfig(path string) (text string, exists bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

func writeBinding(files *fileEdits, folder, id, name string) error {
	path := filepath.Join(folder, ConfigFile)
	text, exists, err := readConfig(path)
	if err != nil {
		return err
	}
	cfg, err := parseConfig(path, text)
	if err != nil {
		return err
	}
	if cfg.id != "" && cfg.id != id {
		return fmt.Errorf("%w: %s is bound to project %s", ErrConflict, path, cfg.id)
	}
	cfg.setIdentity(id, name)
	return files.add(path, text, exists, cfg.text())
}

// renameBinding updates the name in a folder that still holds this binding.
func renameBinding(files *fileEdits, folder, id, name string) error {
	path := filepath.Join(folder, ConfigFile)
	text, exists, err := readConfig(path)
	if err != nil || !exists {
		return err
	}
	cfg, err := parseConfig(path, text)
	if err != nil {
		return err
	}
	if cfg.id != id {
		return nil
	}
	cfg.setIdentity(id, name)
	return files.add(path, text, true, cfg.text())
}

// clearBinding removes the section when it still names this project, and the
// file when nothing else is left in it.
func clearBinding(files *fileEdits, folder, id string) error {
	path := filepath.Join(folder, ConfigFile)
	text, exists, err := readConfig(path)
	if err != nil || !exists {
		return err
	}
	cfg, err := parseConfig(path, text)
	if err != nil {
		return err
	}
	if cfg.start < 0 || cfg.id != id {
		return nil
	}
	cfg.removeSection()
	updated := cfg.text()
	if strings.TrimSpace(updated) == "" {
		updated = ""
	}
	return files.add(path, text, true, updated)
}

// replaceFile swaps the content in by rename, so a crash leaves the old file
// or the new one and never half of either.
func replaceFile(path, text string, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".herdsman-config-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	defer tmp.Close()
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err = tmp.WriteString(text); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
