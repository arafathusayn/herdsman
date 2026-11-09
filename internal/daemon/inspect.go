package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/routedb"
)

// heldInspect marks a route that an inspect daemon leaves unstarted.
const heldInspect = "inspect"

func (d *Daemon) accountDBPath() string { return filepath.Join(d.daemonDir(), "account.db") }

// openOps opens the account database, migrating it, and makes the engine.
func (d *Daemon) openOps() error {
	c, err := store.Open(d.accountDBPath(), store.Writer, store.KindAccount)
	if err != nil {
		return fmt.Errorf("account database: %w", err)
	}
	d.mu.Lock()
	d.acctDB = c
	d.recovery = ops.NewRecovery(ops.RecoveryOptions{StateDir: d.cfg.PrivateHome, Retention: d.cfg.Retention, Quiesce: d.quiesce})
	effects := d.recovery.Effects()
	maps.Copy(effects, d.operationEffects())
	d.ops = ops.NewEngine(c, ops.Options{Effects: effects, Checks: d.cfg.Checks, Now: d.cfg.Now, Fault: d.cfg.OpsFault})
	d.recovery.Attach(d.ops)
	d.mu.Unlock()
	return nil
}

// closeOps ends the recovery work and closes the account database.
func (d *Daemon) closeOps() {
	d.mu.Lock()
	cancel, c := d.opsCancel, d.acctDB
	d.acctDB = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.bg.Wait()
	if c != nil {
		_ = c.Close()
	}
}

// startInspect migrates every database and starts no route: opening one
// takes locks, finishes interrupted changes and delivers.
func (d *Daemon) startInspect(rf RoutesFile) error {
	dbs, err := filepath.Glob(filepath.Join(d.daemonDir(), "routes", "*.db"))
	if err != nil {
		return err
	}
	for _, p := range dbs {
		c, err := store.Open(p, store.Writer, store.KindRoute)
		if err != nil {
			return fmt.Errorf("route database %s: %w", filepath.Base(p), err)
		}
		if err := c.Close(); err != nil {
			return err
		}
	}
	d.mu.Lock()
	for n := range rf.Routes {
		d.held[n] = heldInspect
	}
	d.mu.Unlock()
	return nil
}

// inspectReads are the methods an inspect daemon serves: every other method
// could change something, and a new one is refused until it is listed here.
var inspectReads = map[string]bool{
	"Hello": true, "Health": true, "DaemonStatus": true, "ListOperations": true, "GetOperation": true,
	"ListRoutes": true, "Status": true, "GetSetup": true, "Authority": true, "Wait": true, "Stream": true,
	"Get": true, "List": true, "Search": true, "Plan": true, "Sources": true, "Records": true,
}

// refuseInInspect answers every call that could change something.
func (d *Daemon) refuseInInspect(r *http.Request) *APIError {
	if !d.cfg.Inspect || inspectReads[path.Base(r.URL.Path)] {
		return nil
	}
	return Errf("failed_precondition", "this daemon runs in inspect mode and changes nothing; %s %s was refused", r.Method, r.URL.Path)
}

// startOpsRecovery reconciles and resumes the journal in the background.
func (d *Daemon) startOpsRecovery(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	if d.upgrading {
		d.mu.Unlock()
		cancel()
		return
	}
	d.opsCancel, d.opsCtx = cancel, cctx
	d.bg.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.bg.Done()
		if err := d.ops.Recover(cctx); err != nil && !errors.Is(err, context.Canceled) {
			d.logf("operations: %v", err)
		}
		d.deliverOutbox(cctx)
		d.sweep(cctx)
		d.sweepLater(cctx)
	}()
}

func (d *Daemon) deliverOutbox(ctx context.Context) {
	if d.cfg.Sink == nil {
		return
	}
	if err := d.ops.DeliverOutbox(ctx, d.cfg.Sink); err != nil && !errors.Is(err, context.Canceled) {
		d.logf("outbox: %v", err)
	}
}

// SchemaStatus compares the account database with this binary.
type SchemaStatus struct {
	Current  int `json:"current"`
	Expected int `json:"expected"`
}

// StatusCounts is what the state directory holds.
type StatusCounts struct {
	Routes   int `json:"routes"`
	Tasks    int `json:"tasks"`
	Messages int `json:"messages"`
}

// StatusDetail is the state directory and what it holds.
type StatusDetail struct {
	StateDir string       `json:"state_dir"`
	Inspect  bool         `json:"inspect"`
	Schema   SchemaStatus `json:"schema"`
	Counts   StatusCounts `json:"counts"`
	Digest   string       `json:"digest"`
}

func (d *Daemon) collectStatus() (StatusDetail, error) {
	st := StatusDetail{StateDir: d.cfg.PrivateHome, Inspect: d.cfg.Inspect}
	st.Schema.Expected = store.Newest(store.KindAccount)
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		return st, err
	}
	names := make([]string, 0, len(rf.Routes))
	var recs []store.Record
	for n, e := range rf.Routes {
		names = append(names, n)
		recs = append(recs, store.RouteRecord(n, e.Inbox, e.Repo, e.HerdrSocket, e.Created))
	}
	sort.Strings(names)
	err = d.ops.Read(func(c store.Conn) error {
		if err := c.Query("PRAGMA user_version", nil, func(r store.Row) error { st.Schema.Current = int(r.Int64(0)); return nil }); err != nil {
			return err
		}
		acct, err := store.AccountRecords(c)
		recs = append(recs, acct...)
		return err
	})
	if err != nil {
		return st, err
	}
	for _, n := range names {
		msgs, err := d.routeMessages(n, rf.Routes[n])
		if err != nil {
			return st, err
		}
		recs = append(recs, msgs...)
	}
	st.Counts = StatusCounts{Routes: store.Count(recs, store.RecordRoute), Messages: store.Count(recs, store.RecordMessage)}
	st.Digest = store.Digest(recs)
	return st, nil
}

// routeMessages reads a route's messages from its database when it has one,
// and from its mailbox folders otherwise.
func (d *Daemon) routeMessages(name string, e RouteEntry) ([]store.Record, error) {
	p := routedb.Path(d.cfg.PrivateHome, name)
	if _, err := os.Lstat(p); err == nil {
		c, err := store.Open(p, store.Reader, store.KindRoute)
		if err != nil {
			return nil, err
		}
		defer c.Close()
		var recs []store.Record
		err = c.Tx(func(tx store.Conn) error {
			var err error
			recs, err = store.RouteRecords(name, tx)
			return err
		})
		return recs, err
	}
	return mailboxRecords(name, e.Inbox)
}

// mailboxRecords lists every message held in a route's folders. The folder a
// message sits in is its progress, so it is not part of the record.
func mailboxRecords(route, dir string) ([]store.Record, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []store.Record
	for _, box := range ents {
		if !box.IsDir() || !inbox.NameOK(box.Name()) {
			continue
		}
		for _, state := range []string{"new", "ready", "cur", "quarantine"} {
			for _, f := range inbox.MdFiles(filepath.Join(dir, box.Name(), state)) {
				st, err := os.Lstat(f)
				if err != nil || !st.Mode().IsRegular() {
					continue
				}
				b, err := os.ReadFile(f)
				if err != nil {
					return nil, err
				}
				sum := sha256.Sum256(b)
				recs = append(recs, store.Record{Kind: store.RecordMessage, ID: route + "/" + box.Name() + "/" + strings.TrimSuffix(filepath.Base(f), ".md"),
					Fields: []string{hex.EncodeToString(sum[:])}})
			}
		}
	}
	return recs, nil
}
