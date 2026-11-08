package toml

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// addCorpus seeds a fuzz target with the committed corpus, so that the normal
// test run executes the target on every file.
func addCorpus(f *testing.F) {
	f.Helper()
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
}

// FuzzDecode checks that the reader never crashes, that a parsed document
// writes back every byte, and that a decoded tree survives the plain writer.
func FuzzDecode(f *testing.F) {
	addCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := Parse(data)
		if err != nil {
			return
		}
		if got := doc.Encode(); !bytes.Equal(got, data) {
			t.Fatalf("Encode() = %q, want %q", got, data)
		}
		out, err := Encode(doc.Tree())
		if err != nil {
			t.Fatalf("Encode of a decoded tree: %v", err)
		}
		again, err := Decode(out)
		if err != nil {
			t.Fatalf("Decode(%q): %v", out, err)
		}
		if !tree.Equal(doc.Tree(), again) {
			t.Fatalf("round trip changed the values: %q became %q", data, out)
		}
	})
}

// FuzzEdit checks that an edit either applies and reads back, or leaves the
// document as it was.
func FuzzEdit(f *testing.F) {
	addCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := Parse(data)
		if err != nil {
			return
		}
		path := []string{"worktree", "path"}
		value := tree.NewString("edited")
		if err := doc.Set(path, value); err != nil {
			if got := doc.Encode(); !bytes.Equal(got, data) {
				t.Fatalf("a failed Set changed the document: %q", got)
			}
			return
		}
		if got, ok := doc.Tree().Lookup(path...); !ok || !tree.Equal(got, value) {
			t.Fatalf("Set did not read back from %q", doc.Encode())
		}
		if removed, err := doc.Unset(path); err != nil || !removed {
			t.Fatalf("Unset = %v, %v on %q", removed, err, doc.Encode())
		}
	})
}
