package core_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hclive"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// The input fixtures of spec section 17 ("Input race", "State guards",
// "`if` / first status"): this test binary, started by the core wrapper as
// the harness, in place of the shell stand-in. It stays in the suite and
// records what a real core does to it.

type fixtureMode int

const (
	fixtureNone fixtureMode = iota
	// fixtureControlled reports nothing until the test writes a status to
	// its control file, and records every byte it reads.
	fixtureControlled
	// fixtureDialog is fixtureControlled that also turns into a dialog, in
	// the same process, when input arrives: it reports blocked and prints a
	// prompt that an Enter confirms (N15).
	fixtureDialog
)

// fixtureArgv is the harness command of the fixture for execution exec. The
// mark lets the world's cleanup end it if it outlives its terminal.
func (w *liveWorld) fixtureArgv(exec string) []string {
	argv := []string{w.self, "fixture", "--base", w.fixtureBase(exec), "--mark", w.mark}
	if w.fixture == fixtureDialog {
		argv = append(argv, "--dialog")
	}
	return argv
}

// fixtureBase is the prefix of the fixture's files: .ctl (the status the
// test asks for), .in (every byte the program read), .dialog (written when
// the dialog opened), .confirmed (an Enter that reached the open dialog).
func (w *liveWorld) fixtureBase(exec string) string {
	return filepath.Join(w.core.Dir, "fixture-"+exec)
}

// fixtureMain is the fixture's program.
func fixtureMain(args []string) int {
	var base string
	dialogOnInput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--base":
			if i+1 < len(args) {
				base = args[i+1]
				i++
			}
		case "--dialog":
			dialogOnInput = true
		}
	}
	if base == "" {
		fmt.Fprintln(os.Stderr, "fixture: no --base")
		return 2
	}
	// Raw mode: the paste's first byte arrives before its Enter.
	stty := exec.Command("/bin/stty", "raw", "-echo")
	stty.Stdin = os.Stdin
	_ = stty.Run()
	in, err := os.OpenFile(base+".in", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var mu sync.Mutex
	report := func(state string) { fmt.Printf("\033]7501;state=%s:app=fixture\007", state) }
	dialog := false
	open := func() {
		if dialog {
			return
		}
		dialog = true
		report("blocked")
		fmt.Print("\r\nAllow the action? Enter confirms.\r\n")
		_ = os.WriteFile(base+".dialog", nil, 0o600)
	}
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n == 1 {
				_, _ = in.Write(buf)
				_ = in.Sync()
				mu.Lock()
				if dialog && (buf[0] == '\r' || buf[0] == '\n') {
					if f, err := os.OpenFile(base+".confirmed", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
						_, _ = f.Write(buf)
						_ = f.Close()
					}
				}
				if dialogOnInput {
					open()
				}
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		if b, err := os.ReadFile(base + ".ctl"); err == nil {
			_ = os.Remove(base + ".ctl")
			state := strings.TrimSpace(string(b))
			mu.Lock()
			if state == "blocked" {
				open()
			} else {
				dialog = false
				report(state)
			}
			mu.Unlock()
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// gStatus makes the fixture print state as its OSC 7501 status and returns
// the word core then shows (empty for none). A word core ignores leaves the
// one before it.
func (w *liveWorld) gStatus(rec runtime.Execution, state string) string {
	w.t.Helper()
	base := w.fixtureBase(rec.ID)
	if err := os.WriteFile(base+".ctl.tmp", []byte(state), 0o600); err != nil {
		w.t.Fatal(err)
	}
	if err := os.Rename(base+".ctl.tmp", base+".ctl"); err != nil {
		w.t.Fatal(err)
	}
	c := w.direct()
	word := ""
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(base + ".ctl"); err == nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		t, err := c.Get(testCtx(w.t), rec.TerminalAddress)
		if err != nil {
			w.t.Fatal(err)
		}
		word = ""
		if t.Status != nil {
			word = t.Status.State
		}
		if word == state {
			return word
		}
		time.Sleep(20 * time.Millisecond)
	}
	return word
}

func (w *liveWorld) gReceived(rec runtime.Execution) (string, bool) {
	b, err := os.ReadFile(w.fixtureBase(rec.ID) + ".in")
	if err != nil && !os.IsNotExist(err) {
		w.t.Fatal(err)
	}
	return string(b), true
}

func (w *liveWorld) gSends() []map[string]any {
	var out []map[string]any
	for _, line := range w.proxy.Requests() {
		var r struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal([]byte(line), &r) == nil && r.Method == hc.MethodSend {
			out = append(out, r.Params)
		}
	}
	return out
}

// State guards, against a real core: each status reaches core through the
// fixture's own OSC 7501 output.
func TestLiveTypedSubmitStateGuards(t *testing.T) {
	w := newLive(t, hclive.Options{})
	w.fixture = fixtureControlled
	gateTypedSubmitStateGuards(w)
}

// State guards for a first status, against a real core.
func TestLiveFirstStatusGuards(t *testing.T) {
	w := newLive(t, hclive.Options{})
	w.fixture = fixtureControlled
	gateFirstStatusGuards(w)
}

// Input race, against a real core, with the fixture that turns into a
// dialog when input arrives and the program lost under the submit. Then the
// record of N15: what core does when the paste itself opens the dialog.
func TestLiveInputRace(t *testing.T) {
	w := newLive(t, hclive.Options{})
	w.fixture = fixtureDialog
	gateInputRace(w)

	// N15, recorded: a person's typed submit to the idle fixture, whose
	// paste opens the dialog. Core checks `if` once, before the paste.
	ctx := testCtx(t)
	n, s, _ := submitServices(w)
	l, _ := w.gUp("n15", "n15-o", "orchestrator", "claude")
	e := w.gReady(l)
	if err := n.Approve(e, ""); err != nil {
		t.Fatal(err)
	}
	const line = "hs-n15-line"
	rec, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: line})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	base := w.fixtureBase(e.ID)
	waitFor(t, func() bool { got, _ := w.gReceived(e); return strings.Contains(got, line) })
	time.Sleep(time.Second) // the Enter follows the paste by about 200 ms
	got, _ := w.gReceived(e)
	_, opened := os.Stat(base + ".dialog")
	_, confirmed := os.Stat(base + ".confirmed")
	t.Logf("core %s, N15: submit %s (%s); the program read %q; dialog opened by the paste: %v; Enter reached the open dialog: %v",
		w.direct().Hello().Version, rec.State, rec.Detail, got, opened == nil, confirmed == nil)
	if opened != nil {
		t.Fatal("the paste did not open the fixture's dialog; the record proves nothing")
	}
	if confirmed == nil {
		t.Log("upstream limit: this core does not withhold the Enter from a dialog that opened during the paste; automated submit stays off (spec section 10, rule 1)")
	} else {
		t.Log("this core withheld the Enter from the dialog that opened during the paste")
	}
	if c := w.gCalls("terminal.keys"); c != 0 {
		t.Fatalf("terminal.keys reached core %d times", c)
	}
}

