package daemon

import (
	"fmt"
	"path/filepath"
	"testing"
)

// Item 0a (review a-7 P2): a full hash cache evicts one entry for each
// new one, and setup bounds the proof's files below the cache, so a
// proof's own files never evict each other.

func TestHashCacheEvictsOne(t *testing.T) {
	dir := t.TempDir()
	var c hashCache
	for i := range hashCacheMax {
		if _, err := c.sum(writeFile(t, dir, fmt.Sprintf("f%d", i), "x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.sum(writeFile(t, dir, "one-more", "y")); err != nil {
		t.Fatal(err)
	}
	if len(c.m) != hashCacheMax {
		t.Fatalf("a full cache keeps %d entries after one miss, want %d (one evicted)", len(c.m), hashCacheMax)
	}
	if _, ok := c.m[filepath.Join(dir, "one-more")]; !ok {
		t.Fatal("the new entry is not cached")
	}
}

func TestSetupBoundsRules(t *testing.T) {
	x, _, q := proofRoute(t)
	dir := t.TempDir()
	q.Rules = nil
	for i := range maxSetupRules + 1 {
		q.Rules = append(q.Rules, writeFile(t, dir, fmt.Sprintf("rule-%d.sb", i), "(allow default)\n"))
	}
	if _, e := x.r.setup(q); e == nil || e.Code != "bad_request" {
		t.Fatalf("setup with %d rules: %v, want bad_request", len(q.Rules), e)
	}
	q.Rules = q.Rules[:maxSetupRules]
	if _, e := x.r.setup(q); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if !x.r.proofValid() {
			t.Fatal("proof not valid")
		}
	}
	if len(x.r.hashes.m) != maxSetupRules+1 {
		t.Fatalf("the cache holds %d of the proof's %d files", len(x.r.hashes.m), maxSetupRules+1)
	}
}
