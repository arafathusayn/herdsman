//go:build unix

package plugin

import (
	"os/exec"
	"syscall"
)

// ownGroup makes the plugin the leader of a new process group.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killGroup ends the plugin's group, descendants included. A group that has
// already emptied answers with an error, which means there is nothing to do.
func killGroup(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