// First status and `if`: the person's typed submit goes out only after a
// first status, with `if` set to the state observed and force:false, and the
// program reads the line exactly once.
func TestLiveFirstStatusAndIf(t *testing.T) {
	w := newLive(t, hclive.Options{})
	w.fixture = fixtureControlled
	ctx := testCtx(t)
	n, s, _ := submitServices(w)
	w.gOpenApp()
	l, e := w.gUp("first", "first-o", "orchestrator", "claude")
	if err := n.Approve(w.gRecord(l.Intent), ""); err != nil {
		t.Fatal(err)
	}
	term, err := w.direct().Get(ctx, e.TerminalAddress)
	if err != nil || term.Status != nil {
		t.Fatalf("core holds a status before the program reported one: %+v %v", term.Status, err)
	}
	if _, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "hs-first-line"}); core.CodeOf(err) != core.CodeSubmitNoStatus {
		t.Fatalf("before the first status: %v", err)
	}
	if got := w.gSends(); len(got) != 0 {
		t.Fatalf("sends before the first status: %+v", got)
	}
	e = w.gReady(l) // the fixture reports idle: its first status
	if err := n.Approve(e, ""); err != nil {
		t.Fatal(err)
	}
	observed := ""
	if term, err := w.direct().Get(ctx, e.TerminalAddress); err == nil && term.Status != nil {
		observed = term.Status.State
	}
	rec, err := s.Submit(ctx, core.SubmitRequest{Execution: e.ID, Text: "hs-first-line"})
	if err != nil || rec.State != core.SubmitTyped {
		t.Fatalf("after the first status: %+v %v", rec, err)
	}
	got := w.gSends()
	if len(got) != 1 || got[0]["if"] != observed || got[0]["force"] != false || got[0]["text"] != "hs-first-line" {
		t.Fatalf("the request core got: %+v (observed %q)", got, observed)
	}
	waitFor(t, func() bool { in, _ := w.gReceived(e); return strings.Contains(in, "hs-first-line") })
	time.Sleep(time.Second)
	in, _ := w.gReceived(e)
	if strings.Count(in, "hs-first-line") != 1 {
		t.Fatalf("the program read %q", in)
	}
	t.Logf("core %s typed %q for if=%q force=false", w.direct().Hello().Version, in, observed)
	if c := w.gCalls("terminal.keys"); c != 0 {
		t.Fatalf("terminal.keys reached core %d times", c)
	}
}
