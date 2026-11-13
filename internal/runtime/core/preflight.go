package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/hc"
)

// Codes of the core provider's refusals. Callers branch on these.
const (
	CodeEndpointMismatch  = "endpoint_mismatch"
	CodeUnqualified       = "unqualified_harness"
	CodeRestricted        = "restricted_on_core"
	CodeProgramNotFound   = "program_not_found"
	CodeRootUnreadable    = "root_unreadable"
	CodeFrozen            = "endpoint_frozen"
	CodeNeedsAttention    = "needs_attention"
	CodeWaitingVisibility = "waiting_for_visibility"
	CodeVisibilityMissing = "visibility_missing"
	CodeConflict          = "conflict"
	CodeOutcomeUnknown    = "outcome_unknown"
	CodeLaunchFailed      = "launch_failed"
	CodeIdentityNotProven = "identity_unknown"
	CodeDeliveryNotOnCore = "delivery_unavailable"
)

// Error is a refusal with a stable code. Its message starts with the code, so
// a halted step's reason carries it to the person.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// Is matches an *Error of the same code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code && t.Msg == ""
}

func refuse(code, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// CodeOf returns the code of a core refusal, or "".
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Executor runs a program where core runs, without a terminal.
type Executor interface {
	Exec(ctx context.Context, req hc.ExecRequest) (hc.ExecResult, error)
}

// PreflightInput is what one launch's preflight checks (spec section 8,
// step 1).
type PreflightInput struct {
	Kind string
	// Argv is the harness command as the person wrote it; Argv[0] may be a
	// bare name.
	Argv []string
	// Roots are the repository and worktree roots the agent works in.
	Roots []string
	// Restricted is true under the restricted profile.
	Restricted bool
	// ExpectCore is the core id the route was started on; empty for a new
	// route.
	ExpectCore string
}

// PreflightResult is what preflight established.
type PreflightResult struct {
	Core    hc.CoreInfo
	Version string
	// Program is Argv[0] resolved as core sees it.
	Program string
	Adapter Adapter
}

// Preflight checks one launch against the verified core before anything is
// reserved: the core's identity, the harness's qualification and its required
// arguments, the restricted profile's rule, the program as core resolves it
// (the daemon's PATH is not core's), and that core can read every root (a
// privacy prompt from macOS belongs to core, not to Herdsman).
func Preflight(ctx context.Context, c *hc.Client, in PreflightInput) (PreflightResult, error) {
	out := PreflightResult{Core: c.Core(), Version: c.Hello().Version}
	if in.ExpectCore != "" && in.ExpectCore != out.Core.ID {
		return out, refuse(CodeEndpointMismatch, "the route runs on core %s and this socket leads to core %s; nothing is launched until a person settles which core is right", in.ExpectCore, out.Core.ID)
	}
	a, err := AdapterFor(in.Kind)
	if err != nil {
		return out, refuse(CodeUnqualified, "%v", err)
	}
	out.Adapter = a
	if in.Restricted && !a.RestrictedOnCore {
		return out, refuse(CodeRestricted, "the restricted profile does not launch %s on herdr-core: no restricted worker is qualified on core; set backend = \"herdr-tui\" in [runtime] for this repository", in.Kind)
	}
	if len(in.Argv) == 0 {
		return out, refuse(CodeProgramNotFound, "the launch has no command")
	}
	if err := a.CheckArgs(in.Argv[1:]); err != nil {
		return out, refuse(CodeUnqualified, "%v", err)
	}
	if out.Program, err = ResolveProgram(ctx, c, in.Argv[0]); err != nil {
		return out, err
	}
	for _, root := range in.Roots {
		if err := CheckRoot(ctx, c, root); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ResolveProgram resolves a program name to the absolute path core runs, by
// asking a shell started by core. An absolute name is checked as it is.
func ResolveProgram(ctx context.Context, c Executor, name string) (string, error) {
	if filepath.IsAbs(name) {
		res, err := c.Exec(ctx, hc.ExecRequest{Argv: []string{"/bin/test", "-x", name}, TimeoutSeconds: 10})
		if err != nil {
			return "", refuse(CodeProgramNotFound, "check %s through core: %v", name, err)
		}
		if res.Code == nil || *res.Code != 0 {
			return "", refuse(CodeProgramNotFound, "core cannot run %s", name)
		}
		return name, nil
	}
	res, err := c.Exec(ctx, hc.ExecRequest{Argv: []string{"/bin/sh", "-c", `command -v -- "$1"`, "sh", name}, TimeoutSeconds: 10})
	if err != nil {
		return "", refuse(CodeProgramNotFound, "resolve %s through core: %v", name, err)
	}
	path := strings.TrimSpace(string(res.Stdout))
	if res.Code == nil || *res.Code != 0 || !filepath.IsAbs(path) || strings.ContainsAny(path, "\n") {
		return "", refuse(CodeProgramNotFound, "core finds no program %s on its PATH; give its absolute path in the launch command", name)
	}
	return path, nil
}

// CheckRoot asks core to list a folder. A denial from macOS privacy controls
// shows here, where no status would report it.
func CheckRoot(ctx context.Context, c Executor, root string) error {
	res, err := c.Exec(ctx, hc.ExecRequest{Argv: []string{"/bin/ls", "-a", root}, TimeoutSeconds: 20})
	if err != nil {
		return refuse(CodeRootUnreadable, "list %s through core: %v", root, err)
	}
	if res.Code == nil || *res.Code != 0 || res.TimedOut {
		return refuse(CodeRootUnreadable, "herdr-core cannot read %s; grant Herdr Core access to that folder in System Settings, Privacy & Security, or move the repository", root)
	}
	return nil
}
