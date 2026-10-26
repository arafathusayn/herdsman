package main

import (
	"io"
	"os"

	"github.com/arafathusayn/herdsman/internal/daemon"
)

func init() {
	register(command{name: "daemon", summary: "The per-user message channel daemon: run|install|uninstall|start|stop|restart|status|route|setup|pair|credential.", run: runDaemon})
}

// daemonVersion is this binary's version as the daemon handshake compares
// it (5.6).
func daemonVersion() string { return daemon.BuildVersion(version, commit) }

func runDaemon(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return daemon.RunDaemon(&daemon.AdminEnv{Getenv: os.Getenv, Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Version: daemonVersion(), Executable: executable(), Attended: isTerminal(stdout) && stdinTerminal(stdin)}, args)
}

func stdinTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
