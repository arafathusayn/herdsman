package approvals

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentApprovalsAreAllKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	var group sync.WaitGroup
	errs := make([]error, 24)
	for i := range errs {
		group.Go(func() { errs[i] = Open(path).Approve("p", fmt.Sprintf("template %d", i)) })
	}
	group.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := Open(path).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list["p"]) != len(errs) {
		t.Fatalf("kept %d of %d: %v", len(list["p"]), len(errs), list["p"])
	}
}

func TestConcurrentClearIsNotUndoneByAnOlderSnapshot(t *testing.T) {
	for range 20 {
		path := filepath.Join(t.TempDir(), FileName)
		store := Open(path)
		if err := store.Approve("cleared", "old"); err != nil {
			t.Fatal(err)
		}
		var group sync.WaitGroup
		var approveErr, clearErr error
		group.Go(func() { approveErr = Open(path).Approve("kept", "new") })
		group.Go(func() { _, clearErr = Open(path).Clear("cleared") })
		group.Wait()
		if approveErr != nil || clearErr != nil {
			t.Fatal(approveErr, clearErr)
		}
		list, err := store.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(list["cleared"]) != 0 || len(list["kept"]) != 1 {
			t.Fatalf("list: %v", list)
		}
	}
}

const childStoreEnv = "HERDSMAN_TEST_APPROVALS_STORE"

// TestChildProcessApproves is the body of the processes the test below starts.
func TestChildProcessApproves(t *testing.T) {
	path := os.Getenv(childStoreEnv)
	if path == "" {
		t.Skip("runs only as a child of TestApprovalsFromSeveralProcessesAreAllKept")
	}
	for i := range 5 {
		if err := Open(path).Approve("p", fmt.Sprintf("%d-%d", os.Getpid(), i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestApprovalsFromSeveralProcessesAreAllKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	var group sync.WaitGroup
	outputs := make([][]byte, 4)
	errs := make([]error, len(outputs))
	for i := range outputs {
		group.Go(func() {
			child := exec.Command(os.Args[0], "-test.run=^TestChildProcessApproves$")
			child.Env = append(os.Environ(), childStoreEnv+"="+path)
			outputs[i], errs[i] = child.CombinedOutput()
		})
	}
	group.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%v\n%s", err, outputs[i])
		}
	}
	list, err := Open(path).List()
	if err != nil {
		t.Fatal(err)
	}
	if want := len(outputs) * 5; len(list["p"]) != want {
		t.Fatalf("kept %d of %d", len(list["p"]), want)
	}
}

func TestLockFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := Open(filepath.Join(blocker, FileName))
	if err := store.Approve("p", "t"); err == nil {
		t.Fatal("approve under a file succeeded")
	}
	if _, err := store.Clear(""); err == nil {
		t.Fatal("clear under a file succeeded")
	}
}
