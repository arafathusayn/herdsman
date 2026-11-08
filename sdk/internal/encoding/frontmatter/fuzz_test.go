package frontmatter

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FuzzParse checks that the parser never crashes and that the body it returns
// is always the end of the input. The committed corpus runs in the normal
// test run.
func FuzzParse(f *testing.F) {
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
		doc, err := Parse(data)
		if err != nil {
			return
		}
		if doc.Meta == nil || !bytes.HasSuffix(data, doc.Body) {
			t.Fatalf("doc = %+v for %q", doc, data)
		}
	})
}
