package route

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file cut between a page's snapshot check and its reads gives
// ErrChanged, not a page from the wrong bytes.
func TestPageOfAFileCutWhileReading(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("# Route\n\n## Run state\n\n")
	for i := range 200 {
		fmt.Fprintf(&b, "- 10:00 r%d\n", i)
	}
	file := filepath.Join(dir, RouteFile)
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeRead = func() { _ = os.Truncate(file, 10) }
	defer func() { beforeRead = func() {} }()
	if _, err := tail.Page(150, 100); !errors.Is(err, ErrChanged) {
		t.Fatalf("%v", err)
	}
}
