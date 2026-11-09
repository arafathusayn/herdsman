//go:build !unix

package plugin

import "os/exec"

// ownGroup does nothing where process groups do not exist.
func ownGroup(*exec.Cmd) {}

// killGroup can reach only the plugin itself here.
func killGroup(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
