package catalog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Each way a list cannot be read is reported as unavailable with its
// reason: a missing claude (still listed), a failing codex, an other kind
// whose --help fails, and one whose models listing names none.
func TestPipelineFailures(t *testing.T) {
	bin := t.TempDir()
	fakeBin(t, bin, "herdr", `echo "  [possible values: claude, codex, zed, kilo]"`)
	fakeBin(t, bin, "codex", `exit 1`)
	fakeBin(t, bin, "zed", `exit 2`)
	fakeBin(t, bin, "kilo", `case "$1" in --help) printf 'Commands:\n  models   list models\n';; models) echo 'nothing here!';; esac`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
	got := map[string]string{}
	for _, k := range Pipeline(context.Background(), ExecRunner, "") {
		got[k.Kind] = k.Unavailable
		if !k.Offline || len(k.Models) != 0 {
			t.Errorf("%s %+v", k.Kind, k)
		}
	}
	want := map[string]string{
		"claude": "not found on PATH",
		"codex":  "codex debug models: exit status 1",
		"zed":    "zed --help: exit status 2",
		"kilo":   "kilo models: lists no models",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v", got)
	}
	if e := HerdrError(context.Background(), ExecRunner); e != "" {
		t.Errorf("herdr answered, yet %q", e)
	}
}

// A listing is cut at 500 models.
func TestParseModelLinesCap(t *testing.T) {
	var b strings.Builder
	for i := range 600 {
		fmt.Fprintf(&b, "p/m%d\n", i)
	}
	ms, err := ParseModelLines([]byte(b.String()), nil, "")
	if err != nil || len(ms) != 500 {
		t.Fatalf("%d %v", len(ms), err)
	}
}
