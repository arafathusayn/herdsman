package toml

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// suiteEnv names the tests directory of a toml-test checkout. The workflow
// encoding-suites.yml sets it; without it the official suite is skipped.
const suiteEnv = "HERDSMAN_TOML_TEST_DIR"

// TestOfficialSuite runs the TOML 1.0 list of the official suite through the
// reader and both writers. No case is left out, so there is no list of
// unsupported cases for TOML.
func TestOfficialSuite(t *testing.T) {
	dir := os.Getenv(suiteEnv)
	if dir == "" {
		t.Skip(suiteEnv + " is not set")
	}
	list, err := os.ReadFile(filepath.Join(dir, "files-toml-1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	valid, invalid := 0, 0
	for _, name := range strings.Fields(string(list)) {
		if !strings.HasSuffix(name, ".toml") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, "invalid/") {
			invalid++
			t.Run(name, func(t *testing.T) { checkInvalid(t, src) })
			continue
		}
		valid++
		expected, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(name, ".toml")+".json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) { checkValid(t, src, expected) })
	}
	if valid == 0 || invalid == 0 {
		t.Fatalf("the list held %d valid and %d invalid cases", valid, invalid)
	}
	t.Logf("official suite: %d valid, %d invalid", valid, invalid)
}

func checkInvalid(t *testing.T, src []byte) {
	if _, err := Decode(src); err == nil {
		t.Fatalf("accepted invalid input:\n%s", src)
	}
}

func checkValid(t *testing.T, src, expected []byte) {
	doc, err := Parse(src)
	if err != nil {
		t.Fatalf("rejected valid input: %v\n%s", err, src)
	}
	if got := doc.Encode(); !bytes.Equal(got, src) {
		t.Fatalf("Document.Encode changed the file: %q", got)
	}
	var want any
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatal(err)
	}
	if !matches(t, doc.Tree(), want) {
		t.Fatalf("decoded tree differs from %s\ninput:\n%s", expected, src)
	}
	out, err := Encode(doc.Tree())
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	again, err := Decode(out)
	if err != nil {
		t.Fatalf("Decode of the writer's output: %v\n%s", err, out)
	}
	if !tree.Equal(doc.Tree(), again) {
		t.Fatalf("the writer changed the values:\n%s", out)
	}
}

// matches compares a tree with the suite's tagged JSON form.
func matches(t *testing.T, n *tree.Node, want any) bool {
	switch w := want.(type) {
	case []any:
		if n.Kind != tree.Array || len(n.Items) != len(w) {
			return false
		}
		for i := range w {
			if !matches(t, n.Items[i], w[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		if typ, ok := w["type"].(string); ok && len(w) == 2 {
			if val, ok := w["value"].(string); ok {
				return matchesScalar(n, typ, val)
			}
		}
		if n.Kind != tree.Table || n.Len() != len(w) {
			return false
		}
		for k, v := range w {
			got, ok := n.Get(k)
			if !ok || !matches(t, got, v) {
				return false
			}
		}
		return true
	}
	t.Fatalf("unexpected JSON value %v", want)
	return false
}

func matchesScalar(n *tree.Node, typ, val string) bool {
	switch typ {
	case "string":
		return n.Kind == tree.String && n.Str == val
	case "integer":
		i, err := strconv.ParseInt(val, 10, 64)
		return err == nil && n.Kind == tree.Integer && n.Int == i
	case "float":
		return n.Kind == tree.Float && sameFloat(n.Flt, val)
	case "bool":
		return n.Kind == tree.Bool && strconv.FormatBool(n.Bool) == val
	case "datetime":
		return n.Kind == tree.OffsetDateTime && sameTime(n.Str, val, "2006-01-02T15:04:05.999999999Z07:00")
	case "datetime-local":
		return n.Kind == tree.LocalDateTime && sameTime(n.Str, val, "2006-01-02T15:04:05.999999999")
	case "date-local":
		return n.Kind == tree.LocalDate && n.Str == val
	case "time-local":
		return n.Kind == tree.LocalTime && sameTime(n.Str, val, "15:04:05.999999999")
	}
	return false
}

func sameFloat(got float64, val string) bool {
	switch val {
	case "nan", "+nan", "-nan":
		return math.IsNaN(got)
	}
	want, err := strconv.ParseFloat(strings.ReplaceAll(val, "inf", "Inf"), 64)
	return err == nil && (got == want || math.Abs(got-want) <= math.Abs(want)*1e-15)
}

func sameTime(got, want, layout string) bool {
	normal := strings.ToUpper(strings.Replace(got, " ", "T", 1))
	a, errA := time.Parse(layout, normal)
	b, errB := time.Parse(layout, strings.ToUpper(want))
	return errA == nil && errB == nil && a.Equal(b)
}
