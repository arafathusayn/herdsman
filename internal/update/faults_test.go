package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func swap[T any](t *testing.T, target *T, value T) {
	t.Helper()
	saved := *target
	*target = value
	t.Cleanup(func() { *target = saved })
}

// failingTemp is a real candidate file with one operation made to fail.
type failingTemp struct {
	*os.File
	op  string
	err error
}

func (f *failingTemp) Write(b []byte) (int, error) {
	if f.op == "write" {
		return 0, f.err
	}
	return f.File.Write(b)
}

func (f *failingTemp) Chmod(m os.FileMode) error {
	if f.op == "chmod" {
		return f.err
	}
	return f.File.Chmod(m)
}

func (f *failingTemp) Sync() error {
	if f.op == "sync" {
		return f.err
	}
	return f.File.Sync()
}

func (f *failingTemp) Close() error {
	err := f.File.Close()
	if f.op == "close" {
		return f.err
	}
	return err
}

func TestReplaceKeepsTheOldBinaryWhenTheCandidateCannotBeWritten(t *testing.T) {
	for _, op := range []string{"write", "chmod", "sync", "close"} {
		t.Run(op, func(t *testing.T) {
			exe := exeFile(t)
			planted := errors.New("planted " + op)
			swap(t, &createTemp, func(dir, pattern string) (candidateFile, error) {
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingTemp{File: f, op: op, err: planted}, nil
			})
			smoked := false
			smoke := func(context.Context, string) (string, error) { smoked = true; return "herdsman 1.2.0", nil }
			err := Replace(context.Background(), exe, []byte("new"), "1.2.0", smoke)
			if !errors.Is(err, planted) || !strings.Contains(err.Error(), "write the new binary") {
				t.Fatalf("err = %v", err)
			}
			if smoked {
				t.Fatal("a candidate that was not fully written was run")
			}
			if got, _ := os.ReadFile(exe); string(got) != "old" {
				t.Fatalf("binary = %q", got)
			}
			onlyEntry(t, filepath.Dir(exe))
		})
	}
}

func TestReplaceReportsABinaryThatVanishes(t *testing.T) {
	exe := exeFile(t)
	planted := errors.New("planted stat")
	swap(t, &statFile, func(string) (os.FileInfo, error) { return nil, planted })
	err := Replace(context.Background(), exe, []byte("new"), "1.2.0", okSmoke)
	if !errors.Is(err, planted) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("binary = %q", got)
	}
}

type fakeDir struct{ err error }

func (d fakeDir) Sync() error  { return d.err }
func (d fakeDir) Close() error { return nil }

func TestSyncDirCountsOnlyARealIOError(t *testing.T) {
	planted := errors.New("planted io")
	swap(t, &openDir, func(string) (dirSyncer, error) { return fakeDir{planted}, nil })
	if err := syncDir("x"); !errors.Is(err, planted) {
		t.Fatalf("a real I/O error was dropped: %v", err)
	}
	swap(t, &openDir, func(string) (dirSyncer, error) { return fakeDir{os.ErrInvalid}, nil })
	if err := syncDir("x"); err != nil {
		t.Fatalf("a folder that cannot sync is not a failure: %v", err)
	}
}

func TestReplaceReportsADirectorySyncFailureAfterTheSwap(t *testing.T) {
	exe := exeFile(t)
	planted := errors.New("planted io")
	swap(t, &openDir, func(string) (dirSyncer, error) { return fakeDir{planted}, nil })
	if err := Replace(context.Background(), exe, []byte("new"), "1.2.0", okSmoke); !errors.Is(err, planted) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new" {
		t.Fatalf("binary = %q", got)
	}
}

func TestGitlinkOutsideARepository(t *testing.T) {
	if _, err := Gitlink(t.TempDir(), "x"); err == nil || !strings.Contains(err.Error(), "read submodule x") {
		t.Fatalf("err = %v", err)
	}
}

func TestComponentsStopsOnAnUnwalkableRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Components(root, noGit); err == nil {
		t.Fatal("no error")
	}
}

func TestComponentsStopsWhenAPathCannotBeRelative(t *testing.T) {
	root := writeTree(t, map[string]string{"vendor/a/VENDOR.md": manifestFor("a")})
	planted := errors.New("planted rel")
	swap(t, &relPath, func(string, string) (string, error) { return "", planted })
	if _, err := Components(root, noGit); !errors.Is(err, planted) {
		t.Fatalf("err = %v", err)
	}
}

