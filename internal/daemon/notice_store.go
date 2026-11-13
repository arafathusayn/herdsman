package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// noticeStore keeps the notice domain in the account store, beside the
// execution records and the operation journal, so a daemon restart finds
// every notice, claim, wait owner and route state as it was.
type noticeStore struct{ d *Daemon }

// Tx implements notice.Store: one store transaction under the engine's lock.
func (s noticeStore) Tx(fn func(notice.Tx) error) error {
	return s.d.ops.Read(func(c store.Conn) error {
		return c.Tx(func(c store.Conn) error { return fn(noticeTx{c}) })
	})
}

type noticeTx struct{ c store.Conn }

func decodeNotices(rows []store.NoticeRow, err error) ([]notice.Notice, error) {
	if err != nil {
		return nil, err
	}
	out := make([]notice.Notice, 0, len(rows))
	for _, r := range rows {
		var n notice.Notice
		if err := json.Unmarshal([]byte(r.Doc), &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func decodeNotice(r store.NoticeRow, found bool, err error) (notice.Notice, bool, error) {
	if err != nil || !found {
		return notice.Notice{}, false, err
	}
	var n notice.Notice
	err = json.Unmarshal([]byte(r.Doc), &n)
	return n, err == nil, err
}

func (t noticeTx) Notices(execution string) ([]notice.Notice, error) {
	return decodeNotices(store.NoticesOf(t.c, execution))
}

func (t noticeTx) RouteNotices(repo, route string) ([]notice.Notice, error) {
	return decodeNotices(store.RouteNotices(t.c, repo, route))
}

func (t noticeTx) NoticeByID(id string) (notice.Notice, bool, error) {
	return decodeNotice(store.NoticeByID(t.c, id))
}

func (t noticeTx) NoticeByKey(key string) (notice.Notice, bool, error) {
	return decodeNotice(store.NoticeByKey(t.c, key))
}

func (t noticeTx) PutNotice(n notice.Notice) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return store.PutNotice(t.c, store.NoticeRow{ID: n.ID, Key: n.Key, Execution: n.Execution, Repo: n.Repo, Route: n.Route, Seq: n.Seq, Kind: n.Kind, State: n.State, Doc: string(b)})
}

func (t noticeTx) NextSeq() (int64, error) { return store.NextNoticeSeq(t.c) }

func (t noticeTx) Waits(execution string) ([]notice.WaitRecord, error) {
	rows, err := store.NoticeWaits(t.c, execution)
	if err != nil {
		return nil, err
	}
	out := make([]notice.WaitRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, notice.WaitRecord{Execution: r.Execution, WaitID: r.WaitID, Generation: r.Generation, Created: time.UnixMilli(r.Created)})
	}
	return out, nil
}

func (t noticeTx) PutWait(w notice.WaitRecord) error {
	return store.PutNoticeWait(t.c, store.NoticeWait{Execution: w.Execution, WaitID: w.WaitID, Generation: w.Generation, Created: w.Created.UnixMilli()}, notice.KeepWaitRecords)
}

func (t noticeTx) Route(repo, route string) (notice.RouteState, bool, error) {
	doc, found, err := store.NoticeRoute(t.c, repo, route)
	if err != nil || !found {
		return notice.RouteState{Repo: repo, Route: route}, false, err
	}
	var r notice.RouteState
	err = json.Unmarshal([]byte(doc), &r)
	return r, err == nil, err
}

func (t noticeTx) PutRoute(r notice.RouteState) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return store.PutNoticeRoute(t.c, r.Repo, r.Route, string(b))
}

func (t noticeTx) Approval(execution string) (notice.Approval, bool, error) {
	doc, found, err := store.NoticeApproval(t.c, execution)
	if err != nil || !found {
		return notice.Approval{}, false, err
	}
	var a notice.Approval
	err = json.Unmarshal([]byte(doc), &a)
	return a, err == nil, err
}

func (t noticeTx) PutApproval(a notice.Approval) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return store.PutNoticeApproval(t.c, a.Execution, string(b))
}

func (t noticeTx) DeleteApproval(execution string) error {
	return store.DeleteNoticeApproval(t.c, execution)
}

// noticeExecutions reads executions for the notice gates from the account
// store's records.
type noticeExecutions struct{ d *Daemon }

// Execution implements notice.Executions.
func (x noticeExecutions) Execution(_ context.Context, id string) (runtime.Execution, bool, error) {
	var row store.Execution
	found := false
	err := x.d.ops.Read(func(c store.Conn) error {
		var err error
		row, found, err = store.ExecutionByID(c, id)
		return err
	})
	if err != nil || !found {
		return runtime.Execution{}, false, err
	}
	return executionOf(row), true, nil
}

// Running implements notice.Executions.
func (x noticeExecutions) Running(ctx context.Context) ([]runtime.Execution, error) {
	all, err := storeRecords(x).List(ctx, "", "")
	if err != nil {
		return nil, err
	}
	var out []runtime.Execution
	for _, e := range all {
		if e.Backend == runtime.BackendCore && e.Desired == runtime.DesiredRunning {
			out = append(out, e)
		}
	}
	return out, nil
}
