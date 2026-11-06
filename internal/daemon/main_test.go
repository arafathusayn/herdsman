package daemon

import (
	"os"
	"syscall"
	"testing"
)

// TestMain runs the package's tests with plain fsync(2) behind the fsync
// hook. On darwin (*os.File).Sync is F_FULLFSYNC, which flushes the drive
// cache and costs milliseconds per call; the tests make tens of thousands
// of such calls. No test can tell the two apart: only a power cut can, and
// a test's crash or kill leaves the kernel's page cache intact. The call
// still reaches the kernel with the real descriptor, so an error it returns
// still fails the write. On Linux (*os.File).Sync is already fsync(2).
// Tests that make fsync fail or count its calls replace the hook as before.
func TestMain(m *testing.M) {
	fsync = plainFsync
	os.Exit(m.Run())
}

// plainFsync is fsync(2) on f's descriptor, without Fd's side effect of
// putting the descriptor in blocking mode.
func plainFsync(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = syscall.Fsync(int(fd)) }); err != nil {
		return err
	}
	if serr != nil {
		return &os.PathError{Op: "sync", Path: f.Name(), Err: serr}
	}
	return nil
}

// TestPlainFsync: the test hook syncs a real file and folder and reports
// the kernel's error, so a failed sync still fails a durable write.
func TestPlainFsync(t *testing.T) {
	dir := t.TempDir()
	if err := writeDurable(dir+"/f", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dir + "/f")
	if err != nil {
		t.Fatal(err)
	}
	if err := plainFsync(f); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := plainFsync(f); err == nil {
		t.Fatal("fsync of a closed file succeeded")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := plainFsync(r); err == nil {
		t.Fatal("fsync of a pipe succeeded")
	}
}
