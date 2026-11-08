package yaml

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// FuzzDecode checks that the reader never crashes, returns only its own error
// type, and is deterministic. The committed corpus runs in the normal test
// run.
func FuzzDecode(f *testing.F) {
	files, err := filepath.Glob("testdata/corpus/*")
	if err != nil {
		f.Fatal(err)
	}
	if len(files) == 0 {
		f.Fatal("testdata/corpus is empty")
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := Decode(data)
		if err != nil {
			if _, ok := err.(*Error); !ok {
				t.Fatalf("error of type %T: %v", err, err)
			}
			return
		}
		second, err := Decode(data)
		if err != nil || !tree.Equal(first, second) {
			t.Fatalf("second Decode differs: %v", err)
		}
	})
}
