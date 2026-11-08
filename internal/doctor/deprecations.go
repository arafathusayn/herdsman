package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/shell"
	"github.com/arafathusayn/herdsman/sdk/config"
)

// The kinds of thing that can be deprecated.
const (
	KindKey      = "key"
	KindFlag     = "flag"
	KindVariable = "variable"
	KindWrapper  = "wrapper"
)

// The markers the shell integration writes around its block. A test installs
// the real block, so a change on that side fails here.
const (
	wrapperStart = "# >>> herdsman shell integration >>>"
	wrapperEnd   = "# <<< herdsman shell integration <<<"
)

// Deprecation is one thing a later release replaced, and how to move off it.
// Tools read it from the "deprecation" field of a check in `doctor --json`.
type Deprecation struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Replacement string `json:"replacement"`
	// Since is the release that deprecated it. An installed wrapper has no
	// version of its own, so it leaves this empty.
	Since string `json:"since,omitempty"`
	// Fix is what to run or change.
	Fix string `json:"fix"`
}

// deprecated is every key, flag and variable still accepted under an old
// name. It is empty because no release has retired anything yet. Add a row in
// the change that renames one; the old name keeps working regardless.
var deprecated = []Deprecation{}

// checkDeprecations reports the table's rows in table order, then each
// installed wrapper that differs from this binary's. It reports nothing when
// nothing applies, so a clean machine's output does not change.
func checkDeprecations(p Probes, table []Deprecation) []Check {
	var cs []Check
	found := func(level string, d Deprecation) {
		cs = append(cs, Check{Level: level, Name: "deprecated", Detail: describe(d, ""), Deprecation: &d})
	}
	keys, keysErr := readKeys(p, table)
	if keysErr != nil {
		cs = append(cs, Check{Level: Warn, Name: "deprecated", Detail: "cannot read the configuration: " + keysErr.Error()})
	}
	for _, d := range table {
		switch d.Kind {
		case KindKey:
			if slices.Contains(keys, d.Name) {
				found(Warn, d)
			}
		case KindVariable:
			if p.Getenv(d.Name) != "" {
				found(Warn, d)
			}
		case KindFlag:
			// A flag is used on some other command's run, which doctor
			// cannot see, so it is listed without claiming it is in use.
			found(OK, d)
		}
	}
	if p.ShellRC != nil {
		for _, name := range shell.Shells {
			cs = append(cs, checkWrapper(p, name)...)
		}
	}
	return cs
}

// readKeys loads the configuration only when a row needs it, so a broken file
// is not reported here while no key is deprecated.
func readKeys(p Probes, table []Deprecation) ([]string, error) {
	needed := slices.ContainsFunc(table, func(d Deprecation) bool { return d.Kind == KindKey })
	if !needed || p.ConfigKeys == nil {
		return nil, nil
	}
	return p.ConfigKeys()
}

func describe(d Deprecation, where string) string {
	version := ""
	if d.Since != "" {
		version = "deprecated since " + d.Since + "; "
	}
	return fmt.Sprintf("%s %s%s: use %s (%sfix: %s)", d.Kind, d.Name, where, d.Replacement, version, d.Fix)
}

func checkWrapper(p Probes, name string) []Check {
	path, text, err := p.ShellRC(name)
	if err != nil {
		return []Check{{Level: Warn, Name: "deprecated", Detail: fmt.Sprintf("cannot read the %s startup file %s: %v", name, path, err)}}
	}
	blocks, terminated := wrapperBlocks(text)
	if !terminated {
		return []Check{{Level: Warn, Name: "deprecated", Detail: fmt.Sprintf("%s: the shell integration has no end marker; fix it by hand", path)}}
	}
	install := "herdsman config shell install " + name
	d := Deprecation{Kind: KindWrapper, Name: name, Replacement: "the wrapper of this binary", Fix: install}
	where := " in " + path
	switch {
	case len(blocks) == 0 || len(blocks) == 1 && blocks[0] == wrapperBlock(name):
		return nil
	case len(blocks) > 1:
		// The installer replaces only the first block, so it cannot repair this.
		d.Fix = "remove every block but one by hand, then run " + install
		where = fmt.Sprintf(" in %s (%d blocks)", path, len(blocks))
	}
	return []Check{{Level: Warn, Name: "deprecated", Detail: describe(d, where), Deprecation: &d}}
}

// wrapperBlocks returns each marked block in text, markers included. It
// reports false when the last start marker has no end marker after it.
func wrapperBlocks(text string) (blocks []string, terminated bool) {
	for {
		start := strings.Index(text, wrapperStart)
		if start < 0 {
			return blocks, true
		}
		length := strings.Index(text[start:], wrapperEnd)
		if length < 0 {
			return blocks, false
		}
		end := start + length + len(wrapperEnd)
		blocks = append(blocks, text[start:end])
		text = text[end:]
	}
}

// wrapperBlock is the block this binary installs for a shell in shell.Shells,
// for which neither call can fail.
func wrapperBlock(name string) string {
	wrapper, _ := shell.Wrapper(name)
	completions, _ := shell.Completions(name)
	return wrapperStart + "\n" + wrapper + "\n" + completions + "\n" + wrapperEnd
}

// configKeysInUse lists the keys a file, the environment or a flag set,
// including keys the schema no longer defines.
func configKeysInUse() ([]string, error) {
	dir, _ := os.Getwd()
	result, err := config.Load(config.Options{RepoRoot: nearestRepository(dir)})
	if err != nil {
		return nil, err
	}
	var keys []string
	for key, source := range result.Sources {
		if source != config.SourceDefault {
			keys = append(keys, key)
		}
	}
	for _, warning := range result.Warnings {
		keys = append(keys, warning.Key)
	}
	return keys, nil
}

// nearestRepository walks up from dir to the closest folder holding .git.
func nearestRepository(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// readShellRC returns a shell's startup file and its text; a file that does
// not exist reads as empty.
func readShellRC(name string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	path, err := shell.RCFile(name, shell.Env{Home: home, ZDotDir: os.Getenv("ZDOTDIR")})
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, "", nil
	}
	return path, string(data), err
}