func TestComponentsRefusesASubmoduleThatConflictsWithAManifest(t *testing.T) {
	root := writeTree(t, map[string]string{
		"vendor/a/VENDOR.md":   manifestFor("dup"),
		".gitmodules":          "[submodule \"x\"]\n\tpath = web/dup\n\turl = https://example.com/dup\n",
		"web/dup/package.json": `{"license":"MIT"}`,
	})
	gitlink := func(string, string) (string, error) { return strings.Repeat("a", 40), nil }
	if _, err := Components(root, gitlink); err == nil || !strings.Contains(err.Error(), "recorded twice") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadersReportFilesThatWillNotOpen(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := readManifest(missing); err == nil {
		t.Error("readManifest: no error")
	}
	if _, err := readSQLite(missing); err == nil {
		t.Error("readSQLite: no error")
	}
}

func TestReadSubmodulesReportsAnUnreadableList(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".gitmodules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readSubmodules(root, noGit); err == nil {
		t.Fatal("no error")
	}
}

func TestSubmoduleLicenseOnABadPattern(t *testing.T) {
	if _, err := submoduleLicense("bad["); err == nil {
		t.Fatal("no error")
	}
}

func TestSubmoduleLicenseFallsBackToAPackageWhenTheRootHasNoManifest(t *testing.T) {
	dir := writeTree(t, map[string]string{"packages/core/package.json": `{"license":"Apache-2.0"}`})
	got, err := submoduleLicense(dir)
	if err != nil || got != "Apache-2.0" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSPDXReportsAnEncodingFailure(t *testing.T) {
	planted := errors.New("planted marshal")
	swap(t, &marshalIndent, func(any, string, string) ([]byte, error) { return nil, planted })
	if _, err := SPDX("herdsman", "1.0.0", "2026-01-01T00:00:00Z", nil); !errors.Is(err, planted) {
		t.Fatalf("err = %v", err)
	}
}

// roundTrip answers each URL suffix through the function given.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{ err error }

func (b brokenBody) Read([]byte) (int, error) { return 0, b.err }
func (b brokenBody) Close() error             { return nil }

func TestInstallStopsAtEachFailedStepAndKeepsTheBinary(t *testing.T) {
	r := goodRelease(t, "v1.2.0")
	srv := r.server(t)
	blocked := func(t *testing.T, suffix string) Options {
		t.Helper()
		o := options(t, srv, io.Discard)
		o.Target = "1.2.0"
		transport := srv.Client().Transport
		o.Client = &http.Client{Transport: roundTrip(func(q *http.Request) (*http.Response, error) {
			if strings.HasSuffix(q.URL.Path, suffix) {
				return nil, errors.New("planted network")
			}
			return transport.RoundTrip(q)
		})}
		return o
	}
	for _, suffix := range []string{"checksums.txt.sig", ".tar.gz"} {
		t.Run(suffix, func(t *testing.T) {
			o := blocked(t, suffix)
			if err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "planted network") {
				t.Fatalf("err = %v", err)
			}
			binaryIs(t, o, "old")
		})
	}
	t.Run("the swap fails", func(t *testing.T) {
		o := options(t, srv, io.Discard)
		o.Target = "1.2.0"
		o.Smoke = func(context.Context, string) (string, error) { return "", errors.New("planted smoke") }
		if err := Run(context.Background(), o); err == nil || !strings.Contains(err.Error(), "planted smoke") {
			t.Fatalf("err = %v", err)
		}
		binaryIs(t, o, "old")
	})
}

func TestFetchReportsABadURLAndABodyThatBreaks(t *testing.T) {
	o := Options{Client: &http.Client{}}
	if _, err := fetch(context.Background(), o, "http://bad host/", 10); err == nil {
		t.Error("a bad URL was accepted")
	}
	planted := errors.New("planted read")
	o.Client = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: brokenBody{planted}}, nil
	})}
	if _, err := fetch(context.Background(), o, "http://example.test/x", 10); !errors.Is(err, planted) {
		t.Errorf("err = %v", err)
	}
}

func TestExtractBinaryReportsATruncatedFile(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: binaryName, Typeflag: tar.TypeReg, Mode: 0o755, Size: 4096}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatal(err)
	}
	var zipped bytes.Buffer
	gz := gzip.NewWriter(&zipped)
	// The header and part of the file, as a cut-off download would leave it.
	if _, err := gz.Write(raw.Bytes()[:1024]); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractBinary(zipped.Bytes(), 1<<20); err == nil || !strings.Contains(err.Error(), "read archive") {
		t.Fatalf("err = %v", err)
	}
}
