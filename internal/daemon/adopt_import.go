package daemon

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/routedb"
)

// Adoption's import (sqlite-storage.md section 5, step 3): before the
// adoption changes anything of the inbox, the importer reads it, under
// the adoption's locks, into a staging route database in one
// transaction; the census refuses a file with no target and no listed
// reason. A refusal (census, malformed source, storage) removes the
// staging file and refuses the adoption with the inbox as it was. At
// publish the staging file becomes the route's database,
// <private home>/daemon/routes/<route>.db; the daemon still serves the
// route from its contract 2 files, and the database is the imported
// state its readers come to (owner contract 3).

// stagingPath is the staging route database of an adoption of epoch.
func (d *Daemon) stagingPath(name, epoch string) string {
	return filepath.Join(filepath.Dir(routedb.Path(d.cfg.PrivateHome, name)), name+".staging-"+epoch+".db")
}

// removeDB removes a database file and its -wal and -shm.
func removeDB(p string) {
	for _, s := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(p + s)
	}
}

// importStaging imports the inbox dir into the staging database of the
// adoption of route name with epoch. A database already at the route's
// path is never overwritten.
func (d *Daemon) importStaging(name, dir, epoch string) *APIError {
	final := routedb.Path(d.cfg.PrivateHome, name)
	if _, err := os.Lstat(final); err == nil {
		return Errf("route_exists", "route database %s already exists; move it away before adopting", final)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Errf("internal", "route database %s: %v", final, err)
	}
	routes := filepath.Dir(final)
	if err := os.MkdirAll(routes, 0o700); err != nil {
		return Errf("internal", "route database folder: %v", err)
	}
	// Leftovers of an earlier adoption of this route that never published.
	old, _ := filepath.Glob(filepath.Join(routes, name+".staging-*.db"))
	for _, p := range old {
		removeDB(p)
	}
	p := d.stagingPath(name, epoch)
	c, err := store.Open(p, store.Writer, store.KindRoute)
	if err != nil {
		removeDB(p)
		return Errf("internal", "staging route database: %v", err)
	}
	var ae *APIError
	err = c.Tx(func(tx store.Conn) error {
		if err := store.InitRoute(tx, epoch); err != nil {
			return err
		}
		if ae = importCore(tx, dir, d.now().Unix()); ae != nil {
			return ae
		}
		return nil
	})
	if cerr := c.Close(); err == nil {
		err = cerr
	}
	if ae != nil || err != nil {
		removeDB(p)
		if ae != nil {
			return ae
		}
		return Errf("internal", "staging import: %v", err)
	}
	return nil
}

// publishStaging makes the adoption's staging database the route's
// database; a resume after that rename finds it done.
func (d *Daemon) publishStaging(name, epoch string) *APIError {
	final, p := routedb.Path(d.cfg.PrivateHome, name), d.stagingPath(name, epoch)
	staged, published := exists(p), exists(final)
	switch {
	case staged && published:
		return Errf("route_exists", "route database %s already exists; the adoption stays pending", final)
	case published:
		return nil
	case !staged:
		return Errf("internal", "the staging import %s is missing; the adoption stays pending", p)
	}
	// The store keeps a closed database's -wal and -shm, which its reader
	// needs: they move first, the database file last (the commit).
	for _, s := range []string{"-wal", "-shm", ""} {
		if s != "" && !exists(p+s) {
			continue
		}
		if err := renameDurable(p+s, final+s); err != nil {
			return Errf("internal", "publishing the route database: %v; the adoption stays pending", err)
		}
	}
	return nil
}
