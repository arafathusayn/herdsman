package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testPriv = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))

func testKeys(t *testing.T) []Key {
	t.Helper()
	keys, err := ParseKeys(PublicKeyLine(testPriv.Public().(ed25519.PublicKey)) + "\n")
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func tarball(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sumLine(data []byte, name string) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]) + "  " + name + "\n"
}

func TestSignatureLineNamesSchemeAndKey(t *testing.T) {
	line := Sign(testPriv, []byte("data"))
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != "ed25519" || !strings.HasPrefix(f[1], "sha256:") || len(f[1]) != len("sha256:")+64 {
		t.Fatalf("signature line %q does not name its scheme and key", line)
	}
	if !strings.HasPrefix(PublicKeyLine(testPriv.Public().(ed25519.PublicKey)), "ed25519:") {
		t.Fatal("public key line does not name its scheme")
	}
}

func TestVerifySignature(t *testing.T) {
	keys := testKeys(t)
	good := Sign(testPriv, []byte("checksums"))
	if err := VerifySignature(keys, []byte("checksums"), []byte(good)); err != nil {
		t.Fatalf("a good signature was refused: %v", err)
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	f := strings.Fields(good)
	cases := map[string]struct{ data, sig string }{
		"tampered data":  {"checksumz", good},
		"untrusted key":  {"checksums", Sign(other, []byte("checksums"))},
		"unknown scheme": {"checksums", "rsa " + f[1] + " " + f[2] + "\n"},
		"not base64":     {"checksums", f[0] + " " + f[1] + " !!!\n"},
		"short":          {"checksums", "ed25519 only-two\n"},
		"wrong length":   {"checksums", f[0] + " " + f[1] + " aGVsbG8=\n"},
		"empty":          {"checksums", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := VerifySignature(keys, []byte(c.data), []byte(c.sig)); !errors.Is(err, ErrBadSignature) {
				t.Fatalf("got %v, want ErrBadSignature", err)
			}
		})
	}
	if err := VerifySignature(nil, []byte("checksums"), []byte(good)); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("no keys: got %v", err)
	}
}

func TestSignatureDoesNotCoverBareData(t *testing.T) {
	// A signature made by the same key over the raw bytes must not pass: the
	// signed message carries a purpose string.
	raw := ed25519.Sign(testPriv, []byte("checksums"))
	f := strings.Fields(Sign(testPriv, []byte("checksums")))
	line := fmt.Sprintf("ed25519 %s %s\n", f[1], encodeSig(raw))
	if err := VerifySignature(testKeys(t), []byte("checksums"), []byte(line)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("got %v", err)
	}
}

