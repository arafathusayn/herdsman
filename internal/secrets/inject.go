// Package secrets reads credentials from the system keychain when a process
// starts and keeps them out of what herdsman shows or records. A value exists
// only in the environment of the process that needs it: never on a command
// line, in a file, or in an error.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RefsVariable names the environment variable that lists the references. It
// holds names only, in the form NAME=service/account separated by commas.
const RefsVariable = "HERDSMAN_SECRETS"

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Ref says where one environment variable's value is stored.
type Ref struct{ Env, Service, Account string }

// Keychain reads stored secrets. A missing item is os.ErrNotExist.
type Keychain interface {
	Load(service, account string) (string, error)
}

// ParseRefs reads a reference list. A service may contain slashes; an account
// may not, so the last slash separates them.
func ParseRefs(spec string) ([]Ref, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	var refs []Ref
	for item := range strings.SplitSeq(spec, ",") {
		name, location, ok := strings.Cut(strings.TrimSpace(item), "=")
		slash := strings.LastIndexByte(location, '/')
		if !ok || !envName.MatchString(name) || slash <= 0 || slash == len(location)-1 {
			return nil, fmt.Errorf("secrets: reference %q is not NAME=service/account", item)
		}
		if slices.ContainsFunc(refs, func(r Ref) bool { return r.Env == name }) {
			return nil, fmt.Errorf("secrets: %s is listed twice", name)
		}
		refs = append(refs, Ref{name, location[:slash], location[slash+1:]})
	}
	return refs, nil
}

// Set is the secrets one process loaded.
type Set struct {
	refs     []Ref
	values   []string
	redactor *Redactor
}

// Load reads every reference. One that cannot be read fails the whole load,
// because a process started without its redaction list could show the value.
func Load(k Keychain, refs []Ref) (*Set, error) {
	s := &Set{refs: refs}
	for _, ref := range refs {
		v, err := k.Load(ref.Service, ref.Account)
		if err != nil {
			return nil, fmt.Errorf("secrets: cannot read %s from the keychain: %w", ref.Env, err)
		}
		if v == "" {
			return nil, fmt.Errorf("secrets: the keychain item for %s is empty", ref.Env)
		}
		s.values = append(s.values, v)
	}
	s.redactor = NewRedactor(s.values...)
	return s, nil
}

// FromEnv loads what RefsVariable lists; unset means no secrets.
func FromEnv(getenv func(string) string, k Keychain) (*Set, error) {
	refs, err := ParseRefs(getenv(RefsVariable))
	if err != nil {
		return nil, err
	}
	return Load(k, refs)
}

// Redactor is the redactor for this set's values.
func (s *Set) Redactor() *Redactor { return s.redactor }

// Environ is base for a child process, with each secret's variable set from
// the keychain. An inherited variable of the same name is dropped, so the
// keychain is the only place the value can come from.
func (s *Set) Environ(base []string) []string {
	out := make([]string, 0, len(base)+len(s.refs))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(s.refs, func(r Ref) bool { return r.Env == name }) {
			out = append(out, kv)
		}
	}
	for i, ref := range s.refs {
		out = append(out, ref.Env+"="+s.values[i])
	}
	return out
}

type systemKeychain struct{}

// System is this machine's keychain.
func System() Keychain { return systemKeychain{} }

// Load runs the platform tool. Its standard error is discarded and only the
// exit code is reported, so no tool output can reach an error message.
func (systemKeychain) Load(service, account string) (string, error) {
	out, err := exec.Command(ToolName, toolArgs(service, account)...).Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			if strconv.Itoa(ee.ExitCode()) == notFoundExit {
				return "", os.ErrNotExist
			}
			return "", fmt.Errorf("%s exited with code %d", ToolName, ee.ExitCode())
		}
		return "", fmt.Errorf("%s could not run", ToolName)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}
