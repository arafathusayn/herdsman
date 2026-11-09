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

// BranchDivergence is one branch's counts and the commit they were computed
// for.
type BranchDivergence struct {
	Object string
	Divergence
}

// AheadBehind compares every branch with base in one git process, keyed by
// full ref name. Each entry names the tip git read together with the counts, so
// a caller can tell counts for the commit it holds from counts for a commit the
// branch moved to meanwhile, even if it has since moved back.
func (r *Reader) AheadBehind(ctx context.Context, base string) (map[string]BranchDivergence, error) {
	if base == "" || strings.ContainsAny(base, ")%\n\x00") {
		return nil, fmt.Errorf("gitrefs: invalid base %q", base)
	}
	out, err := r.runGit(ctx, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(ahead-behind:"+base+")", "refs/heads")
	if err != nil {
		return nil, err
	}
	return parseAheadBehind(out)
}

func parseAheadBehind(out string) (map[string]BranchDivergence, error) {
	result := map[string]BranchDivergence{}
	for line := range strings.Lines(out) {
		fields := strings.Split(strings.TrimSuffix(line, "\n"), "\x00")
		if len(fields) != 3 {
			return nil, fmt.Errorf("gitrefs: unexpected ahead-behind line: %q", line)
		}
		ahead, behind, spaced := strings.Cut(fields[2], " ")
		aheadCount, aheadErr := strconv.Atoi(ahead)
		behindCount, behindErr := strconv.Atoi(behind)
		if !spaced || aheadErr != nil || behindErr != nil {
			return nil, fmt.Errorf("gitrefs: unexpected ahead-behind line: %q", line)
		}
		result[fields[0]] = BranchDivergence{Object: fields[1], Divergence: Divergence{Ahead: aheadCount, Behind: behindCount}}
	}
	return result, nil
}
