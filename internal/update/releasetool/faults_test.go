package main

import (
	"crypto/ed25519"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/update"
)

func swap[T any](t *testing.T, target *T, value T) {
	t.Helper()
	saved := *target
	*target = value
	t.Cleanup(func() { *target = saved })
}

func signArgs(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "c")
	if err := os.WriteFile(in, []byte("sums"), 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{"sign", "--in", in, "--out", filepath.Join(dir, "c.sig")}
}

func TestSignRefusesWhenTheTrustedListCannotBeRead(t *testing.T) {
	seed, _ := newKey(t)
	planted := errors.New("planted keys")
	swap(t, &trustedKeys, func() ([]update.Key, error) { return nil, planted })
	msg, code := call(t, map[string]string{keyEnv: seed}, signArgs(t)...)
	if code != 1 || !strings.Contains(msg, "planted keys") {
		t.Fatalf("code %d: %s", code, msg)
	}
}

func TestSignRefusesAKeyTheBinariesDoNotTrust(t *testing.T) {
	seed, _ := newKey(t)
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	swap(t, &trustedKeys, func() ([]update.Key, error) { return []update.Key{{Pub: other}}, nil })
	msg, code := call(t, map[string]string{keyEnv: seed}, signArgs(t)...)
	if code != 1 || !strings.Contains(msg, "do not trust") {
		t.Fatalf("code %d: %s", code, msg)
	}
}

func TestKeygenReportsFailures(t *testing.T) {
	args := func(dir string) []string {
		return []string{"keygen", "--seed-out", filepath.Join(dir, "s"), "--public-out", filepath.Join(dir, "p")}
	}
	t.Run("no randomness", func(t *testing.T) {
		planted := errors.New("planted random")
		swap(t, &generateKey, func(io.Reader) (ed25519.PublicKey, ed25519.PrivateKey, error) { return nil, nil, planted })
		if msg, code := call(t, nil, args(t.TempDir())...); code != 1 || !strings.Contains(msg, "planted random") {
			t.Fatalf("code %d: %s", code, msg)
		}
	})
	t.Run("the seed cannot be written", func(t *testing.T) {
		swap(t, &writeSeed, func(*os.File, string) (int, error) { return 0, errors.New("planted write") })
		if msg, code := call(t, nil, args(t.TempDir())...); code != 1 || !strings.Contains(msg, "planted write") {
			t.Fatalf("code %d: %s", code, msg)
		}
	})
}

func TestSBOMStopsOnABrokenManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "vendor", "a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VENDOR.md"), []byte("nothing useful\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "sbom.json")
	msg, code := call(t, nil, "sbom", "--root", root, "--version", "1.0.0", "--created", "2026-01-02T03:04:05Z", "--zig-version", "0.17.0", "--out", out)
	if code != 1 || !strings.Contains(msg, "no Module line") {
		t.Fatalf("code %d: %s", code, msg)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("an SBOM was written: %v", err)
	}
}

func TestMainExitsWithRunsCode(t *testing.T) {
	var got []int
	swap(t, &exit, func(code int) { got = append(got, code) })
	swap(t, &os.Args, []string{"releasetool"})
	main()
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("exit calls %v", got)
	}
}
