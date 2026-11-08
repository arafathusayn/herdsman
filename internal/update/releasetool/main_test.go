package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/update"
)

func call(t *testing.T, env map[string]string, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := run(args, func(k string) string { return env[k] }, &out)
	return out.String(), code
}

// newKey makes a throwaway key and returns its seed and public line.
func newKey(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	seed, pub := filepath.Join(dir, "seed"), filepath.Join(dir, "pub")
	if out, code := call(t, nil, "keygen", "--seed-out", seed, "--public-out", pub); code != 0 {
		t.Fatalf("keygen: %s", out)
	}
	st, err := os.Stat(seed)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("seed file %v, err %v", st, err)
	}
	s, _ := os.ReadFile(seed)
	p, _ := os.ReadFile(pub)
	return strings.TrimSpace(string(s)), strings.TrimSpace(string(p))
}

func TestSignThenVerify(t *testing.T) {
	seed, pub := newKey(t)
	dir := t.TempDir()
	in, sig, keys := filepath.Join(dir, "c"), filepath.Join(dir, "c.sig"), filepath.Join(dir, "keys")
	os.WriteFile(in, []byte("sums"), 0o644)
	os.WriteFile(keys, []byte(pub+"\n"), 0o644)
	env := map[string]string{keyEnv: seed, allowEnv: "1"}

	if out, code := call(t, env, "sign", "--in", in, "--out", sig); code != 0 {
		t.Fatalf("sign: %s", out)
	}
	if out, code := call(t, nil, "verify", "--in", in, "--sig", sig, "--keys", keys); code != 0 {
		t.Fatalf("verify: %s", out)
	}
	os.WriteFile(in, []byte("changed"), 0o644)
	if _, code := call(t, nil, "verify", "--in", in, "--sig", sig, "--keys", keys); code != 1 {
		t.Fatalf("a changed file verified: code %d", code)
	}
}

func TestSignRefuses(t *testing.T) {
	seed, _ := newKey(t)
	dir := t.TempDir()
	in, sig := filepath.Join(dir, "c"), filepath.Join(dir, "c.sig")
	os.WriteFile(in, []byte("sums"), 0o644)

	out, code := call(t, nil, "sign", "--in", in, "--out", sig)
	if code != 1 || !strings.Contains(out, keyEnv) {
		t.Fatalf("no key: code %d, %s", code, out)
	}
	if _, code := call(t, map[string]string{keyEnv: "not base64 !"}, "sign", "--in", in, "--out", sig); code != 1 {
		t.Fatalf("bad key: code %d", code)
	}
	// The key is throwaway, so the binary does not trust it: a release signed
	// with it could never be installed.
	out, code = call(t, map[string]string{keyEnv: seed}, "sign", "--in", in, "--out", sig)
	if code != 1 || !strings.Contains(out, "do not trust") {
		t.Fatalf("untrusted key: code %d, %s", code, out)
	}
	if _, err := os.Stat(sig); err == nil {
		t.Fatal("a refused run wrote a signature")
	}
	if strings.Contains(out, seed) {
		t.Fatal("the key was printed")
	}
	allowed := map[string]string{keyEnv: seed, allowEnv: "1"}
	if _, code := call(t, allowed, "sign", "--in", filepath.Join(dir, "gone"), "--out", sig); code != 1 {
		t.Fatalf("missing input: code %d", code)
	}
	if _, code := call(t, allowed, "sign", "--in", in, "--out", filepath.Join(dir, "no/dir/sig")); code != 1 {
		t.Fatalf("unwritable output: code %d", code)
	}
}

func TestVerifyErrors(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	os.WriteFile(f, []byte("x"), 0o644)
	gone := filepath.Join(dir, "gone")
	for name, args := range map[string][]string{
		"no input":     {"verify", "--in", gone, "--sig", f, "--keys", f},
		"no signature": {"verify", "--in", f, "--sig", gone, "--keys", f},
		"no keys":      {"verify", "--in", f, "--sig", f, "--keys", gone},
		"bad keys":     {"verify", "--in", f, "--sig", f, "--keys", f},
	} {
		if _, code := call(t, nil, args...); code != 1 {
			t.Errorf("%s: code %d", name, code)
		}
	}
}

func TestKeygenErrors(t *testing.T) {
	dir := t.TempDir()
	if _, code := call(t, nil, "keygen", "--seed-out", filepath.Join(dir, "no/dir"), "--public-out", filepath.Join(dir, "p")); code != 1 {
		t.Errorf("seed: code %d", code)
	}
	if _, code := call(t, nil, "keygen", "--seed-out", filepath.Join(dir, "s"), "--public-out", filepath.Join(dir, "no/dir")); code != 1 {
		t.Errorf("public: code %d", code)
	}
	existing := filepath.Join(dir, "existing")
	os.WriteFile(existing, []byte("keep"), 0o600)
	if _, code := call(t, nil, "keygen", "--seed-out", existing, "--public-out", filepath.Join(dir, "p2")); code != 1 {
		t.Error("keygen overwrote a seed")
	}
}

func TestSBOM(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "sbom.json")
	if msg, code := call(t, nil, "sbom", "--root", root, "--version", "1.0.0", "--created", "2026-01-02T03:04:05Z", "--zig-version", "0.17.0", "--out", out); code != 0 {
		t.Fatalf("sbom: %s", msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := update.Components(root, update.Gitlink)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if !bytes.Contains(data, []byte(`"`+c.Name+`"`)) {
			t.Errorf("%s is missing", c.Name)
		}
	}
	for _, name := range []string{"musl", "zig-compiler-rt", "go-stdlib"} {
		if !bytes.Contains(data, []byte(`"`+name+`"`)) {
			t.Errorf("%s is missing", name)
		}
	}
	if _, code := call(t, nil, "sbom", "--root", t.TempDir()+"/gone/.gitmodules/x", "--version", "1.0.0", "--created", "bad", "--zig-version", "0.17.0", "--out", out); code != 1 {
		t.Errorf("bad time: code %d", code)
	}
	if _, code := call(t, nil, "sbom", "--root", root, "--version", "1.0.0", "--created", "2026-01-02T03:04:05Z", "--zig-version", "0.17.0", "--out", filepath.Join(t.TempDir(), "no/dir/x")); code != 1 {
		t.Errorf("unwritable: code %d", code)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"sign", "--bad"}, {"verify", "--bad"}, {"keygen", "--bad"}, {"sbom", "--bad"}, {"sign"}, {"verify"}, {"keygen"}, {"sbom"}} {
		if _, code := call(t, nil, args...); code != 2 {
			t.Errorf("%v: code %d", args, code)
		}
	}
}
