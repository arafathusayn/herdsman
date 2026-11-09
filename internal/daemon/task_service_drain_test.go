package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/wtree"
)

func TestDrainDeletesOnlyInsideTheTrash(t *testing.T) {
	common := t.TempDir()
	if err := startDrain(common, filepath.Join(common, "elsewhere")); err == nil {
		t.Fatal("drained outside the trash")
	}
	source := filepath.Join(common, "tree")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "f"), []byte("f"), 0o600); err != nil {
		t.Fatal(err)
	}
	trashed, err := wtree.MoveToTrash(common, source, "a-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := startDrain(common, trashed); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(trashed); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the drain did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