func TestParseKeys(t *testing.T) {
	keys, err := ParseKeys("# rotation\n\n" + PublicKeyLine(testPriv.Public().(ed25519.PublicKey)) + "\n")
	if err != nil || len(keys) != 1 || !strings.HasPrefix(keys[0].ID, "sha256:") {
		t.Fatalf("keys %v, err %v", keys, err)
	}
	for _, bad := range []string{"rsa:AAAA", "ed25519:!!!", "ed25519:aGVsbG8=", "noscheme"} {
		if _, err := ParseKeys(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestTrustedKeysFileParses(t *testing.T) {
	if _, err := TrustedKeys(); err != nil {
		t.Fatal(err)
	}
}

func TestParseChecksums(t *testing.T) {
	sum := strings.Repeat("a", 64)
	got, err := ParseChecksums([]byte(sum + "  a.tar.gz\n\n"))
	if err != nil || got["a.tar.gz"] != sum {
		t.Fatalf("got %v, err %v", got, err)
	}
	for name, bad := range map[string]string{
		"one field":  "abc\n",
		"short hash": "abc  a.tar.gz\n",
		"not hex":    strings.Repeat("z", 64) + "  a.tar.gz\n",
		"duplicate":  sum + "  a\n" + sum + "  a\n",
	} {
		if _, err := ParseChecksums([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestVerifyArchive(t *testing.T) {
	data := []byte("archive")
	sums, _ := ParseChecksums([]byte(sumLine(data, "a.tar.gz")))
	if err := VerifyArchive(sums, "a.tar.gz", data); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchive(sums, "a.tar.gz", []byte("other")); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("got %v", err)
	}
	if err := VerifyArchive(sums, "b.tar.gz", data); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("missing entry: got %v", err)
	}
}

func TestExtractBinary(t *testing.T) {
	got, err := ExtractBinary(tarball(t, map[string]string{"LICENSE": "l", "herdsman": "bin", "docs/man/herdsman": "not this"}), 1024)
	if err != nil || string(got) != "bin" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if _, err := ExtractBinary(tarball(t, map[string]string{"LICENSE": "l"}), 1024); err == nil {
		t.Error("an archive without the binary was accepted")
	}
	if _, err := ExtractBinary(tarball(t, map[string]string{"herdsman": "too long"}), 3); err == nil {
		t.Error("an oversized binary was accepted")
	}
	if _, err := ExtractBinary([]byte("not gzip"), 1024); err == nil {
		t.Error("garbage was accepted")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte("not a tar stream, long enough to be read as a broken header"))
	gz.Close()
	if _, err := ExtractBinary(buf.Bytes(), 1024); err == nil {
		t.Error("a broken tar stream was accepted")
	}
	var sym bytes.Buffer
	gz = gzip.NewWriter(&sym)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "herdsman", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"})
	tw.Close()
	gz.Close()
	if _, err := ExtractBinary(sym.Bytes(), 1024); err == nil {
		t.Error("a symlink was taken for the binary")
	}
}

func okSmoke(_ context.Context, _ string) (string, error) { return "herdsman 1.2.0 (abc)", nil }

func exeFile(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "herdsman")
	if err := os.WriteFile(exe, []byte("old"), 0o750); err != nil {
		t.Fatal(err)
	}
	return exe
}

func onlyEntry(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("leftovers beside the binary: %v", ents)
	}
}

func TestReplaceSwapsAndKeepsMode(t *testing.T) {
	exe := exeFile(t)
	if err := Replace(context.Background(), exe, []byte("new"), "1.2.0", okSmoke); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	st, _ := os.Stat(exe)
	if string(got) != "new" || st.Mode().Perm() != 0o750 {
		t.Fatalf("content %q, mode %v", got, st.Mode())
	}
	onlyEntry(t, filepath.Dir(exe))
}

func TestReplaceFollowsLinkToTheRealBinary(t *testing.T) {
	exe := exeFile(t)
	link := filepath.Join(t.TempDir(), "herdsman")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	if err := Replace(context.Background(), link, []byte("new"), "1.2.0", okSmoke); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new" {
		t.Fatalf("target holds %q", got)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a file")
	}
}

func TestReplaceLeavesOldBinaryOnFailedSmoke(t *testing.T) {
	for name, smoke := range map[string]SmokeFunc{
		"does not run":  func(context.Context, string) (string, error) { return "", errors.New("killed") },
		"wrong version": func(context.Context, string) (string, error) { return "herdsman 9.9.9 (x)", nil },
		"prefix only":   func(context.Context, string) (string, error) { return "herdsman 1.2.01 (x)", nil },
	} {
		t.Run(name, func(t *testing.T) {
			exe := exeFile(t)
			if err := Replace(context.Background(), exe, []byte("new"), "1.2.0", smoke); err == nil {
				t.Fatal("a binary that failed its check replaced the old one")
			}
			if got, _ := os.ReadFile(exe); string(got) != "old" {
				t.Fatalf("binary holds %q", got)
			}
			onlyEntry(t, filepath.Dir(exe))
		})
	}
}

func TestReplaceKeepsTheOldNameOnFailedRename(t *testing.T) {
	exe := exeFile(t)
	// During the check the target becomes a folder with content: rename fails.
	smoke := func(context.Context, string) (string, error) {
		if err := os.Remove(exe); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(exe, "inner"), 0o755); err != nil {
			t.Fatal(err)
		}
		return "herdsman 1.2.0 (x)", nil
	}
	if err := Replace(context.Background(), exe, []byte("new"), "1.2.0", smoke); err == nil {
		t.Fatal("a failed rename was reported as success")
	}
	onlyEntry(t, filepath.Dir(exe))
}

func TestSyncDirIgnoresAMissingFolder(t *testing.T) {
	if err := syncDir(filepath.Join(t.TempDir(), "gone")); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceRefusesPackageManagerInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Caskroom", "herdsman", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "herdsman")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(context.Background(), exe, []byte("new"), "1.2.0", okSmoke); !errors.Is(err, ErrManaged) {
		t.Fatalf("got %v", err)
	}
}

func TestReplaceErrors(t *testing.T) {
	if err := Replace(context.Background(), filepath.Join(t.TempDir(), "gone"), []byte("x"), "1.2.0", okSmoke); err == nil {
		t.Error("a missing binary was accepted")
	}
	exe := exeFile(t)
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := Replace(context.Background(), exe, []byte("x"), "1.2.0", okSmoke); err == nil {
		t.Error("an unwritable folder was accepted")
	}
}

