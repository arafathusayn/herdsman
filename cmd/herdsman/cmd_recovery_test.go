package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type recordedCall struct {
	method  string
	request map[string]any
}

func fakeRecovery(t *testing.T, reply string, fail error) *[]recordedCall {
	t.Helper()
	calls := &[]recordedCall{}
	saved := recoveryCall
	recoveryCall = func(_ context.Context, method string, request []byte) ([]byte, error) {
		var body map[string]any
		if err := json.Unmarshal(request, &body); err != nil {
			t.Fatal(err)
		}
		*calls = append(*calls, recordedCall{method, body})
		return []byte(reply), fail
	}
	t.Cleanup(func() { recoveryCall = saved })
	return calls
}

func TestUndoVerb(t *testing.T) {
	calls := fakeRecovery(t, `{"target":"/x/tree","original":true}`, nil)
	out, errOut, code := call(t, "undo", "OP1", "--to", "/x/tree")
	if code != 0 || !strings.Contains(out, "/x/tree") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	got := (*calls)[0]
	if got.method != "Undo" || got.request["snapshot"] != "OP1" || got.request["target"] != "/x/tree" {
		t.Fatalf("%+v", got)
	}
	out, _, code = call(t, "undo", "OP1", "--json")
	if code != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("%d %q", code, out)
	}
}

func TestExportVerbMakesTheDestinationAbsolute(t *testing.T) {
	calls := fakeRecovery(t, `{"id":"OP2","state":"done"}`, nil)
	dir := t.TempDir()
	t.Chdir(dir)
	out, errOut, code := call(t, "export", "backup")
	if code != 0 || !strings.Contains(out, "OP2") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	dest, _ := (*calls)[0].request["dest"].(string)
	if !filepath.IsAbs(dest) || filepath.Base(dest) != "backup" {
		t.Fatalf("dest = %q", dest)
	}
}

func TestImportVerb(t *testing.T) {
	calls := fakeRecovery(t, `{"state_dir":"/x/state"}`, nil)
	t.Chdir(t.TempDir())
	out, errOut, code := call(t, "import", "backup", "--state-dir", "/x/state")
	if code != 0 || !strings.Contains(out, "/x/state") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	got := (*calls)[0]
	source, _ := got.request["source"].(string)
	if got.method != "Import" || !filepath.IsAbs(source) || got.request["state_dir"] != "/x/state" {
		t.Fatalf("%+v", got)
	}
}

func TestRecoveryVerbsReportUsageAndFailure(t *testing.T) {
	fakeRecovery(t, ``, errors.New("the cap is full"))
	for _, args := range [][]string{{"undo"}, {"export"}, {"import"}, {"undo", "a", "b"}, {"export", "--nope", "x"}} {
		if _, _, code := call(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	for _, args := range [][]string{{"undo", "a"}, {"export", "/x"}, {"import", "/x"}} {
		_, errOut, code := call(t, args...)
		if code != 1 || !strings.Contains(errOut, "the cap is full") {
			t.Errorf("%v: exit %d, %q", args, code, errOut)
		}
	}
}
