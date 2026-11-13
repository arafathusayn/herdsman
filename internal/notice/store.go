package notice

import (
	"encoding/json"
	"slices"
	"sync"
)

// Store is the durable home of notices, waits, route states and approvals.
// Every change the service makes in one call happens in one Tx, so a daemon
// restart finds either all of it or none.
type Store interface {
	Tx(func(Tx) error) error
}

// Tx is one transaction of a Store.
type Tx interface {
	// Notices lists an execution's notices in issue order.
	Notices(execution string) ([]Notice, error)
	// RouteNotices lists a route's notices in issue order.
	RouteNotices(repo, route string) ([]Notice, error)
	NoticeByID(id string) (Notice, bool, error)
	NoticeByKey(key string) (Notice, bool, error)
	// PutNotice writes a notice by id; a new one takes the next sequence.
	PutNotice(n Notice) error
	NextSeq() (int64, error)

	Waits(execution string) ([]WaitRecord, error)
	PutWait(w WaitRecord) error

	Route(repo, route string) (RouteState, bool, error)
	PutRoute(r RouteState) error

	Approval(execution string) (Approval, bool, error)
	PutApproval(a Approval) error
	DeleteApproval(execution string) error
}

// MemStore keeps everything in memory, as documents, so that a test sees the
// same copies a database would give. Tests use it.
type MemStore struct {
	mu        sync.Mutex
	notices   map[string][]byte
	order     []string
	waits     map[string][]WaitRecord
	routes    map[string]RouteState
	approvals map[string]Approval
	seq       int64
}

// NewMemStore is an empty store.
func NewMemStore() *MemStore {
	return &MemStore{notices: map[string][]byte{}, waits: map[string][]WaitRecord{}, routes: map[string]RouteState{}, approvals: map[string]Approval{}}
}

// Tx implements Store. A failed transaction leaves nothing behind.
func (m *MemStore) Tx(fn func(Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.clone()
	if err := fn(memTx{m}); err != nil {
		m.restore(snap)
		return err
	}
	return nil
}

type memSnap struct {
	notices   map[string][]byte
	order     []string
	waits     map[string][]WaitRecord
	routes    map[string]RouteState
	approvals map[string]Approval
	seq       int64
}

func (m *MemStore) clone() memSnap {
	s := memSnap{notices: map[string][]byte{}, order: slices.Clone(m.order), waits: map[string][]WaitRecord{}, routes: map[string]RouteState{}, approvals: map[string]Approval{}, seq: m.seq}
	for k, v := range m.notices {
		s.notices[k] = v
	}
	for k, v := range m.waits {
		s.waits[k] = slices.Clone(v)
	}
	for k, v := range m.routes {
		s.routes[k] = v
	}
	for k, v := range m.approvals {
		s.approvals[k] = v
	}
	return s
}

func (m *MemStore) restore(s memSnap) {
	m.notices, m.order, m.waits, m.routes, m.approvals, m.seq = s.notices, s.order, s.waits, s.routes, s.approvals, s.seq
}

type memTx struct{ m *MemStore }

func (t memTx) all() []Notice {
	out := make([]Notice, 0, len(t.m.order))
	for _, id := range t.m.order {
		var n Notice
		_ = json.Unmarshal(t.m.notices[id], &n)
		out = append(out, n)
	}
	slices.SortStableFunc(out, func(a, b Notice) int { return int(a.Seq - b.Seq) })
	return out
}

func (t memTx) Notices(execution string) ([]Notice, error) {
	var out []Notice
	for _, n := range t.all() {
		if n.Execution == execution {
			out = append(out, n)
		}
	}
	return out, nil
}

func (t memTx) RouteNotices(repo, route string) ([]Notice, error) {
	var out []Notice
	for _, n := range t.all() {
		if n.Repo == repo && n.Route == route {
			out = append(out, n)
		}
	}
	return out, nil
}

func (t memTx) NoticeByID(id string) (Notice, bool, error) {
	b, ok := t.m.notices[id]
	if !ok {
		return Notice{}, false, nil
	}
	var n Notice
	err := json.Unmarshal(b, &n)
	return n, err == nil, err
}

func (t memTx) NoticeByKey(key string) (Notice, bool, error) {
	for _, n := range t.all() {
		if n.Key == key {
			return n, true, nil
		}
	}
	return Notice{}, false, nil
}

func (t memTx) PutNotice(n Notice) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if _, ok := t.m.notices[n.ID]; !ok {
		t.m.order = append(t.m.order, n.ID)
	}
	t.m.notices[n.ID] = b
	return nil
}

func (t memTx) NextSeq() (int64, error) {
	t.m.seq++
	return t.m.seq, nil
}

func (t memTx) Waits(execution string) ([]WaitRecord, error) {
	return slices.Clone(t.m.waits[execution]), nil
}

func (t memTx) PutWait(w WaitRecord) error {
	list := t.m.waits[w.Execution]
	for i, x := range list {
		if x.WaitID == w.WaitID {
			list[i] = w
			return nil
		}
	}
	list = append(list, w)
	slices.SortFunc(list, func(a, b WaitRecord) int { return int(b.Generation - a.Generation) })
	if len(list) > KeepWaitRecords {
		list = list[:KeepWaitRecords]
	}
	t.m.waits[w.Execution] = list
	return nil
}

func (t memTx) Route(repo, route string) (RouteState, bool, error) {
	r, ok := t.m.routes[repo+"\x00"+route]
	return r, ok, nil
}

func (t memTx) PutRoute(r RouteState) error {
	t.m.routes[r.Repo+"\x00"+r.Route] = r
	return nil
}

func (t memTx) Approval(execution string) (Approval, bool, error) {
	a, ok := t.m.approvals[execution]
	return a, ok, nil
}

func (t memTx) PutApproval(a Approval) error {
	t.m.approvals[a.Execution] = a
	return nil
}

func (t memTx) DeleteApproval(execution string) error {
	delete(t.m.approvals, execution)
	return nil
}