func TestSmokeRunsTheBinaryVersion(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"herdsman 1.2.0 ($1)\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := Smoke(context.Background(), script)
	if err != nil || !strings.Contains(out, "herdsman 1.2.0 (version)") {
		t.Fatalf("out %q, err %v", out, err)
	}
	if _, err := Smoke(context.Background(), filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("a missing binary ran")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.2.0", 0},
		{"1.2.0", "1.10.0", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.2.0-rc.1", "1.2.0", -1},
		{"1.2.0", "1.2.0-rc.1", 1},
		{"1.2.0-rc.1", "1.2.0-rc.2", -1},
		{"1.2.0-rc.10", "1.2.0-rc.2", 1},
		{"1.2.0-a", "1.2.0-1", 1},
		{"1.2.0-1", "1.2.0-a", -1},
		{"1.2.0-a", "1.2.0-b", -1},
		{"1.2.0-rc", "1.2.0-rc.1", -1},
	} {
		got, err := CompareVersions(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("%s vs %s: got %d, err %v, want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range [][2]string{{"x", "1.0.0"}, {"1.0.0", "1.0"}, {"1.a.0", "1.0.0"}, {"1.0.0-", "1.0.0"}, {"1.0.0", "1.0.0-a/b"}} {
		if _, err := CompareVersions(bad[0], bad[1]); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
}

// release is a fake release server; its fields are what it serves.
type release struct {
	tag       string
	checksums []byte
	sig       []byte
	archive   []byte
	archives  int
	failAll   bool
}

func (r *release) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if r.failAll {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		v := strings.TrimPrefix(r.tag, "v")
		switch q.URL.Path {
		case "/api/repos/" + Repository + "/releases/latest":
			json.NewEncoder(w).Encode(map[string]string{"tag_name": r.tag})
		case "/dl/" + Repository + "/releases/download/" + r.tag + "/checksums.txt":
			w.Write(r.checksums)
		case "/dl/" + Repository + "/releases/download/" + r.tag + "/checksums.txt.sig":
			w.Write(r.sig)
		case "/dl/" + Repository + "/releases/download/" + r.tag + "/herdsman_" + v + "_testos_testarch.tar.gz":
			r.archives++
			w.Write(r.archive)
		default:
			http.NotFound(w, q)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func goodRelease(t *testing.T, tag string) *release {
	t.Helper()
	v := strings.TrimPrefix(tag, "v")
	archive := tarball(t, map[string]string{"herdsman": "new binary", "LICENSE": "l"})
	sums := []byte(sumLine(archive, "herdsman_"+v+"_testos_testarch.tar.gz"))
	return &release{tag: tag, checksums: sums, sig: []byte(Sign(testPriv, sums)), archive: archive}
}

func options(t *testing.T, srv *httptest.Server, out io.Writer) Options {
	t.Helper()
	return Options{
		Client:  srv.Client(),
		API:     srv.URL + "/api",
		Base:    srv.URL + "/dl",
		Keys:    testKeys(t),
		Current: "1.0.0",
		Exe:     exeFile(t),
		GOOS:    "testos",
		GOARCH:  "testarch",
		Smoke:   func(context.Context, string) (string, error) { return "herdsman 1.2.0 (x)", nil },
		Stdout:  out,
	}
}

func binaryIs(t *testing.T, o Options, want string) {
	t.Helper()
	if got, _ := os.ReadFile(o.Exe); string(got) != want {
		t.Fatalf("binary holds %q, want %q", got, want)
	}
}

func TestRunInstallsAVerifiedRelease(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	var out bytes.Buffer
	o := options(t, r.server(t), &out)
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	binaryIs(t, o, "new binary")
	if !strings.Contains(out.String(), "1.0.0") || !strings.Contains(out.String(), "1.2.0") {
		t.Fatalf("output %q", out.String())
	}
}

func TestRunRefusesBadSignatureBeforeDownloadingTheArchive(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	r.sig = []byte(Sign(other, r.checksums))
	o := options(t, r.server(t), io.Discard)
	if err := Run(context.Background(), o); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("got %v", err)
	}
	if r.archives != 0 {
		t.Fatal("the archive was fetched before the signature passed")
	}
	binaryIs(t, o, "old")
}

func TestRunRefusesTamperedChecksums(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	r.checksums = append(r.checksums, sumLine([]byte("x"), "extra")...)
	o := options(t, r.server(t), io.Discard)
	if err := Run(context.Background(), o); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("got %v", err)
	}
	binaryIs(t, o, "old")
}

func TestRunRefusesArchiveThatDoesNotMatch(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	r.archive = tarball(t, map[string]string{"herdsman": "evil"})
	o := options(t, r.server(t), io.Discard)
	if err := Run(context.Background(), o); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("got %v", err)
	}
	binaryIs(t, o, "old")
}

func TestRunRefusesOlderSignedReleaseServedAsLatest(t *testing.T) {
	// The checksums are signed for 0.9.0; the server claims the newer tag.
	r := goodRelease(t, "v0.9.0")
	r.tag = "v1.2.0"
	o := options(t, r.server(t), io.Discard)
	if err := Run(context.Background(), o); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("got %v", err)
	}
	binaryIs(t, o, "old")
}

func TestRunDowngrade(t *testing.T) {
	r := goodRelease(t, "v0.9.0")
	o := options(t, r.server(t), io.Discard)
	o.Target = "0.9.0"
	o.Smoke = func(context.Context, string) (string, error) { return "herdsman 0.9.0 (x)", nil }
	if err := Run(context.Background(), o); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("got %v", err)
	}
	binaryIs(t, o, "old")
	o.AllowDowngrade = true
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	binaryIs(t, o, "new binary")
}

func TestRunAlreadyCurrent(t *testing.T) {
	r := goodRelease(t, "v1.0.0")
	var out bytes.Buffer
	o := options(t, r.server(t), &out)
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	binaryIs(t, o, "old")
	if !strings.Contains(out.String(), "up to date") || r.archives != 0 {
		t.Fatalf("output %q, archives %d", out.String(), r.archives)
	}
}

func TestRunCheckChangesNothing(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	var out bytes.Buffer
	o := options(t, r.server(t), &out)
	o.Check = true
	o.Keys = nil // reading the latest tag needs no key
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	binaryIs(t, o, "old")
	if !strings.Contains(out.String(), "1.2.0") || r.archives != 0 {
		t.Fatalf("output %q, archives %d", out.String(), r.archives)
	}
}

func TestRunRefusesWithoutKeys(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	o := options(t, r.server(t), io.Discard)
	o.Keys = nil
	if err := Run(context.Background(), o); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("got %v", err)
	}
	binaryIs(t, o, "old")
}

