package main

import (
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/message"
)

func init() {
	register(command{name: "message", summary: "The adjutant message channel: message status|wait|read|release|ack|send|hold|unhold, and register|courier|card-watch|resume-goal|authority|leak-gate.", run: runMessage})
}

// executable is the running binary, links resolved, for the binary setup proof.
func executable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func runMessage(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	e := &message.Env{Getenv: os.Getenv, Stdout: stdout, Stderr: stderr, Client: herdrClient(), Executable: executable()}
	// As the scripts' traps: an interrupted command frees its locks and
	// exits 130, 143 or 129.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer close(sigs)
	defer signal.Stop(sigs)
	go func() {
		s, ok := <-sigs
		if !ok {
			return
		}
		e.Release()
		os.Exit(128 + int(s.(syscall.Signal)))
	}()
	return e.Main(args)
}
