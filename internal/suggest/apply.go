package suggest

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ApprovalScope is the name approvals of recipes are stored under, beside the
// projects' hook approvals in the same file.
const ApprovalScope = "herdsman-suggest-catalog"

// The reasons a recipe does not run.
var (
	ErrNoTerminal  = errors.New("suggest apply --run needs a terminal: a person must approve what a recipe runs")
	ErrNoRecipe    = errors.New("this suggestion has no recipe")
	ErrNotRunnable = errors.New("this recipe is a configuration snippet: add it to the listed files by hand")
	ErrDeclined    = errors.New("the recipe was not approved, so it did not run")
)

// RecipeKind says which form of recipe an entry holds.
type RecipeKind string

// The forms of recipe.
const (
	RecipeCommand   RecipeKind = "command"
	RecipeMCP       RecipeKind = "mcp"
	RecipeSkillRepo RecipeKind = "skill_repo"
)

// Recipe is what applying an entry would do, with the checksum an approval
// is recorded against.
type Recipe struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Source   SourceRef  `json:"source"`
	Kind     RecipeKind `json:"kind"`
	Text     string     `json:"text"`
	Touches  []string   `json:"touches"`
	Checksum string     `json:"checksum"`
}

// RecipeOf returns an entry's recipe; false means the entry has none.
func RecipeOf(e Entry) (Recipe, bool) {
	if e.Install == nil {
		return Recipe{}, false
	}
	r := Recipe{ID: e.ID, Title: e.Title, Source: e.Source, Touches: e.Install.Touches}
	switch {
	case e.Install.Command != "":
		r.Kind, r.Text = RecipeCommand, e.Install.Command
	case e.Install.MCP != "":
		r.Kind, r.Text = RecipeMCP, e.Install.MCP
	default:
		r.Kind, r.Text = RecipeSkillRepo, e.Install.SkillRepo
	}
	r.Checksum = checksum(e.ID, *e.Install)
	return r, true
}

// checksum covers what the recipe does and nothing that only describes it, so
// rewording an entry keeps its approval and changing its effect loses it. Every
// field carries its length, so that no two different recipes can read the same.
func checksum(id string, in Install) string {
	h := sha256.New()
	for _, field := range append([]string{id, in.Command, in.MCP, in.SkillRepo}, in.Touches...) {
		fmt.Fprintf(h, "%d:%s,", len(field), field)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Terminal is where a run talks to a person. IsTerminal is the caller's check
// of In; a run with it false stops before anything else happens.
type Terminal struct {
	In         io.Reader
	Out, Err   io.Writer
	IsTerminal bool
}

// ApprovalStore remembers accepted checksums. It is the shape of the store
// project hooks use, so that one file holds both.
type ApprovalStore interface {
	Approved(scope, checksum string) (bool, error)
	Approve(scope string, checksums []string) error
}

// Applier runs recipes once a person has approved their checksums.
type Applier struct {
	Store ApprovalStore
	Exec  func(ctx context.Context, r Recipe, dir string, t Terminal) error
}

// Run executes an entry's recipe in dir. The terminal is required on every run,
// approved or not, so that a process with no person behind it cannot install
// even what a person accepted earlier.
func (a *Applier) Run(ctx context.Context, e Entry, dir string, t Terminal) error {
	if !t.IsTerminal {
		return ErrNoTerminal
	}
	r, ok := RecipeOf(e)
	if !ok {
		return ErrNoRecipe
	}
	if r.Kind == RecipeMCP {
		return ErrNotRunnable
	}
	approved, err := a.Store.Approved(ApprovalScope, r.Checksum)
	if err != nil {
		return err
	}
	if !approved {
		if !confirm(r, t) {
			return ErrDeclined
		}
		if err := a.Store.Approve(ApprovalScope, []string{r.Checksum}); err != nil {
			return err
		}
	}
	return a.Exec(ctx, r, dir, t)
}

func confirm(r Recipe, t Terminal) bool {
	fmt.Fprintf(t.Out, "%s (from %s %s)\n%s: %s\ntouches: %s\nchecksum: %s\nRun it? Type yes: ",
		r.ID, r.Source.Kind, r.Source.Name, r.Kind, r.Text, strings.Join(r.Touches, ", "), r.Checksum)
	line, err := bufio.NewReader(t.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	return strings.TrimSpace(line) == "yes"
}

// ExecRecipe carries out an approved recipe with the terminal attached.
func ExecRecipe(ctx context.Context, r Recipe, dir string, t Terminal) error {
	var cmd *exec.Cmd
	switch r.Kind {
	case RecipeCommand:
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", r.Text)
	case RecipeSkillRepo:
		if len(r.Touches) == 0 {
			return errors.New("this skill names no folder to clone into")
		}
		target, err := expandHome(r.Touches[0])
		if err != nil {
			return err
		}
		cmd = exec.CommandContext(ctx, "git", "clone", "--", r.Text, target)
	default:
		return ErrNotRunnable
	}
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, t.In, t.Out, t.Err
	return cmd.Run()
}

func expandHome(path string) (string, error) {
	rest, found := strings.CutPrefix(path, "~/")
	if !found {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}
