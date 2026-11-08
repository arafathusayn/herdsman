package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/policy"
)

// Role is what an agent does on a task, as far as its roots are concerned.
type Role string

// The roles.
const (
	RoleImplementer Role = "implementer"
	RoleIntegrator  Role = "integrator"
)

// RootRequest asks what one agent may write.
type RootRequest struct {
	Policy   policy.Policy
	Role     Role
	TaskRoot string
	// Protected names paths no root may hold or sit inside, such as the
	// configuration that decides what the agent is allowed.
	Protected []string
}

// Roots is a resolved grant: absolute paths with every link followed.
type Roots struct {
	Primary string
	Extra   []string
}

// ResolveRoots returns the grant for one agent. Only an integrator receives
// the policy's named roots; everyone else gets their own folder.
func ResolveRoots(req RootRequest) (Roots, error) {
	protected := make([]string, 0, len(req.Protected))
	for _, p := range req.Protected {
		real, err := resolveExisting(p)
		if err != nil {
			return Roots{}, err
		}
		protected = append(protected, real)
	}
	primary, err := resolveRoot(req.TaskRoot, protected)
	if err != nil {
		return Roots{}, err
	}
	out := Roots{Primary: primary}
	if req.Role != RoleIntegrator || req.Policy.NoRoots {
		return out, nil
	}
	for _, root := range req.Policy.Roots {
		real, err := resolveRoot(root, protected)
		if err != nil {
			return Roots{}, err
		}
		if real != primary {
			out.Extra = append(out.Extra, real)
		}
	}
	slices.Sort(out.Extra)
	out.Extra = slices.Compact(out.Extra)
	return out, nil
}

func resolveRoot(root string, protected []string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("sandbox root %q is not absolute", root)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("sandbox root: %w", err)
	}
	for _, p := range protected {
		if within(p, real) || within(real, p) {
			return "", fmt.Errorf("sandbox root %s overlaps protected path %s", real, p)
		}
	}
	return real, nil
}

// resolveExisting follows the links of the longest part of p that exists, so
// that a file not created yet is still compared by its real location.
func resolveExisting(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("protected path %q is not absolute", p)
	}
	rest := ""
	for current := filepath.Clean(p); ; current = filepath.Dir(current) {
		real, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("protected path: %w", err)
		}
		rest = filepath.Join(filepath.Base(current), rest)
	}
}

// within says whether path is dir or lies below it.
func within(dir, path string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, string(os.PathSeparator))+string(os.PathSeparator))
}
