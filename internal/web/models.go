package web

import (
	"context"
	"embed"
	"fmt"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/catalog"
)

// The leak gate's last-known lists: the checked-in real output of each
// harness's catalog, read when the harness cannot be asked, so its model
// names stay blocked on a machine without it.
//
//go:embed catalogdata/claude-help.txt catalogdata/codex-debug-models.json
var lastKnownModels embed.FS

// modelsTimeout bounds each catalog command HarnessModels runs.
const modelsTimeout = 5 * time.Second

// HarnessModels returns the model ids the installed harnesses list in their
// own catalogs (claude --help, codex debug models): the checked-in
// last-known output stands in for a harness that cannot be asked. No model
// name is written in herdsman itself.
func HarnessModels(ctx context.Context) []string {
	return harnessModels(ctx, runner(catalog.ExecRunner))
}

func harnessModels(ctx context.Context, run runner) []string {
	ask := func(name string, args ...string) func() ([]byte, error) {
		return func() ([]byte, error) {
			c, cancel := context.WithTimeout(ctx, modelsTimeout)
			defer cancel()
			return run(c, name, args...)
		}
	}
	var (
		wg            sync.WaitGroup
		claude, codex CatalogKind
	)
	wg.Go(func() { claude = fromOutput(parseClaudeHelp, "claude-help.txt", ask("claude", "--help")) })
	wg.Go(func() {
		codex = fromOutput(parseCodexModels, "codex-debug-models.json", ask("codex", "debug", "models"))
	})
	wg.Wait()
	var ids []string
	for _, k := range []CatalogKind{claude, codex} {
		for _, m := range k.Models {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// fromOutput parses a command's output, or the checked-in output, offline,
// when the command fails or its output does not parse.
func fromOutput(parse func([]byte) (CatalogKind, error), file string, run func() ([]byte, error)) CatalogKind {
	if out, err := run(); err == nil {
		if k, err := parse(out); err == nil {
			return k
		}
	}
	b, err := lastKnownModels.ReadFile("catalogdata/" + file)
	if err != nil {
		panic(err) // embedded at build time
	}
	k, err := parse(b)
	if err != nil {
		panic(fmt.Sprintf("catalogdata/%s does not parse: %v", file, err))
	}
	k.Offline = true
	return k
}
