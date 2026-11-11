package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The [runtime] section is strict: a misspelled backend must not fall through
// to the installation default.
func TestUnknownRuntimeKeyStops(t *testing.T) {
	for name, text := range map[string]string{
		"backend":  "[runtime]\nbakend = \"herdr-tui\"\n",
		"core":     "[runtime.core]\nsocekt = \"/srv/hc.sock\"\n",
		"app":      "[runtime.app]\nhoem = \"/srv/app\"\n",
		"tui view": "[runtime.tui_view]\nenabeld = true\n",
		"section":  "[runtime.mystery]\na = 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadFile(configFile(t, text))
			var strict *StrictError
			if !errors.As(err, &strict) || !strings.HasPrefix(strict.Warning.Key, "runtime.") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRuntimeSectionKeepsOmittedApartFromExplicit(t *testing.T) {
	got, err := ReadFile(configFile(t, "[runtime]\nbackend = \"herdr-tui\"\n[runtime.core]\nexperimental = false\nconnect_timeout = \"5s\"\n[runtime.tui_view]\nenabled = false\n"))
	if err != nil {
		t.Fatal(err)
	}
	rt := got.Config.GetRuntime()
	if rt.GetBackend() != "herdr-tui" || rt.Backend == nil {
		t.Fatalf("backend: %v", rt)
	}
	if rt.GetCore().Experimental == nil || rt.GetCore().GetExperimental() || rt.GetCore().Binary != nil || rt.GetCore().GetConnectTimeout() != "5s" {
		t.Fatalf("core: %v", rt.GetCore())
	}
	if rt.GetTuiView().Enabled == nil || rt.GetTuiView().Socket != nil || rt.GetApp() != nil {
		t.Fatalf("view and app: %v", rt)
	}
}

// Gate "Upgrade": a new file names its backend (the Herdr TUI while herdr-core
// ships as an experiment), an existing file without the key has no backend
// after loading (so the installation default decides), and update never adds
// one.
func TestNewFileNamesItsBackendAndUpdateAddsNoBackend(t *testing.T) {
	for _, format := range fixtureFormats {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "herdsman.config."+format)
			if err := InitFile(path, format); err != nil {
				t.Fatal(err)
			}
			got, err := ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Config.GetRuntime().GetBackend() != NewFileBackend || NewFileBackend != "herdr-tui" {
				t.Fatalf("a new file names %q", got.Config.GetRuntime().GetBackend())
			}
		})
	}

	const existing = "# an existing file\n[hooks]\npre-merge = \"a\"\n"
	path := configFile(t, existing)
	opts := isolated(t)
	opts.ConfigPath = path
	loaded, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.GetRuntime().GetBackend() != "" {
		t.Fatalf("an existing file gained a backend: %v", loaded.Config.GetRuntime())
	}
	if _, ok := loaded.Sources["runtime.backend"]; ok {
		t.Fatalf("runtime.backend has a source: %v", loaded.Sources)
	}
	if err := UpdateFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "runtime") || strings.Contains(string(data), "backend") {
		t.Fatalf("update added a backend:\n%s", data)
	}
	if Defaults().GetRuntime() != nil {
		t.Fatal("the defaults name a backend, so every existing file would get one")
	}
}
