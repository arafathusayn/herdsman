package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const envPrivateHome = "HERDSMAN_PRIVATE_HOME"

// These are variables so that a test need not wait for them.
var (
	daemonStartLimit = 30 * time.Second
	daemonStopLimit  = 10 * time.Second
)

// daemonFolder makes the benchmark's own daemon state folder. Its path is
// short because a socket path has a small limit on macOS.
func daemonFolder() (root, private string, err error) {
	if root, err = os.MkdirTemp("", "hb"); err != nil {
		return "", "", err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", errors.Join(err, os.RemoveAll(root))
	}
	private = filepath.Join(resolved, "p")
	if err := os.Mkdir(private, 0o700); err != nil {
		return "", "", errors.Join(err, os.RemoveAll(root))
	}
	return resolved, private, nil
}

// startDaemon runs a daemon on the fixture's home and state folder, returns
// when its socket answers, and hands back the function that stops it.
func startDaemon(binary string, env []string, dir, private string) (func() error, error) {
	var log bytes.Buffer
	cmd := exec.Command(binary, "daemon", "run")
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = &log, &log
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	socket := filepath.Join(private, "run", "daemon.sock")
	for deadline := time.Now().Add(daemonStartLimit); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		select {
		case <-exited:
			return nil, fmt.Errorf("the daemon exited: %s", firstLine(log.Bytes()))
		default:
		}
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			return func() error { return stopDaemon(cmd, exited) }, nil
		}
	}
	_ = cmd.Process.Kill()
	<-exited
	return nil, fmt.Errorf("the daemon did not answer: %s", firstLine(log.Bytes()))
}

func stopDaemon(cmd *exec.Cmd, exited <-chan struct{}) error {
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case <-exited:
		return nil
	case <-time.After(daemonStopLimit):
		_ = cmd.Process.Kill()
		<-exited
		return errors.New("the daemon did not stop when asked")
	}
}

// runWithDaemon gives a case that needs one a daemon for its whole run. A
// daemon that cannot start or stop makes the case unavailable, never faster.
func (b *Bench) runWithDaemon(c Case, rounds int, measure measureFunc) CaseResult {
	if !c.Daemon {
		return runCase(c, rounds, measure)
	}
	stop, err := b.daemon()
	if err != nil {
		return unavailable(c, "daemon: "+err.Error())
	}
	result := runCase(c, rounds, measure)
	if err := stop(); err != nil {
		return unavailable(c, "daemon: "+err.Error())
	}
	return result
}
