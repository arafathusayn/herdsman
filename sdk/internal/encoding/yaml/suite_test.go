package yaml

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// suiteEnv names a checkout of the yaml-test-suite data branch. The workflow
// encoding-suites.yml sets it; without it the official suite is skipped.
const suiteEnv = "HERDSMAN_YAML_TEST_DIR"

// loadUnsupported reads the committed list: one case id per line, then the
// reason. The list is the named record of what the subset leaves out.
func loadUnsupported(t *testing.T) map[string]string {
	file, err := os.Open("testdata/suite-unsupported.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	listed := map[string]string{}
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		id, reason, ok := strings.Cut(line, " ")
		if !ok || strings.TrimSpace(reason) == "" {
			t.Fatalf("line without a reason: %q", line)
		}
		listed[id] = strings.TrimSpace(reason)
	}
	return listed
}

// TestOfficialSuite passes every case that is not listed, and fails when a
// listed case starts passing or an id in the list no longer exists, so the
// list cannot go stale.
func TestOfficialSuite(t *testing.T) {
	dir := os.Getenv(suiteEnv)
	if dir == "" {
		t.Skip(suiteEnv + " is not set")
	}
	listed := loadUnsupported(t)
	seen := map[string]bool{}
	passed := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Name() != "in.yaml" {
			return err
		}
		caseDir := filepath.Dir(path)
		rel, err := filepath.Rel(dir, caseDir)
		if err != nil {
			return err
		}
		id := filepath.ToSlash(rel)
		seen[id] = true
		problem := checkCase(t, caseDir)
		_, isListed := listed[id]
		switch {
		case isListed && problem == "":
			t.Errorf("%s: listed as unsupported but passes; remove it from the list", id)
		case !isListed && problem != "":
			t.Errorf("%s: %s", id, problem)
		case problem == "":
			passed++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for id := range listed {
		if !seen[id] {
			t.Errorf("%s: listed but not in the suite", id)
		}
	}
	if passed == 0 {
		t.Fatal("no case ran")
	}
	t.Logf("official suite: %d passed, %d listed as unsupported", passed, len(listed))
}

// checkCase returns what is wrong with the reader's answer to one case, or ""
// when it is right. A construct the subset refuses counts as wrong here, so
// that each one must be listed by name.
func checkCase(t *testing.T, dir string) string {
	src, err := os.ReadFile(filepath.Join(dir, "in.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, decodeErr := Decode(src)
	if _, err := os.Stat(filepath.Join(dir, "error")); err == nil {
		if decodeErr == nil {
			return "accepted invalid input"
		}
		return ""
	}
	if decodeErr != nil {
		if errors.Is(decodeErr, ErrUnsupported) {
			return "unsupported: " + decodeErr.Error()
		}
		return "rejected valid input: " + decodeErr.Error()
	}
	expected, err := os.ReadFile(filepath.Join(dir, "in.json"))
	if err != nil {
		return "" // The suite gives no JSON form for this case; parsing is the check.
	}
	dec := json.NewDecoder(bytes.NewReader(expected))
	var want any
	if err := dec.Decode(&want); err != nil {
		if errors.Is(err, io.EOF) {
			want = nil
		} else {
			t.Fatal(err)
		}
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "the suite expects several documents, but the reader returned one"
	}
	if !matches(got, want) {
		return "wrong value: got " + show(got) + ", want " + strings.TrimSpace(string(expected))
	}
	return ""
}

func matches(n *tree.Node, want any) bool {
	switch w := want.(type) {
	case nil:
		return n.Kind == tree.Null
	case bool:
		return n.Kind == tree.Bool && n.Bool == w
	case string:
		return n.Kind == tree.String && n.Str == w
	case float64:
		switch n.Kind {
		case tree.Integer:
			return float64(n.Int) == w
		case tree.Float:
			return n.Flt == w || math.Abs(n.Flt-w) <= math.Abs(w)*1e-15
		}
		return false
	case []any:
		if n.Kind != tree.Array || len(n.Items) != len(w) {
			return false
		}
		for i := range w {
			if !matches(n.Items[i], w[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		if n.Kind != tree.Table || n.Len() != len(w) {
			return false
		}
		for k, v := range w {
			got, ok := n.Get(k)
			if !ok || !matches(got, v) {
				return false
			}
		}
		return true
	}
	return false
}
