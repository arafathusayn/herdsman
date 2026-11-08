package gitrefs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Divergence counts the commits a branch has that the base lacks (Ahead) and
// the commits the base has that the branch lacks (Behind).
type Divergence struct {
	Ahead  int
	Behind int
}

// AheadBehind compares every branch with base in one git process, keyed by
// full branch name. It needs git 2.41 or newer, with any ref storage.
func (r *Reader) AheadBehind(ctx context.Context, base string) (map[string]Divergence, error) {
	if base == "" || strings.ContainsAny(base, ")%\n\x00") {
		return nil, fmt.Errorf("gitrefs: invalid base %q", base)
	}
	out, err := r.runGit(ctx, "for-each-ref", "--format=%(refname)%00%(ahead-behind:"+base+")", "refs/heads")
	if err != nil {
		return nil, err
	}
	return parseAheadBehind(out)
}

func parseAheadBehind(out string) (map[string]Divergence, error) {
	result := map[string]Divergence{}
	for line := range strings.Lines(out) {
		name, counts, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "\x00")
		ahead, behind, spaced := strings.Cut(counts, " ")
		if !ok || !spaced {
			return nil, fmt.Errorf("gitrefs: unexpected ahead-behind line: %q", line)
		}
		aheadCount, aheadErr := strconv.Atoi(ahead)
		behindCount, behindErr := strconv.Atoi(behind)
		if aheadErr != nil || behindErr != nil {
			return nil, fmt.Errorf("gitrefs: unexpected ahead-behind line: %q", line)
		}
		result[name] = Divergence{Ahead: aheadCount, Behind: behindCount}
	}
	return result, nil
}
