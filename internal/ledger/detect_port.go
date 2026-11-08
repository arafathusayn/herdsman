package ledger

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// PortRange is a task's port block, both ends included. The zero value holds
// no port.
type PortRange struct {
	First int
	Last  int
}

func (r PortRange) contains(port int) bool {
	return r.First > 0 && port >= r.First && port <= r.Last
}

// Listener is one listening socket.
type Listener struct {
	Pid  int
	Host string
	Port int
}

// Connection is one established socket as its owner sees it.
type Connection struct {
	Pid        int
	LocalHost  string
	LocalPort  int
	RemoteHost string
	RemotePort int
}

func hostPort(address string) (string, int, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, fmt.Errorf("ledger: lsof address %q: %w", address, err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		return "", 0, fmt.Errorf("ledger: lsof port in %q: %w", address, err)
	}
	return host, number, nil
}

// ParseLsofListeners reads `lsof -iTCP -sTCP:LISTEN -P -n -F pn`.
func ParseLsofListeners(raw []byte) ([]Listener, error) {
	var listeners []Listener
	err := walkLsof(raw, func(pid int, _, name string) error {
		host, port, err := hostPort(name)
		if err != nil {
			return err
		}
		listeners = append(listeners, Listener{Pid: pid, Host: host, Port: port})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return listeners, nil
}

// ParseLsofConnections reads `lsof -iTCP -sTCP:ESTABLISHED -P -n -F pn`.
func ParseLsofConnections(raw []byte) ([]Connection, error) {
	var connections []Connection
	err := walkLsof(raw, func(pid int, _, name string) error {
		local, remote, found := strings.Cut(name, "->")
		if !found {
			return fmt.Errorf("ledger: lsof connection %q has no remote side", name)
		}
		localHost, localPort, err := hostPort(local)
		if err != nil {
			return err
		}
		remoteHost, remotePort, err := hostPort(remote)
		if err != nil {
			return err
		}
		connections = append(connections, Connection{Pid: pid, LocalHost: localHost, LocalPort: localPort, RemoteHost: remoteHost, RemotePort: remotePort})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connections, nil
}

// outsiders names who uses a listener from outside the task: a local client
// that is not a task process, a socket on the listener's port to another host,
// whichever pid holds it since a forked handler owns its connection, or a
// loopback socket on the listener with no visible client row, as for a client
// of another user.
func outsiders(listener Listener, connections []Connection, inside map[int]bool) []string {
	var names []string
	for _, c := range connections {
		local := net.ParseIP(c.RemoteHost).IsLoopback()
		serverSide := c.LocalPort == listener.Port
		switch {
		case c.RemotePort == listener.Port && c.Pid != listener.Pid && !inside[c.Pid]:
			// Any address counts: a client may reach the listener through a
			// local interface, and a false match only makes the server shared.
			names = append(names, "pid:"+strconv.Itoa(c.Pid))
		case !local && serverSide:
			names = append(names, "remote:"+c.RemoteHost)
		case local && serverSide:
			names = append(names, unresolvedPeers(c, connections, inside)...)
		}
	}
	return names
}

// unresolvedPeers inspects every owner of the other end of a server-side
// socket, where both reversed addresses and ports must match. A socket can
// have several owners after a fork, so every outside owner is named, and an
// inside owner resolves the peer only when there is no outside one. With no
// owner, or only the listener's own row outside the task, the client is
// unattributed.
func unresolvedPeers(server Connection, connections []Connection, inside map[int]bool) []string {
	var outside []string
	resolved := false
	for _, c := range connections {
		if c.LocalHost != server.RemoteHost || c.LocalPort != server.RemotePort ||
			c.RemoteHost != server.LocalHost || c.RemotePort != server.LocalPort {
			continue
		}
		switch {
		case inside[c.Pid]:
			resolved = true
		case c.Pid != server.Pid:
			outside = append(outside, "pid:"+strconv.Itoa(c.Pid))
		}
	}
	if len(outside) > 0 || resolved {
		return outside
	}
	return []string{"unattributed:" + strconv.Itoa(server.RemotePort)}
}

// listenerShares is every reason a listener is not the task's alone.
func (p *planner) listenerShares(listener Listener, inside map[int]bool) []string {
	shares := outsiders(listener, p.s.Connections, inside)
	for _, other := range p.s.Others {
		if other.Ports.contains(listener.Port) {
			shares = append(shares, "task:"+other.Task)
		}
	}
	if process, ok := p.byPid[listener.Pid]; ok {
		if execution, has := process.execution(); has && execution != p.s.Execution {
			shares = append(shares, "execution:"+execution)
		}
	}
	return shares
}

// insideTask reports a client that does not make a server shared: it has the
// task's role or a proven command and nothing shares it. A folder or the
// execution marker proves nothing, since every child of a pane inherits it.
func (c *candidate) insideTask() bool {
	if c.line.Role != "" {
		return true
	}
	return c.proven && len(c.ev.SharedWith) == 0
}

// ports is detector 2: the listeners of candidates and anything listening in
// the task's block. It runs before the process lines are final because a
// server with outside clients is shared, and so is the process behind it.
func (p *planner) ports(processes map[int]*candidate) []*candidate {
	inside := make(map[int]bool, len(processes))
	for pid, c := range processes {
		inside[pid] = c.insideTask()
	}
	type key struct{ pid, port int }
	seen := make(map[key]bool)
	var relevant []Listener
	for _, listener := range p.s.Listeners {
		k := key{listener.Pid, listener.Port}
		_, candidate := processes[listener.Pid]
		if seen[k] || !candidate && !p.s.Ports.contains(listener.Port) {
			continue
		}
		seen[k] = true
		relevant = append(relevant, listener)
	}
	users := make([][]string, len(relevant))
	for i, listener := range relevant {
		users[i] = p.listenerShares(listener, inside)
		if owner, ok := processes[listener.Pid]; ok {
			owner.ev.SharedWith = append(owner.ev.SharedWith, users[i]...)
		}
	}
	found := make([]*candidate, 0, len(relevant))
	for i, listener := range relevant {
		found = append(found, p.port(listener, processes[listener.Pid], users[i]))
	}
	return found
}

func (p *planner) port(listener Listener, owner *candidate, users []string) *candidate {
	c := newCandidate(KindPort, fmt.Sprintf("%d/%d", listener.Port, listener.Pid), ProcessIdentity(listener.Pid, time.Time{}))
	if owner != nil {
		c.line.Identity = owner.line.Identity
		c.ev.Recorded, c.ev.Unverified = owner.ev.Recorded, owner.ev.Unverified
		c.ev.SharedWith = append(c.ev.SharedWith, owner.ev.SharedWith...)
		c.ev.add(SignalCandidateListener)
	} else if process, ok := p.byPid[listener.Pid]; ok {
		c.line.Identity = ProcessIdentity(process.Pid, process.Start).String()
		p.older(&c.ev, process.Start, false)
	} else {
		c.ev.Unverified = true
	}
	if p.s.Ports.contains(listener.Port) {
		c.ev.add(SignalPortBlock)
	}
	c.ev.SharedWith = append(c.ev.SharedWith, users...)
	return c
}
