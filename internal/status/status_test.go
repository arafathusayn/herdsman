package status

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

func snapshot(t *testing.T, o Options) string {
	t.Helper()
	var b bytes.Buffer
	Write(context.Background(), &b, o)
	return b.String()
}

func match(t *testing.T, name, re, s string) {
	t.Helper()
	if !regexp.MustCompile("(?m)" + re).MatchString(s) {
		t.Errorf("%s: want /%s/ in:\n%s", name, re, s)
	}
}

func noMatch(t *testing.T, name, re, s string) {
	t.Helper()
	if regexp.MustCompile("(?m)" + re).MatchString(s) {
		t.Errorf("%s: unexpected /%s/ in:\n%s", name, re, s)
	}
}

// A1 and A2 of run-tests.sh: the machine line and the tree checks.
func TestMachineLineAndTrees(t *testing.T) {
	quiet := filepath.Join(t.TempDir(), "quiet-tree")
	if err := os.Mkdir(quiet, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	o := Options{Specs: ParseSpecs("im-a:w9:p1:" + quiet), LimitMin: 30, Now: now, Machine: ReadMachine()}
	out := snapshot(t, o)
	match(t, "A1 machine line", `^machine: load [0-9.]+ \(5 min\) on [0-9]+ cores, memory free [0-9?]+%`, out)
	match(t, "A1b quiet tree flagged", `under quiet-tree: 0  <-- NO WRITES in 10m`, out)
	if err := os.WriteFile(filepath.Join(quiet, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = snapshot(t, o)
	match(t, "A1c a fresh write clears the flag", `under quiet-tree: 1$`, out)
	o.Specs = ParseSpecs("im-a:w9:p1:" + filepath.Join(t.TempDir(), "no-such-tree"))
	out = snapshot(t, o)
	match(t, "A2 a tree path that does not exist is flagged", `tree not found: .*no-such-tree  <-- TREE MISSING`, out)
	noMatch(t, "A2b and is not read as no writes", `NO WRITES`, out)
}

func TestWriteCountSkipsCachesAndStopsAtFifty(t *testing.T) {
	tree := t.TempDir()
	for _, d := range []string{"node_modules", ".git", "target", "src"} {
		_ = os.MkdirAll(filepath.Join(tree, d), 0o755)
		_ = os.WriteFile(filepath.Join(tree, d, "f"), nil, 0o644)
	}
	_ = os.WriteFile(filepath.Join(tree, ".next"), nil, 0o644) // a pruned name that is a file
	_ = os.Symlink(filepath.Join(tree, "src", "f"), filepath.Join(tree, "link"))
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(tree, "src", "f"), old, old)
	if n := RecentWrites(tree, time.Now().Add(-10*time.Minute), 50); n != 0 {
		t.Fatalf("count %d, want 0", n)
	}
	for i := range 60 {
		_ = os.WriteFile(filepath.Join(tree, "src", fmt.Sprintf("n%d", i)), nil, 0o644)
	}
	out := snapshot(t, Options{Specs: []Spec{{Name: "im-a", Pane: "p", Tree: tree}}, LimitMin: 30, Now: time.Now()})
	match(t, "fifty or more writes", `under [^ ]+: 50\+$`, out)
	if n := RecentWrites(filepath.Join(tree, "nope"), time.Now(), 50); n != 0 {
		t.Fatal("a missing tree counts nothing")
	}
}

func TestAgentLines(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	a := herdrtest.Agent("w9:p1", "s")
	a["name"], a["agent_status"] = "im-a", "working"
	fx.SetAgent("w9:p1", a)
	fx.SetScreen("w9:p1", strings.Join([]string{
		"gpt-5.1-codex high · 40% left",
		"└ ◆ npm test  running  23m 45s",
		"└ ◆ a long job  running  1h 02m",
		"└ ◆ short  running  2m 1s",
		"└ ◆ workflow " + strings.Repeat("é", 70) + " 5m",
		"model Opus 4.7",
		"• Working (3m • esc to interrupt)",
	}, "\n")+"\n")
	out := snapshot(t, Options{Specs: ParseSpecs("im-a:w9:p1: rv-1:w9:p9:"), LimitMin: 30, Client: herdr.New(srv.Socket), Now: time.Date(2026, 1, 1, 12, 0, 1, 0, time.Local),
		Machine: Machine{Load5: "9.50", CPUs: "8", Free: "5"}})
	match(t, "the time", `^12:00:01$`, out)
	match(t, "LOAD HIGH", `^   <-- LOAD HIGH$`, out)
	match(t, "MEMORY LOW", `^   <-- MEMORY LOW$`, out)
	match(t, "state and the last model", `^== im-a \(w9:p1\) state=working model=Opus 4.7$`, out)
	noMatch(t, "only the last three jobs", `npm test`, out)
	match(t, "an hour job is over the limit", `^   bg: a long job  running  1h 02m \[62m\]  <-- OVER 30m$`, out)
	match(t, "a short job is not", `^   bg: short  running  2m 1s \[2m\]$`, out)
	match(t, "a workflow row, cut at 70 characters", "^   bg: workflow "+strings.Repeat("é", 61)+` \[5m\]$`, out)
	match(t, "the foreground line", `^   fg: • Working \(3m • esc to interrupt\)$`, out)
	match(t, "an unknown agent", `^== rv-1 \(w9:p9\) state= model=$`, out)
	noMatch(t, "no tree, no write line", `files changed`, out)

	out = snapshot(t, Options{Specs: ParseSpecs("x"), Machine: Machine{}, LimitMin: 5, Now: time.Now()})
	match(t, "unknown machine values", `^machine: load \? \(5 min\) on \? cores, memory free \?%$`, out)
	noMatch(t, "no machine flags without values", `LOAD HIGH|MEMORY LOW`, out)
}

func TestParseSpecs(t *testing.T) {
	got := ParseSpecs(" im-a:w7:p14:/abs/repo  rv-1:w7:p17: solo ")
	want := []Spec{{"im-a", "w7:p14", "/abs/repo"}, {"rv-1", "w7:p17", ""}, {"solo", "solo", "solo"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if got := ParseSpecs("a:p"); got[0].Pane != "p" || got[0].Tree != "p" {
		t.Fatalf("got %+v", got)
	}
}

func TestFlags(t *testing.T) {
	got := Flags(strings.Join([]string{"12:00:00", "machine: load 9.00 (5 min) on 8 cores, memory free 50%", "   <-- LOAD HIGH",
		"== im-a (w9:p1) state= model=", "   files changed in last 10m under t: 0  <-- NO WRITES in 10m",
		"   bg: x [40m]  <-- OVER 30m", "   bg: y [41m]  <-- OVER 30m", "== ", "  <-- ODD"}, "\n"))
	want := []string{" ODD", "im-a NO WRITES in 10m", "im-a OVER 30m", "machine LOAD HIGH"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

// R2 finding 2: an explicit LIMIT_MIN=0 flags every job, as the script's
// [ "$mins" -ge 0 ] does; Write takes the limit as given.
func TestLimitZeroFlagsEveryJob(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	a := herdrtest.Agent("w9:p1", "s")
	a["name"], a["agent_status"] = "im-a", "working"
	fx.SetAgent("w9:p1", a)
	fx.SetScreen("w9:p1", "└ ◆ short  running  2m 1s\n")
	out := snapshot(t, Options{Specs: ParseSpecs("im-a:w9:p1:"), LimitMin: 0, Client: herdr.New(srv.Socket), Now: time.Now(),
		Machine: Machine{Load5: "0.10", CPUs: "8", Free: "50"}})
	match(t, "a two-minute job is over a zero limit", `^   bg: short  running  2m 1s \[2m\]  <-- OVER 0m$`, out)
}
