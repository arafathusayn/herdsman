package sys

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// connectedPair returns both ends of a Unix socket. The socket is in a short
// directory of its own because sun_path is limited to about 100 bytes.
func connectedPair(t *testing.T) (client, server *net.UnixConn) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sys")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	client, err = net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err = l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

func TestPeerCredentialsNameThisProcess(t *testing.T) {
	_, server := connectedPair(t)
	got, err := PeerCredentials(server)
	if err != nil {
		t.Fatal(err)
	}
	want := PeerCred{UID: os.Geteuid(), GID: os.Getegid(), PID: os.Getpid()}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPeerCredentialsFailures(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	client, _ := connectedPair(t)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for name, conn := range map[string]syscall.Conn{"not a socket": file, "closed": client, "unset": &net.UnixConn{}} {
		t.Run(name, func(t *testing.T) {
			got, err := PeerCredentials(conn)
			if err == nil {
				t.Fatalf("no error, got %+v", got)
			}
			if got != (PeerCred{}) {
				t.Fatalf("a failed call returned %+v", got)
			}
		})
	}
}

func TestIdentifyIsStableForOneProcess(t *testing.T) {
	first, err := Identify(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first.PID != os.Getpid() {
		t.Fatalf("pid %d, want %d", first.PID, os.Getpid())
	}
	if first.Start.IsZero() || first.Start.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("implausible start time %v", first.Start)
	}
	second, err := Identify(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Same(second) {
		t.Fatalf("two readings differ: %v and %v", first, second)
	}
}

func TestIdentifyTellsTwoProcessesApart(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()
	mine, err := Identify(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := Identify(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Same(theirs) {
		t.Fatalf("%v and %v read as one process", mine, theirs)
	}
}

func TestIdentifyReportsAMissingProcess(t *testing.T) {
	// The last value wraps to this process's pid if the pid is ever truncated
	// to 32 bits on its way to the kernel.
	for _, pid := range []int{0, -1, 1 << 30, 1<<32 + os.Getpid()} {
		if got, err := Identify(pid); !errors.Is(err, ErrNoProcess) {
			t.Errorf("pid %d: got %v, error %v", pid, got, err)
		}
	}
}

func TestSameComparesPIDAndInstant(t *testing.T) {
	at := time.Unix(1000, 5)
	base := Process{PID: 7, Start: at}
	if !base.Same(Process{PID: 7, Start: at.In(time.FixedZone("x", 3600))}) {
		t.Error("the same instant in another zone read as different")
	}
	if base.Same(Process{PID: 8, Start: at}) {
		t.Error("another pid read as the same process")
	}
	if base.Same(Process{PID: 7, Start: at.Add(time.Nanosecond)}) {
		t.Error("another start time read as the same process")
	}
}

func TestMapGone(t *testing.T) {
	other := errors.New("other")
	if got := mapGone(fmt.Errorf("read: %w", syscall.ESRCH), syscall.ESRCH); !errors.Is(got, ErrNoProcess) {
		t.Errorf("listed error mapped to %v", got)
	}
	if got := mapGone(other, syscall.ESRCH); got != other {
		t.Errorf("unlisted error mapped to %v", got)
	}
}

// fakeProc writes a /proc tree holding one process.
func fakeProc(t *testing.T, stat, procStat string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "42"), 0o755); err != nil {
		t.Fatal(err)
	}
	if stat != "" {
		if err := os.WriteFile(filepath.Join(root, "42", "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if procStat != "" {
		if err := os.WriteFile(filepath.Join(root, "stat"), []byte(procStat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// statLine builds a stat line whose start time field holds ticks.
func statLine(comm, ticks string) string {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[19] = ticks
	return "42 (" + comm + ") " + strings.Join(fields, " ") + " 99 98\n"
}

func TestStartFromProcAddsTicksToBootTime(t *testing.T) {
	root := fakeProc(t, statLine("a) b (c", "12350"), "cpu 1 2\nbtime 1000\nprocesses 3\n")
	got, err := startFromProc(root, 42)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1123, int64(500*time.Millisecond))
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestStartFromProcFailures(t *testing.T) {
	good := statLine("x", "100")
	boot := "btime 1000\n"
	cases := []struct {
		name, stat, procStat string
		pid                  int
		gone                 bool
	}{
		{name: "process gone", stat: good, procStat: boot, pid: 43, gone: true},
		{name: "no closing parenthesis", stat: "42 x S 0\n", procStat: boot, pid: 42},
		{name: "short line", stat: "42 (x) S 0\n", procStat: boot, pid: 42},
		{name: "bad ticks", stat: statLine("x", "soon"), procStat: boot, pid: 42},
		{name: "no system stat", stat: good, pid: 42},
		{name: "no boot time", stat: good, procStat: "cpu 1\n", pid: 42},
		{name: "bad boot time", stat: good, procStat: "btime soon\n", pid: 42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := startFromProc(fakeProc(t, c.stat, c.procStat), c.pid)
			if err == nil {
				t.Fatalf("no error, got %v", got)
			}
			if gone := errors.Is(err, ErrNoProcess); gone != c.gone {
				t.Fatalf("no-process %v, want %v: %v", gone, c.gone, err)
			}
		})
	}
}

// writeSource writes a file with a known mode and an old modification time.
func writeSource(t *testing.T, dir string) (string, time.Time) {
	t.Helper()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o640); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1_500_000_000, 123_456_000)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return src, mtime
}

func TestCloneFileMakesAnIndependentCopy(t *testing.T) {
	dir := t.TempDir()
	src, mtime := writeSource(t, dir)
	dst := filepath.Join(dir, "dst")
	if err := CloneFile(src, dst); err != nil {
		if runtime.GOOS == "linux" && errors.Is(err, ErrCloneUnsupported) {
			// Most Linux test filesystems have no reflinks.
			if _, statErr := os.Lstat(dst); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("an unsupported clone left the destination: %v", statErr)
			}
			t.Skip("this filesystem has no reflinks")
		}
		t.Fatal(err)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(srcInfo, dstInfo) {
		t.Fatal("the destination is a hard link to the source")
	}
	if dstInfo.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", dstInfo.Mode().Perm())
	}
	if !dstInfo.ModTime().Equal(mtime) {
		t.Errorf("mtime %v, want %v", dstInfo.ModTime(), mtime)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "original" {
		t.Fatalf("the clone before any write: %q, %v", got, err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o600); err != nil {
		t.Fatal(err)
	}
	overwrite(t, dst, "CLONE!!!")
	if got, err := os.ReadFile(src); err != nil || string(got) != "original" {
		t.Fatalf("source after writing the clone: %q, %v", got, err)
	}
	overwrite(t, src, "SOURCE!!")
	if got, err := os.ReadFile(dst); err != nil || string(got) != "CLONE!!!" {
		t.Fatalf("clone after writing the source: %q, %v", got, err)
	}
}

func overwrite(t *testing.T, path, body string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte(body), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSetLinkTimeChangesOnlyTheLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	for name, to := range map[string]string{"live": target, "dangling": filepath.Join(dir, "gone")} {
		link := filepath.Join(dir, name)
		if err := os.Symlink(to, link); err != nil {
			t.Fatal(err)
		}
		mtime := time.Unix(1_500_000_000, 123_456_000)
		if err := SetLinkTime(link, mtime); err != nil {
			t.Fatal(err)
		}
		got, err := os.Lstat(link)
		if err != nil {
			t.Fatal(err)
		}
		if !got.ModTime().Equal(mtime) {
			t.Errorf("%s: mtime %v, want %v", name, got.ModTime(), mtime)
		}
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the link's target changed")
	}
	if err := SetLinkTime(filepath.Join(dir, "missing"), time.Now()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}

func TestMakeRawReportsAFailedSet(t *testing.T) {
	fd := openPTY(t)
	old := setTermios
	setTermios = func(int, uint, *unix.Termios) error { return unix.EIO }
	defer func() { setTermios = old }()
	if mode, err := MakeRaw(fd); !errors.Is(err, unix.EIO) || mode != nil {
		t.Fatalf("got %v, %v", mode, err)
	}
}

func TestLinuxPeerRejectsAbsentCredentials(t *testing.T) {
	if got, err := peerFromUcred(0, 65534, 65534); err == nil {
		t.Fatalf("an unconnected socket gave %+v", got)
	}
	got, err := peerFromUcred(42, 7, 8)
	if err != nil || got != (PeerCred{UID: 7, GID: 8, PID: 42}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestPeerCredentialsRefuseOtherSockets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	tcp, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	if runtime.GOOS == "linux" {
		return // The kernel answers these; TestLinuxPeerRejectsAbsentCredentials covers what it gives.
	}
	if got, err := PeerCredentials(tcp.(*net.TCPConn)); err == nil {
		t.Fatalf("a TCP socket gave %+v", got)
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "unconnected")
	defer file.Close()
	if got, err := PeerCredentials(file); err == nil {
		t.Fatalf("an unconnected socket gave %+v", got)
	}
}

// A real FICLONE, which ordinary Linux test filesystems cannot do.
func TestCloneFileOnBtrfs(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CI") == "" {
		t.Skip("mounts a loopback image with sudo; runs on Linux CI only")
	}
	root := t.TempDir()
	must := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	if _, err := exec.LookPath("mkfs.btrfs"); err != nil {
		must("sudo", "apt-get", "install", "-y", "btrfs-progs")
	}
	image, mount := filepath.Join(root, "btrfs.img"), filepath.Join(root, "mnt")
	if err := os.Mkdir(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	must("truncate", "-s", "256M", image)
	must("mkfs.btrfs", "-q", image)
	must("sudo", "mount", "-o", "loop", image, mount)
	t.Cleanup(func() { _ = exec.Command("sudo", "umount", mount).Run() })
	must("sudo", "chown", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), mount)
	src, _ := writeSource(t, mount)
	dst := filepath.Join(mount, "dst")
	if err := CloneFile(src, dst); err != nil {
		t.Fatalf("FICLONE on btrfs: %v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "original" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestCloneFileKeepsAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src, _ := writeSource(t, dir)
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(dst, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CloneFile(src, dst); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("error %v, want one that says the file exists", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "keep" {
		t.Fatalf("destination: %q, %v", got, err)
	}
}

func TestCloneFileRefusesWhatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	src, _ := writeSource(t, dir)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"directory": dir, "symlink": link} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(dir, "dst-"+name)
			if err := CloneFile(path, dst); !errors.Is(err, ErrNotRegular) {
				t.Fatalf("error %v", err)
			}
			if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("destination: %v", err)
			}
		})
	}
}

func TestCloneFileMissingPaths(t *testing.T) {
	dir := t.TempDir()
	src, _ := writeSource(t, dir)
	cases := map[string][2]string{
		"no source":      {filepath.Join(dir, "absent"), filepath.Join(dir, "dst")},
		"no destination": {src, filepath.Join(dir, "absent", "dst")},
	}
	for name, paths := range cases {
		t.Run(name, func(t *testing.T) {
			err := CloneFile(paths[0], paths[1])
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("error %v", err)
			}
			if errors.Is(err, ErrCloneUnsupported) {
				t.Fatalf("a missing path read as an unsupported clone: %v", err)
			}
		})
	}
}

func TestWrapCloneErrorNamesTheCallersFallback(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EXDEV, syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.ENOTTY, syscall.EINVAL, syscall.ENOSYS} {
		err := wrapCloneError("a", "b", errno)
		if !errors.Is(err, ErrCloneUnsupported) || !errors.Is(err, errno) {
			t.Errorf("%v wrapped to %v", errno, err)
		}
	}
	err := wrapCloneError("a", "b", syscall.EACCES)
	if errors.Is(err, ErrCloneUnsupported) || !errors.Is(err, syscall.EACCES) {
		t.Errorf("EACCES wrapped to %v", err)
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("the message does not name both paths: %v", err)
	}
}

// openPTY returns the replica side of a new pseudo-terminal, the side a program
// sees as its terminal.
func openPTY(t *testing.T) int {
	t.Helper()
	controller, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	replica, err := os.OpenFile(replicaPath(t, int(controller.Fd())), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replica.Close() })
	return int(replica.Fd())
}

func notATerminal(t *testing.T) int {
	t.Helper()
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return int(file.Fd())
}

func TestRawModeAndRestore(t *testing.T) {
	fd := openPTY(t)
	before, err := unix.IoctlGetTermios(fd, reqGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := MakeRaw(fd)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := unix.IoctlGetTermios(fd, reqGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Lflag&(unix.ECHO|unix.ICANON|unix.ISIG) != 0 {
		t.Errorf("local flags still set: %#x", raw.Lflag)
	}
	if raw.Iflag&(unix.ICRNL|unix.IXON) != 0 || raw.Oflag&unix.OPOST != 0 {
		t.Errorf("input %#x, output %#x", raw.Iflag, raw.Oflag)
	}
	if raw.Cflag&unix.CSIZE != unix.CS8 || raw.Cc[unix.VMIN] != 1 || raw.Cc[unix.VTIME] != 0 {
		t.Errorf("control %#x, vmin %d, vtime %d", raw.Cflag, raw.Cc[unix.VMIN], raw.Cc[unix.VTIME])
	}
	if err := mode.Restore(); err != nil {
		t.Fatal(err)
	}
	after, err := unix.IoctlGetTermios(fd, reqGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	if *after != *before {
		t.Fatalf("after restoring %+v, want %+v", *after, *before)
	}
}

func TestRawModeFailures(t *testing.T) {
	if mode, err := MakeRaw(notATerminal(t)); err == nil {
		t.Fatalf("no error, got %+v", mode)
	}
	mode, err := MakeRaw(openPTY(t))
	if err != nil {
		t.Fatal(err)
	}
	mode.fd = notATerminal(t)
	if err := mode.Restore(); err == nil {
		t.Fatal("restoring on a file that is not a terminal succeeded")
	}
}

func TestIsTerminal(t *testing.T) {
	if !IsTerminal(openPTY(t)) {
		t.Error("a pseudo-terminal read as not a terminal")
	}
	if IsTerminal(notATerminal(t)) {
		t.Error("the null device read as a terminal")
	}
}

func TestTerminalSize(t *testing.T) {
	fd := openPTY(t)
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: 132, Row: 43}); err != nil {
		t.Fatal(err)
	}
	cols, rows, err := TerminalSize(fd)
	if err != nil {
		t.Fatal(err)
	}
	if cols != 132 || rows != 43 {
		t.Fatalf("got %dx%d", cols, rows)
	}
	if _, _, err := TerminalSize(notATerminal(t)); err == nil {
		t.Fatal("no error on a file that is not a terminal")
	}
}
