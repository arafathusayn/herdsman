package runtime

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestResolveFollowsTheSpecOrder(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want Selection
	}{
		{"installation", Inputs{Installation: BackendCore, Experimental: true}, Selection{Backend: BackendCore, Source: SourceInstallation}},
		{"config over installation", Inputs{Config: BackendTUI, ConfigPath: "/srv/c.toml", Installation: BackendCore}, Selection{Backend: BackendTUI, Source: SourceConfig, ConfigPath: "/srv/c.toml"}},
		{"flag over config", Inputs{Flag: BackendCore, Config: BackendTUI, Installation: BackendTUI, Experimental: true}, Selection{Backend: BackendCore, Source: SourceFlag}},
		{"route over everything", Inputs{Config: BackendCore, Installation: BackendCore, Persisted: BackendTUI}, Selection{Backend: BackendTUI, Source: SourceRoute}},
		{"core route without the marker", Inputs{Installation: BackendTUI, Persisted: BackendCore}, Selection{Backend: BackendCore, Source: SourceRoute}},
		{"route with its own flag", Inputs{Flag: BackendTUI, Persisted: BackendTUI}, Selection{Backend: BackendTUI, Source: SourceRoute}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve(c.in)
			if err != nil || got != c.want {
				t.Fatalf("%+v %v, want %+v", got, err, c.want)
			}
		})
	}
}

func TestResolveRefusesAMigrationAndAMisspelling(t *testing.T) {
	_, err := Resolve(Inputs{Flag: BackendCore, Persisted: BackendTUI, Installation: BackendTUI})
	if !errors.Is(err, ErrMigrationRequired) || CodeOf(err) != CodeBackendMigrationRequired || !strings.Contains(err.Error(), "--backend herdr-core") {
		t.Fatalf("%v", err)
	}
	for name, in := range map[string]Inputs{
		"flag":         {Flag: "herdr-gui", Installation: BackendTUI},
		"config":       {Config: "tui", Installation: BackendTUI},
		"installation": {Installation: "core"},
		"route":        {Persisted: "x", Installation: BackendTUI},
	} {
		if _, err := Resolve(in); CodeOf(err) != CodeUnknownBackend {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Resolve(Inputs{}); CodeOf(err) != CodeUnknownBackend {
		t.Fatalf("%v", err)
	}
}

// Core is selected without the former experimental marker, from every source.
// Keeping that marker in an older config must not change selection or warnings.
func TestCoreNeedsNoExperimentMarker(t *testing.T) {
	for name, in := range map[string]Inputs{
		"flag":         {Flag: BackendCore, Installation: BackendTUI},
		"config":       {Config: BackendCore, ConfigPath: "/srv/c.toml", Installation: BackendTUI},
		"installation": {Installation: BackendCore},
	} {
		t.Run(name, func(t *testing.T) {
			without, err := Resolve(in)
			if err != nil || without.Backend != BackendCore || without.Experimental {
				t.Fatalf("without marker: %+v %v", without, err)
			}
			in.Experimental = true
			with, err := Resolve(in)
			if err != nil || with != without {
				t.Fatalf("legacy marker changed selection: %+v %v", with, err)
			}
		})
	}
	sel, err := Resolve(Inputs{Installation: BackendTUI, Experimental: true})
	if err != nil || sel.Backend != BackendTUI || sel.Experimental {
		t.Fatalf("explicit TUI: %+v %v", sel, err)
	}
}

func TestLegacyRecordsAreTUI(t *testing.T) {
	if LegacyBackend("") != BackendTUI || LegacyBackend(BackendCore) != BackendCore {
		t.Fatal("legacy decoding")
	}
}

// No fallback: a backend the build lacks is refused, and the refusal names
// the line that selects the Herdr TUI.
func TestRegistryRefusesAMissingBackendNamingTheTUI(t *testing.T) {
	opened := ""
	r := Registry{BackendTUI: func(endpoint string) (Provider, error) { opened = endpoint; return nil, nil }}
	if _, err := r.Open(BackendTUI, "/srv/herdr.sock"); err != nil || opened != "/srv/herdr.sock" {
		t.Fatalf("%v %q", err, opened)
	}
	_, err := r.Open(BackendCore, "/srv/hc.sock")
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), `backend = "herdr-tui"`) || !strings.Contains(err.Error(), "not available in this build") {
		t.Fatalf("%v", err)
	}
	if err := r.Available(BackendCore); CodeOf(err) != CodeBackendUnavailable {
		t.Fatalf("%v", err)
	}
	if _, err := r.Open("herdr", ""); CodeOf(err) != CodeUnknownBackend {
		t.Fatalf("%v", err)
	}
	if opened != "/srv/herdr.sock" {
		t.Fatal("a refused backend reached a factory")
	}
}

func TestProgramOfKeepsUnknownApartFromIdle(t *testing.T) {
	for raw, want := range map[string]Program{"idle": ProgramIdle, "working": ProgramWorking, "blocked": ProgramBlocked, "done": ProgramDone, "error": ProgramError, "": ProgramUnknown, "compacting": ProgramUnknown} {
		if got := ProgramOf(raw); got != want {
			t.Errorf("%q: %s, want %s", raw, got, want)
		}
	}
}

func TestExecutionIDsAreRandomUUIDs(t *testing.T) {
	shape := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 64 {
		id := NewExecutionID()
		if !shape.MatchString(id) || seen[id] {
			t.Fatalf("%q", id)
		}
		seen[id] = true
	}
}
