package wtree

import (
	"context"
	"testing"

	"github.com/arafathusayn/herdsman/internal/gitrefs"
)

func TestIntegrationFollowsTheCounts(t *testing.T) {
	for _, c := range []struct {
		d    gitrefs.Divergence
		want Integration
	}{
		{gitrefs.Divergence{}, IntegrationSame},
		{gitrefs.Divergence{Behind: 2}, IntegrationAncestor},
		{gitrefs.Divergence{Ahead: 1}, IntegrationNone},
		{gitrefs.Divergence{Ahead: 1, Behind: 1}, IntegrationNone},
	} {
		if got := integrationOf(c.d); got != c.want {
			t.Errorf("%+v: %q", c.d, got)
		}
	}
}

// The cell is the same whether the counts were computed or came from the cache.
func TestIntegrationIsSetOnAComputedAndACachedRow(t *testing.T) {
	f := newFixture(t, 1)
	for _, pass := range []string{"computed", "cached"} {
		rows, err := f.coll.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if row := rowByTask(t, rows, "t00"); !row.HasDivergence || row.Integration != IntegrationSame {
			t.Fatalf("%s: %+v", pass, row)
		}
	}
}
