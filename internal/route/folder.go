package route

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
)

func applyFolder(ctx context.Context, _ Deps, _ *herdr.Client, d stepData) error {
	for _, dir := range []string{"ref", "probes", ".steps"} {
		if err := os.MkdirAll(filepath.Join(d.Folder, dir), 0o755); err != nil {
			return err
		}
	}
	if err := writeOnce(filepath.Join(d.Folder, "00-route.md"), []byte(routeFile(d)), 0o644); err != nil {
		return err
	}
	script := "#!/bin/bash\nexec " + shellQuote(d.Executable) + " run-check \"$@\"\n"
	if err := writeOnce(filepath.Join(d.Folder, "run-check.sh"), []byte(script), 0o755); err != nil {
		return err
	}
	return excludeFolder(ctx, d.Repo)
}

func routeFile(d stepData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Route: %s\n\nRequest: %s\n\n", d.Route, d.Prompt)
	if d.Config != "" {
		fmt.Fprintf(&b, "Config: %s\n", d.Config)
	}
	fmt.Fprintf(&b, "Orchestrator: %s\n", d.Orchestrator)
	if d.Adjutant != "" {
		fmt.Fprintf(&b, "Adjutant: %s\nInbox: %s (per route)\nQuestions: through the adjutant\n", d.Adjutant, d.Inbox)
	}
	b.WriteString("\n| Task | Pane | State |\n| --- | --- | --- |\n")
	return b.String()
}

// writeOnce keeps an existing file: the route's own edits win over a resume.
func writeOnce(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// excludeFolder hides .herdsman from git through the local exclude file, so
// the route folder can never be committed by accident.
func excludeFolder(ctx context.Context, repo string) error {
	path, err := excludePath(ctx, repo)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for line := range strings.SplitSeq(string(current), "\n") {
		if strings.TrimSpace(line) == ".herdsman" {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(current) > 0 && !strings.HasSuffix(string(current), "\n") {
		current = append(current, '\n')
	}
	return os.WriteFile(path, append(current, ".herdsman\n"...), 0o644)
}

// excludePath finds the exclude file, which a linked worktree keeps in the
// common git folder.
func excludePath(ctx context.Context, repo string) (string, error) {
	gitPath := filepath.Join(repo, ".git")
	if st, err := os.Stat(gitPath); err == nil && st.IsDir() {
		return filepath.Join(gitPath, "info", "exclude"), nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude").Output()
	if err != nil {
		return "", fmt.Errorf("find the git exclude file: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func probeFolder(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(filepath.Join(d.Folder, "run-check.sh")); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}
