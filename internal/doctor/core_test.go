package doctor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// The holder command lines are the ones herdr-core 0.1.19 shows: under the
// name hc when hc started the core, under herdr-core when the Herdr app did.
func TestCheckCore(t *testing.T) {
	const (
		hold    = "/opt/bin/hc hold --socket /run/herdr-core/default.holder.3cd8168b.sock"
		appHold = "/opt/bin/herdr-core hold --socket /state/herdr-core/default/default.holder.8f6529c6.sock"
	)
	cases := []struct {
		name   string
		state  *CoreState
		err    error
		level  string // "" for no line
		detail string // a part of the detail
	}{
		{name: "not installed"},
		{name: "not running", state: &CoreState{Version: "hc 0.1.19"}, level: OK, detail: "hc 0.1.19 is not running"},
		{name: "no live terminal", state: &CoreState{Version: "hc 0.1.19", Running: true}, level: OK, detail: "no live terminal"},
		{name: "a holder named hc", state: &CoreState{Version: "hc 0.1.19", Running: true, Holders: []string{hold}}, level: OK, detail: "knows its holder process (hc hold)"},
		{name: "a holder named herdr-core", state: &CoreState{Version: "hc 0.1.19", Running: true, Holders: []string{appHold}}, level: OK, detail: "knows its holder process (herdr-core hold)"},
		{name: "a renamed subcommand", state: &CoreState{Version: "hc 0.2.0", Running: true, Holders: []string{"/opt/bin/hc keep --socket /run/x.sock"}}, level: OK, detail: "(hc keep)"},
		{name: "a holder under another program", state: &CoreState{Version: "hc 0.2.0", Running: true, Holders: []string{hold, "/opt/libexec/hc-holder --socket /run/x.sock"}}, level: Fail, detail: "hc 0.2.0: the launcher check does not know its holder process (hc-holder --socket)"},
		{name: "the status cannot be read", err: errors.New("hc server status: no holders list"), level: Warn, detail: "not tested against herdr-core: hc server status: no holders list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fakes(t, nil)
			p.Core = func(context.Context) (*CoreState, error) { return tc.state, tc.err }
			got := find(run(t, p), "herdr-core")
			if tc.level == "" {
				if len(got) != 0 {
					t.Fatalf("got %v, want no line", got)
				}
				return
			}
			if len(got) != 1 || got[0].Level != tc.level || !strings.Contains(got[0].Detail, tc.detail) {
				t.Fatalf("got %v, want one %s line with %q", got, tc.level, tc.detail)
			}
		})
	}
}

func TestNoCoreProbeAddsNoLine(t *testing.T) {
	if got := find(run(t, fakes(t, nil)), "herdr-core"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestAnUnknownHolderFailsTheReport(t *testing.T) {
	p := fakes(t, nil)
	p.Core = func(context.Context) (*CoreState, error) {
		return &CoreState{Version: "hc 0.2.0", Running: true, Holders: []string{"/opt/libexec/hc-holder"}}, nil
	}
	if !Failed(run(t, p)) {
		t.Fatal("an unknown holder did not fail the report")
	}
}

// The status is the one herdr-core 0.1.19 prints, with and without a live
// terminal.
func TestHolderPIDs(t *testing.T) {
	cases := []struct {
		name, status string
		want         []int
		err          string
	}{
		{"one holder", `{"core":{"id":"c4"},"finished":0,"holder":{"current":true,"instance":"4b","pid":629,"terminals":1},"holders":[{"current":true,"instance":"4b","pid":629,"terminals":1}],"live":1,"pid":565,"version":"0.1.19"}`, []int{629}, ""},
		{"two holders", `{"holders":[{"pid":629},{"pid":700}]}`, []int{629, 700}, ""},
		{"no live terminal", `{"holder":null,"holders":[],"live":0,"pid":565,"version":"0.1.19"}`, nil, ""},
		{"a holder without a process", `{"holders":[{"pid":0},{"instance":"4b"}]}`, nil, ""},
		{"no holders list", `{"live":1,"pid":565}`, nil, "no holders list"},
		{"not JSON", `core is starting`, nil, "not JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := holderPIDs([]byte(tc.status))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %v, want an error with %q", err, tc.err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("got %v %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestProcessName(t *testing.T) {
	for command, want := range map[string]string{
		"/opt/bin/hc hold --socket /run/x.sock": "hc hold",
		"/opt/bin/herdr-core serve":             "herdr-core serve",
		"hc":                                    "hc",
		"":                                      "no command line",
	} {
		if got := processName(command); got != want {
			t.Errorf("processName(%q) = %q, want %q", command, got, want)
		}
	}
}
