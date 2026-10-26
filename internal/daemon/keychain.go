package daemon

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Keychain stores and deletes the keychain option's credentials (7.5):
// service herdsman/<route>, account <name>.
type Keychain interface {
	Store(service, account, secret string) error
	Load(service, account string) (string, error)
	Delete(service, account string) error
}

// securityKeychain is the macOS login Keychain through the security tool,
// with access limited to the herdsman binary.
type securityKeychain struct{ binary string }

func (k securityKeychain) run(args ...string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("the keychain option exists only on macOS")
	}
	out, err := exec.Command("security", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func (k securityKeychain) Store(service, account, secret string) error {
	args := []string{"add-generic-password", "-U", "-s", service, "-a", account, "-w", secret}
	if k.binary != "" {
		args = append(args, "-T", k.binary)
	}
	_, err := k.run(args...)
	return err
}

func (k securityKeychain) Load(service, account string) (string, error) {
	return k.run("find-generic-password", "-s", service, "-a", account, "-w")
}

func (k securityKeychain) Delete(service, account string) error {
	_, err := k.run("delete-generic-password", "-s", service, "-a", account)
	return err
}

func (d *Daemon) keychain() Keychain {
	if d.cfg.Keychain != nil {
		return d.cfg.Keychain
	}
	return securityKeychain{binary: d.cfg.Executable}
}

// SystemKeychain is the client's keychain reader.
func SystemKeychain() Keychain { return securityKeychain{} }
