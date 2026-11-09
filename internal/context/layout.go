// Package context reads and writes a project's context repo: a git repository
// holding the project's knowledge and its chronology. The chronology is a
// graph of immutable event files, so two clones can add events independently
// and git merges them without touching an existing file.
package context

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The layout of a context repo. These names are a compatibility surface.
const (
	KnowledgeDir = "knowledge"
	EventsDir    = "events"
	SuggestDir   = "suggest"
	ReadmeFile   = "README.md"

	// keepFile holds an empty folder in git.
	keepFile = ".gitkeep"
)

// Errors a caller can tell apart with errors.Is.
var (
	ErrNotGitRepo     = errors.New("not a git repository")
	ErrNotContextRepo = errors.New("not a context repo")
	ErrInvalid        = errors.New("invalid event")
	ErrMalformed      = errors.New("malformed event file")
)

const readme = `# Project context

This repository is managed with herdsman.

- ` + "`knowledge/`" + `: notes in Markdown, in any structure you like.
- ` + "`events/`" + `: the project's history. Each file is one event and is never edited.
- ` + "`suggest/`" + `: suggestions specific to this project.
`

// Init lays out a context repo in folder, which must be a git repository. It
// is safe to repeat: whatever is already there is kept.
func Init(folder string) error {
	if _, err := os.Lstat(filepath.Join(folder, ".git")); err != nil {
		return fmt.Errorf("%w: %s", ErrNotGitRepo, folder)
	}
	for _, sub := range []string{KnowledgeDir, EventsDir, SuggestDir} {
		if err := os.MkdirAll(filepath.Join(folder, sub), 0o755); err != nil {
			return err
		}
		if err := writeIfAbsent(filepath.Join(folder, sub, keepFile), ""); err != nil {
			return err
		}
	}
	return writeIfAbsent(filepath.Join(folder, ReadmeFile), readme)
}

// writeIfAbsent creates a file, leaves an existing one alone and refuses
// anything at path that is not a regular file.
func writeIfAbsent(path, text string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		return nil
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
