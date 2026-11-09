package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed requirements.md
var requirementsFile string

// Declaration is what requirements.md says about a capability on a platform.
type Declaration string

const (
	// Undeclared means the file says nothing, which a gate may not rely on.
	Undeclared Declaration = ""
	Provided   Declaration = "provided"
	Absent     Declaration = "absent"
)

var platforms = []string{"darwin", "linux"}

// Requirements holds the declarations by platform, then by capability.
type Requirements map[string]map[string]Declaration

// Declared returns the declaration, or Undeclared for an unknown platform or
// capability.
func (r Requirements) Declared(goos, capability string) Declaration {
	return r[goos][capability]
}

// ParseRequirements reads the first table whose first column is Capability. A
// value it cannot read is an error: a typo must not turn into a skip.
func ParseRequirements(text string) (Requirements, error) {
	var columns map[string]int
	reqs := Requirements{}
	for _, line := range strings.Split(text, "\n") {
		cells, isRow := tableCells(line)
		if columns == nil {
			if isRow && cells[0] == "Capability" {
				var err error
				if columns, err = platformColumns(cells); err != nil {
					return nil, err
				}
			}
			continue
		}
		if !isRow {
			break
		}
		if strings.HasPrefix(cells[0], "---") {
			continue
		}
		if err := reqs.addRow(cells, columns); err != nil {
			return nil, err
		}
	}
	if len(reqs) == 0 {
		return nil, errors.New("requirements: no capability table")
	}
	return reqs, nil
}

func tableCells(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return nil, false
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	for i, part := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(part), "`")
	}
	return parts, true
}

func platformColumns(header []string) (map[string]int, error) {
	columns := map[string]int{}
	for i, name := range header {
		columns[name] = i
	}
	for _, goos := range platforms {
		if _, ok := columns[goos]; !ok {
			return nil, fmt.Errorf("requirements: no column for %s", goos)
		}
	}
	return columns, nil
}

func (r Requirements) addRow(cells []string, columns map[string]int) error {
	capability := cells[0]
	for _, goos := range platforms {
		if columns[goos] >= len(cells) {
			return fmt.Errorf("requirements: %s has no value for %s", capability, goos)
		}
		value := Declaration(cells[columns[goos]])
		if value != Provided && value != Absent {
			return fmt.Errorf("requirements: %s on %s is %q, want provided or absent", capability, goos, value)
		}
		if r[goos] == nil {
			r[goos] = map[string]Declaration{}
		}
		if _, dup := r[goos][capability]; dup {
			return fmt.Errorf("requirements: %s appears twice", capability)
		}
		r[goos][capability] = value
	}
	return nil
}

// Probe reports why a capability is not usable on this machine, or nil.
type Probe func() error

// Decide settles a gate before it starts. Only a declaration of absence may
// skip; everything else that is not a working capability fails the run.
func Decide(reqs Requirements, goos string, known map[string]Probe, gate Gate) Result {
	var absent []string
	for _, need := range gate.Needs {
		switch reqs.Declared(goos, need) {
		case Absent:
			absent = append(absent, need)
		case Provided:
			probe, ok := known[need]
			if !ok {
				return Result{Gate: gate.Name, Status: StatusFail, Reason: "no probe for capability " + need}
			}
			if err := probe(); err != nil {
				return Result{Gate: gate.Name, Status: StatusFail, Reason: fmt.Sprintf("%s is declared provided on %s and is missing: %v", need, goos, err)}
			}
		default:
			return Result{Gate: gate.Name, Status: StatusFail, Reason: fmt.Sprintf("capability %s is not declared for %s in requirements.md", need, goos)}
		}
	}
	if len(absent) > 0 {
		return Result{Gate: gate.Name, Status: StatusSkip, Reason: fmt.Sprintf("%s declared absent on %s", strings.Join(absent, ", "), goos)}
	}
	return Result{Gate: gate.Name, Status: StatusRun}
}

func runTool(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// containerImage is the image the container gate runs. The gate never pulls,
// so the image is part of the capability.
const containerImage = "alpine:3"

// These are replaced by tests.
var (
	geteuid   = os.Geteuid
	ptyDevice = "/dev/ptmx"
)

func probes() map[string]Probe { return probesFor(runtime.GOOS) }

func probesFor(goos string) map[string]Probe {
	self := strconv.Itoa(os.Getpid())
	return map[string]Probe{
		"ps": func() error {
			if err := runTool("ps", "-o", "lstart=", "-p", self); err != nil {
				return err
			}
			if goos == "darwin" {
				return runTool("ps", "-E", "-p", self)
			}
			return runTool("ps", "e", "-p", self)
		},
		"lsof":    func() error { return runTool("lsof", "-nP", "-a", "-p", self, "-d", "cwd", "-Fn") },
		"signals": func() error { return syscall.Kill(os.Getpid(), 0) },
		"docker": func() error {
			if err := runTool("docker", "info"); err != nil {
				return err
			}
			return runTool("docker", "image", "inspect", containerImage)
		},
		"btrfs-loopback": func() error {
			for _, tool := range []string{"mkfs.btrfs", "losetup"} {
				if _, err := exec.LookPath(tool); err != nil {
					return err
				}
			}
			if geteuid() == 0 {
				return nil
			}
			return runTool("sudo", "-n", "true")
		},
		"keychain": func() error {
			if goos == "darwin" {
				return runTool("security", "list-keychains")
			}
			_, err := exec.LookPath("secret-tool")
			return err
		},
		"pty": func() error {
			f, err := os.OpenFile(ptyDevice, os.O_RDWR|syscall.O_NOCTTY, 0)
			if err != nil {
				return err
			}
			return f.Close()
		},
	}
}