func TestRunDevBuildCanUpdate(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	o := options(t, r.server(t), io.Discard)
	o.Current = "dev"
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	binaryIs(t, o, "new binary")
}

func TestRunNetworkAndInputFailures(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	srv := r.server(t)

	t.Run("server down", func(t *testing.T) {
		r.failAll = true
		defer func() { r.failAll = false }()
		if err := Run(context.Background(), options(t, srv, io.Discard)); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("bad tag from the server", func(t *testing.T) {
		bad := &release{tag: "../../evil"}
		if err := Run(context.Background(), options(t, bad.server(t), io.Discard)); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("bad target", func(t *testing.T) {
		o := options(t, srv, io.Discard)
		o.Target = "not-a-version"
		if err := Run(context.Background(), o); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("bad current", func(t *testing.T) {
		o := options(t, srv, io.Discard)
		o.Current = "banana"
		if err := Run(context.Background(), o); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("missing signature", func(t *testing.T) {
		o := options(t, srv, io.Discard)
		o.Target = "1.3.0"
		if err := Run(context.Background(), o); err == nil {
			t.Fatal("no error")
		}
		binaryIs(t, o, "old")
	})
	t.Run("oversized checksums", func(t *testing.T) {
		big := goodRelease(t, "v1.2.0")
		big.checksums = bytes.Repeat([]byte("a"), maxChecksums+1)
		if err := Run(context.Background(), options(t, big.server(t), io.Discard)); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("signed checksums that do not parse", func(t *testing.T) {
		odd := goodRelease(t, "v1.2.0")
		odd.checksums = []byte("garbage\n")
		odd.sig = []byte(Sign(testPriv, odd.checksums))
		if err := Run(context.Background(), options(t, odd.server(t), io.Discard)); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("archive without the binary", func(t *testing.T) {
		empty := goodRelease(t, "v1.2.0")
		empty.archive = tarball(t, map[string]string{"LICENSE": "l"})
		empty.checksums = []byte(sumLine(empty.archive, "herdsman_1.2.0_testos_testarch.tar.gz"))
		empty.sig = []byte(Sign(testPriv, empty.checksums))
		if err := Run(context.Background(), options(t, empty.server(t), io.Discard)); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("not json", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("{")) }))
		defer s.Close()
		if err := Run(context.Background(), options(t, s, io.Discard)); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		s := httptest.NewServer(http.NotFoundHandler())
		o := options(t, s, io.Discard)
		s.Close()
		if err := Run(context.Background(), o); err == nil {
			t.Fatal("no error")
		}
	})
}
