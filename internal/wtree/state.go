package wtree

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	// Registers the SQLite driver; only that package names it.
	_ "github.com/arafathusayn/herdsman/internal/daemon/store"
)

//go:embed schema.sql
var schemaOne string

//go:embed schema_two.sql
var schemaTwo string

// migrations holds one script per schema version, in order. Versions only go
// forward: a script is added here and never edited or removed.
var migrations = []string{schemaOne, schemaTwo}

// StateFolder is the state's folder inside a repository's common git directory.
const StateFolder = "herdsman"

const previousKey = "previous"

// ErrSchemaNewer reports a state database written by a newer herdsman.
var ErrSchemaNewer = errors.New("wtree: state schema is newer than this herdsman")

// State is one repository's worktree database.
type State struct {
	db *sql.DB
}

// Worktree is one recorded worktree.
type Worktree struct {
	Task      string
	Branch    string
	Path      string
	CreatedAt time.Time
}

// OpenState opens the state under the repository's common git directory,
// creating it on first use and migrating it forward.
//
// The daemon's store replaces SQLite's file open for the whole process with
// one that accepts only a private folder and private regular files, so the
// same requirements are checked here and hold with or without the store.
func OpenState(commonDir string) (*State, error) {
	path, err := prepareStateFiles(commonDir)
	if err != nil {
		return nil, err
	}
	query := url.Values{
		"mode": {"rw"}, "_txlock": {"immediate"}, "_busy_timeout": {"5000"},
		"_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_foreign_keys": {"on"},
	}
	location := url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, fmt.Errorf("wtree: open state: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db, migrations); err != nil {
		db.Close()
		return nil, err
	}
	return &State{db: db}, nil
}

func prepareStateFiles(commonDir string) (string, error) {
	if info, err := os.Stat(commonDir); err != nil {
		return "", fmt.Errorf("wtree: common git directory: %w", err)
	} else if !info.IsDir() {
		return "", fmt.Errorf("wtree: %s is not a directory", commonDir)
	}
	folder := filepath.Join(commonDir, StateFolder)
	if err := os.Mkdir(folder, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("wtree: state folder: %w", err)
	}
	if err := checkPrivate(folder, true); err != nil {
		return "", err
	}
	path := filepath.Join(folder, "state.db")
	if err := createPrivate(path); err != nil {
		return "", err
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if err := checkPrivate(name, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return path, nil
}

// createPrivate makes a missing file with mode 600, because SQLite would use
// the umask. An existing file is never opened here: closing any descriptor of
// a file drops the locks a live connection in this process holds on it.
func createPrivate(path string) error {
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("wtree: create state: %w", err)
	}
	return file.Close()
}

// checkPrivate requires a real folder of mode 700 or a regular file of mode
// 600, owned by this user and never a link.
func checkPrivate(path string, folder bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("wtree: %w", err)
	}
	want, kind := fs.FileMode(0o600), info.Mode().IsRegular()
	if folder {
		want, kind = 0o700, info.IsDir()
	}
	if !kind {
		return fmt.Errorf("wtree: %s is not a %s", path, map[bool]string{true: "folder", false: "regular file"}[folder])
	}
	if info.Mode().Perm() != want {
		return fmt.Errorf("wtree: %s has mode %o, want %o", path, info.Mode().Perm(), want)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("wtree: %s is not owned by this user", path)
	}
	return nil
}

// migrate applies every script past the stored version in one transaction, so
// a failure leaves the version and the tables as they were.
func migrate(ctx context.Context, db *sql.DB, scripts []string) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("wtree: migrate: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("wtree: read schema version: %w", err)
	}
	if version > len(scripts) {
		return fmt.Errorf("%w: version %d, this herdsman knows %d", ErrSchemaNewer, version, len(scripts))
	}
	for i := version; i < len(scripts); i++ {
		if _, err = tx.ExecContext(ctx, scripts[i]); err != nil {
			return fmt.Errorf("wtree: migrate to version %d: %w", i+1, err)
		}
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(scripts))); err != nil {
		return fmt.Errorf("wtree: write schema version: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("wtree: migrate: %w", err)
	}
	return nil
}

// Close releases the database.
func (s *State) Close() error {
	return s.db.Close()
}

// SchemaVersion reports the stored schema version.
func (s *State) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}

// PutWorktree records a worktree, or updates the row of its task.
func (s *State) PutWorktree(ctx context.Context, w Worktree) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO worktrees (task, branch, path, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(task) DO UPDATE SET branch = excluded.branch, path = excluded.path`,
		w.Task, w.Branch, w.Path, time.Now().Unix())
	return err
}

// Worktrees lists the recorded worktrees by task.
func (s *State) Worktrees(ctx context.Context) ([]Worktree, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task, branch, path, created_at FROM worktrees ORDER BY task`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []Worktree
	for rows.Next() {
		var w Worktree
		var created int64
		if err := rows.Scan(&w.Task, &w.Branch, &w.Path, &created); err != nil {
			return nil, err
		}
		w.CreatedAt = time.Unix(created, 0)
		found = append(found, w)
	}
	return found, rows.Err()
}

// DeleteWorktree removes a task's row.
func (s *State) DeleteWorktree(ctx context.Context, task string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM worktrees WHERE task = ?`, task)
	return err
}

// SetPrevious remembers the worktree the "-" selector returns to.
func (s *State) SetPrevious(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, previousKey, name)
	return err
}

// Mark remembers that something happened once in this repository.
func (s *State) Mark(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, '1')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key)
	return err
}

// Marked reports whether Mark was called for key.
func (s *State) Marked(ctx context.Context, key string) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Previous returns the remembered worktree, or "" when there is none.
func (s *State) Previous(ctx context.Context) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, previousKey).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}
