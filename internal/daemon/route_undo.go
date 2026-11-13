package daemon

import (
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
)

// A route delete forgets the route's daemon channel once its inbox is gone.
// Undo of that delete puts the inbox back, so it registers the channel again,
// on either backend: the route is then a stopped route whose resume starts its
// agents under fresh launches (its executions ended with the delete and are
// never restored).

// undone are the snapshots an undo restored. A route delete's snapshot is
// named by the delete's operation id, so a delete that undo reversed leaves
// the route stopped, not deleted.
func (s *routeService) undone() map[string]bool {
	all, err := s.d.ops.List()
	if err != nil {
		return nil
	}
	return ops.UndoneSnapshots(all)
}

// restoreDeletedRoute registers again the channel of the route whose delete
// took the snapshot undo just restored. Any other snapshot changes nothing.
func (d *Daemon) restoreDeletedRoute(snapshot string) error {
	del, err := d.ops.Get(snapshot)
	if err != nil {
		return nil // not an operation's snapshot
	}
	return d.restoreChannel(del)
}

// restoreChannel registers the channel a finished delete forgot, once undo
// has put its inbox back. Only an inbox the daemon owns comes back as a
// channel: a script-owned TUI inbox was never registered. A name registered
// again since, at any inbox, is left alone.
func (d *Daemon) restoreChannel(del *ops.Operation) error {
	if del.Kind != route.KindDelete || del.State != ops.OpDone {
		return nil
	}
	ch, ok := route.ForgottenChannelOf(del)
	if !ok || !validName(ch.Route) || !filepath.IsAbs(ch.Inbox) {
		return nil
	}
	if _, owned := ReadOwner(ch.Inbox); !owned {
		return nil
	}
	d.regMu.Lock()
	rf, err := ReadRoutes(d.cfg.PrivateHome)
	if err != nil {
		d.regMu.Unlock()
		return err
	}
	if _, taken := rf.Routes[ch.Route]; taken {
		d.regMu.Unlock()
		return nil
	}
	if e := d.nameTaken(rf, ch.Route, ""); e != nil {
		d.regMu.Unlock()
		return e
	}
	rf.Routes[ch.Route] = RouteEntry{Inbox: ch.Inbox, Repo: ch.Repo, HerdrSocket: ch.HerdrSocket, Created: d.now().Unix()}
	if err := d.writeRoutes(rf); err != nil {
		d.regMu.Unlock()
		return err
	}
	entry := rf.Routes[ch.Route]
	d.regMu.Unlock()
	d.openRoute(ch.Route, entry)
	d.logf("route %s: its channel is registered again at %s, which undo restored", ch.Route, ch.Inbox)
	return nil
}
