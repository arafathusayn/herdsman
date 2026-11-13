package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/runtime/coreconfig"
	"github.com/arafathusayn/herdsman/sdk/config"
)

// CoreState is what an installed herdr-core says about itself.
type CoreState struct {
	// Version is what the program prints for --version, such as "hc 0.1.19".
	Version string
	// Running says a herdr-core process is in the process table.
	Running bool
	// Holders are the command lines of the holder processes the running core
	// lists, as the process table shows them.
	Holders []string
}

// checkCore tests what the launcher check assumes about herdr-core against the
// installed program: that a holder, the parent of each terminal's program,
// runs under a name herdr.IsCoreProcess knows. A change in herdr-core that
// breaks the assumption would otherwise go unseen, and a command started
// through herdr-core would pass as typed by a person. It reads what a running
// core reports and starts nothing, so with no live terminal there is nothing
// to test.
func checkCore(ctx context.Context, p Probes, add adder) {
	if p.Core == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := p.Core(cctx)
	switch {
	case err != nil:
		add(Warn, "herdr-core", "the launcher check is not tested against herdr-core: "+err.Error())
	case st == nil: // not installed
	case !st.Running:
		add(OK, "herdr-core", st.Version+" is not running, so the launcher check is not tested against it")
	case len(st.Holders) == 0:
		add(OK, "herdr-core", st.Version+" has no live terminal, so the launcher check is not tested against it")
	default:
		for _, holder := range st.Holders {
			if !herdr.IsCoreProcess(holder) {
				add(Fail, "herdr-core", fmt.Sprintf("%s: the launcher check does not know its holder process (%s), so a command started through herdr-core passes as typed by a person; update herdsman", st.Version, processName(holder)))
				return
			}
		}
		add(OK, "herdr-core", fmt.Sprintf("%s: the launcher check knows its holder process (%s)", st.Version, processName(st.Holders[0])))
	}
}

// processName shortens a command line to its program's name and first
// argument, such as "hc hold".
func processName(command string) string {
	words := strings.Fields(command)
	if len(words) == 0 {
		return "no command line"
	}
	words[0] = filepath.Base(words[0])
	return strings.Join(words[:min(len(words), 2)], " ")
}

// coreState reads the installed herdr-core, or returns nil when none is on
// PATH. It asks the core for its status only when a herdr-core process is
// already running, because any question starts a core that is not.
func coreState(ctx context.Context) (*CoreState, error) {
	binary, socket, err := configuredCore()
	if err != nil {
		return nil, err
	}
	return coreStateOf(ctx, binary, socket)
}

// configuredCore is the hc program and the core socket of the config in use:
// [runtime.core] binary (empty when the config names none) and the endpoint
// its socket or session resolves to. HC_SOCKET and HC_SESSION are never read.
func configuredCore() (binary, socket string, err error) {
	dir, _ := os.Getwd()
	result, err := config.Load(config.Options{RepoRoot: nearestRepository(dir)})
	if err != nil {
		return "", "", err
	}
	s, err := coreconfig.Resolve(coreconfig.Input{Runtime: result.Config.GetRuntime(), Sources: result.Sources, ProjectPath: result.ProjectPath, UserPath: result.UserPath})
	if err != nil {
		return "", "", err
	}
	if s.Binary.From != coreconfig.FromDefault {
		binary = s.Binary.Value
	}
	return binary, s.Socket.Value, nil
}

// coreBinary finds the hc program: the configured one, which must be there,
// else the first of herdr-core's names on PATH; empty when none is installed.
func coreBinary(binary string, lookPath func(string) (string, error)) (string, error) {
	if binary != "" {
		found, err := lookPath(binary)
		if err != nil {
			return "", fmt.Errorf("[runtime.core] binary %q: %w", binary, err)
		}
		return found, nil
	}
	for _, name := range herdr.CoreNames {
		if found, err := lookPath(name); err == nil {
			return found, nil
		}
	}
	return "", nil
}

// coreCommand runs the hc program against the configured core: HC_SOCKET is
// the resolved socket, and the caller's HC_SESSION and HC_TERMINAL_ID, which
// may be another agent's scope, are left out.
func coreCommand(ctx context.Context, bin, socket string, environ []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if k != "HC_SOCKET" && k != "HC_SESSION" && k != "HC_TERMINAL_ID" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if socket != "" {
		cmd.Env = append(cmd.Env, "HC_SOCKET="+socket)
	}
	return cmd
}

// coreStateOf reads the hc program binary (or the installed one when empty)
// about the core at socket.
func coreStateOf(ctx context.Context, binary, socket string) (*CoreState, error) {
	bin, err := coreBinary(binary, exec.LookPath)
	if err != nil || bin == "" {
		return nil, err
	}
	name := filepath.Base(bin)
	out, err := coreCommand(ctx, bin, socket, os.Environ(), "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("%s --version: %w", name, err)
	}
	st := &CoreState{Version: strings.TrimSpace(string(out))}
	table, err := exec.CommandContext(ctx, "ps", "-axo", "command=").Output()
	if err != nil {
		return nil, fmt.Errorf("read the process table: %w", err)
	}
	st.Running = slices.ContainsFunc(strings.Split(string(table), "\n"), herdr.IsCoreProcess)
	if !st.Running {
		return st, nil
	}
	out, err = coreCommand(ctx, bin, socket, os.Environ(), "server", "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("%s server status: %w", name, err)
	}
	pids, err := holderPIDs(out)
	if err != nil {
		return nil, fmt.Errorf("%s server status: %w", name, err)
	}
	for _, pid := range pids {
		row, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			continue // the holder ended after the core listed it
		}
		if command := strings.TrimSpace(string(row)); command != "" {
			st.Holders = append(st.Holders, command)
		}
	}
	return st, nil
}

// holderPIDs reads the holders' process IDs from `hc server status --json`.
// A status without the list is an error: the launcher check cannot be tested
// against a core that no longer names its holders.
func holderPIDs(status []byte) ([]int, error) {
	var parsed struct {
		Holders *[]struct {
			PID int `json:"pid"`
		} `json:"holders"`
	}
	if err := json.Unmarshal(status, &parsed); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if parsed.Holders == nil {
		return nil, errors.New("no holders list")
	}
	var pids []int
	for _, holder := range *parsed.Holders {
		if holder.PID > 1 {
			pids = append(pids, holder.PID)
		}
	}
	return pids, nil
}
