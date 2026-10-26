package daemon

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Credential generations (7.5, 4.4). An approval stages its secrets under
// names no client reads, commits its record, then activates each secret
// with one rename (file) or one store (keychain). A refused approval
// leaves the active generation untouched; replay activates only a
// generation whose staged copy is still there, and secrets are removed
// only against the final records, so an older generation never deletes a
// later one's secret.

// stagedDir is a credential generation's staging folder.
func (r *Route) stagedDir(gen string) string {
	return r.path("state", "credentials", "staged-"+gen)
}

// stagedAccount is a keychain secret's staging account.
func stagedAccount(name, gen string) string { return name + ".staged." + gen }

// stageSecrets writes a generation's secrets under staging names.
func (r *Route) stageSecrets(gen string, creds map[string]credential, secrets map[string]string) error {
	if err := mkdirs(r.inbox, "state/credentials/staged-"+gen); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(secrets)) {
		var err error
		if creds[name].Option == "file" {
			err = writeDurable(filepath.Join(r.stagedDir(gen), name), []byte(secrets[name]+"\n"), 0o600)
		} else {
			err = r.d.keychain().Store("herdsman/"+r.name, stagedAccount(name, gen), secrets[name])
		}
		if err != nil {
			return fmt.Errorf("credential for %q: %w", name, err)
		}
	}
	return nil
}

// dropStaged removes a refused generation's staged secrets.
func (r *Route) dropStaged(gen string, creds map[string]credential) {
	for name, c := range creds {
		switch c.Option {
		case "file":
			_ = os.Remove(filepath.Join(r.stagedDir(gen), name))
		case "keychain":
			_ = r.d.keychain().Delete("herdsman/"+r.name, stagedAccount(name, gen))
		}
	}
	_ = os.Remove(r.stagedDir(gen))
}

// secretMatches reports whether a secret file holds the recorded hash.
func secretMatches(p, sum string) bool {
	if !regular(p) {
		return false
	}
	b, err := readSmall(p, 256)
	return err == nil && sha256Hex([]byte(strings.TrimSpace(string(b)))) == sum
}

// activate moves a committed generation's staged secrets into place,
// idempotently: a secret whose staged copy is gone is already active, and
// an active secret is only ever replaced by its own generation's copy.
func (r *Route) activate(gen string, creds map[string]credential) error {
	if gen == "" {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(creds)) {
		c := creds[name]
		switch c.Option {
		case "file":
			staged := filepath.Join(r.stagedDir(gen), name)
			if !exists(staged) {
				continue
			}
			if !secretMatches(staged, c.SHA256) {
				return fmt.Errorf("%w: staged credential for %q does not match its record", errHeld, name)
			}
			if err := renameDurable(staged, r.path("state", "credentials", name)); err != nil {
				return err
			}
		case "keychain":
			kc := r.d.keychain()
			s, err := kc.Load("herdsman/"+r.name, stagedAccount(name, gen))
			if err != nil {
				continue // already active
			}
			if sha256Hex([]byte(s)) != c.SHA256 {
				return fmt.Errorf("%w: staged keychain credential for %q does not match its record", errHeld, name)
			}
			if err := kc.Store("herdsman/"+r.name, name, s); err != nil {
				return err
			}
			_ = kc.Delete("herdsman/"+r.name, stagedAccount(name, gen))
		}
	}
	_ = os.Remove(r.stagedDir(gen))
	return nil
}

// setCredentials records the route's current credentials; outside replay
// it then prunes the secrets the records no longer accept.
func (r *Route) setCredentials(creds map[string]credential) error {
	if creds == nil {
		creds = map[string]credential{}
	}
	if err := mkdirs(r.inbox, "state/credentials"); err != nil {
		return err
	}
	b, _ := encode(creds)
	if err := writeDurable(r.path("state", "credentials.json"), append(b, '\n'), 0o600); err != nil {
		return err
	}
	for n, c := range r.creds {
		if c.Option == "keychain" {
			r.keychainSeen[n] = true
		}
	}
	r.creds = creds
	r.d.indexCredentials(r.name, creds)
	if r.replaying {
		return nil
	}
	return r.pruneSecrets()
}

// pruneSecrets removes every secret the current records do not accept: a
// file of a party without a file credential or whose bytes differ from its
// hash, a staging folder of no unfinished generation, and the keychain
// secret of a party that no longer has one.
func (r *Route) pruneSecrets() error {
	live := map[string]bool{}
	for _, rec := range r.unfinished {
		if rec.Generation != "" {
			live["staged-"+rec.Generation] = true
		}
	}
	dir := r.path("state", "credentials")
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if gen, ok := strings.CutPrefix(e.Name(), "staged-"); ok && e.IsDir() {
			if live[e.Name()] {
				continue
			}
			subs, _ := os.ReadDir(p)
			for _, s := range subs {
				_ = os.Remove(filepath.Join(p, s.Name()))
			}
			for n, c := range r.creds {
				if c.Option == "keychain" {
					_ = r.d.keychain().Delete("herdsman/"+r.name, stagedAccount(n, gen))
				}
			}
			_ = os.Remove(p)
			continue
		}
		c, ok := r.creds[e.Name()]
		if !ok || c.Option != "file" || !secretMatches(p, c.SHA256) {
			_ = os.Remove(p)
		}
	}
	for n := range r.keychainSeen {
		if c, ok := r.creds[n]; !ok || c.Option != "keychain" {
			_ = r.d.keychain().Delete("herdsman/"+r.name, n)
			delete(r.keychainSeen, n)
		}
	}
	return syncDir(dir)
}
