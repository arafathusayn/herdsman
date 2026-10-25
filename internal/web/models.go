package web

import (
	"context"
	"sync"
)

// HarnessModels returns the model ids the installed harnesses list in their
// own catalogs (claude --help, codex debug models), each read the way the
// form reads it: the checked-in last-known output stands in for a harness
// that cannot be asked. No model name is written in herdsman itself.
func HarnessModels(ctx context.Context) []string {
	return harnessModels(ctx, execRunner)
}

func harnessModels(ctx context.Context, run runner) []string {
	ask := func(name string, args ...string) func() ([]byte, error) {
		return func() ([]byte, error) {
			c, cancel := context.WithTimeout(ctx, catalogTimeout)
			defer cancel()
			return run(c, name, args...)
		}
	}
	var (
		wg            sync.WaitGroup
		claude, codex CatalogKind
	)
	wg.Go(func() { claude = fromOutput(parseClaudeHelp, "claude-help.txt", ask("claude", "--help")) })
	wg.Go(func() { codex = fromOutput(parseCodexModels, "codex-debug-models.json", ask("codex", "debug", "models")) })
	wg.Wait()
	var ids []string
	for _, k := range []CatalogKind{claude, codex} {
		for _, m := range k.Models {
			ids = append(ids, m.ID)
		}
	}
	return ids
}
