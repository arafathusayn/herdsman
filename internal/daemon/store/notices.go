package store

// NoticeRow is one notice document with the columns its queries read.
type NoticeRow struct {
	ID, Key, Execution, Repo, Route string
	Seq                             int64
	Kind, State, Doc                string
}

const noticeColumns = "id, key, execution_id, repo, route, seq, kind, state, doc"

func scanNotice(r Row) NoticeRow {
	return NoticeRow{ID: r.Text(0), Key: r.Text(1), Execution: r.Text(2), Repo: r.Text(3), Route: r.Text(4), Seq: r.Int64(5), Kind: r.Text(6), State: r.Text(7), Doc: r.Text(8)}
}

func noticesWhere(c Conn, where string, args ...any) ([]NoticeRow, error) {
	var out []NoticeRow
	err := c.Query("SELECT "+noticeColumns+" FROM notices WHERE "+where+" ORDER BY seq", args, func(r Row) error {
		out = append(out, scanNotice(r))
		return nil
	})
	return out, err
}

// NoticesOf lists an execution's notices in issue order.
func NoticesOf(c Conn, execution string) ([]NoticeRow, error) {
	return noticesWhere(c, "execution_id = ?", execution)
}

// RouteNotices lists a route's notices in issue order.
func RouteNotices(c Conn, repo, route string) ([]NoticeRow, error) {
	return noticesWhere(c, "repo = ? AND route = ?", repo, route)
}

// NoticeByID reads one notice by id.
func NoticeByID(c Conn, id string) (NoticeRow, bool, error) {
	rows, err := noticesWhere(c, "id = ?", id)
	if err != nil || len(rows) == 0 {
		return NoticeRow{}, false, err
	}
	return rows[0], true, nil
}

// NoticeByKey reads one notice by its idempotency key.
func NoticeByKey(c Conn, key string) (NoticeRow, bool, error) {
	rows, err := noticesWhere(c, "key = ?", key)
	if err != nil || len(rows) == 0 {
		return NoticeRow{}, false, err
	}
	return rows[0], true, nil
}

// PutNotice writes a notice document by id.
func PutNotice(c Conn, n NoticeRow) error {
	_, err := c.Exec(`INSERT INTO notices(`+noticeColumns+`) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET state = excluded.state, doc = excluded.doc`,
		n.ID, n.Key, n.Execution, n.Repo, n.Route, n.Seq, n.Kind, n.State, n.Doc)
	return err
}

// NextNoticeSeq is the sequence a new notice takes.
func NextNoticeSeq(c Conn) (int64, error) {
	var seq int64
	err := c.Query("SELECT coalesce(max(seq), 0) + 1 FROM notices", nil, func(r Row) error { seq = r.Int64(0); return nil })
	return seq, err
}

// NoticeWait is one wait id of an execution and the generation it was given.
type NoticeWait struct {
	Execution, WaitID string
	Generation        int64
	// Created is in milliseconds since the Unix epoch.
	Created int64
}

// NoticeWaits lists an execution's wait ids, newest generation first.
func NoticeWaits(c Conn, execution string) ([]NoticeWait, error) {
	var out []NoticeWait
	err := c.Query("SELECT execution_id, wait_id, generation, created FROM notice_waits WHERE execution_id = ? ORDER BY generation DESC", []any{execution}, func(r Row) error {
		out = append(out, NoticeWait{Execution: r.Text(0), WaitID: r.Text(1), Generation: r.Int64(2), Created: r.Int64(3)})
		return nil
	})
	return out, err
}

// PutNoticeWait records a wait id and keeps the newest keep of the execution.
func PutNoticeWait(c Conn, w NoticeWait, keep int) error {
	if _, err := c.Exec(`INSERT INTO notice_waits(execution_id, wait_id, generation, created) VALUES (?,?,?,?)
		ON CONFLICT(execution_id, wait_id) DO UPDATE SET generation = excluded.generation, created = excluded.created`,
		w.Execution, w.WaitID, w.Generation, w.Created); err != nil {
		return err
	}
	_, err := c.Exec(`DELETE FROM notice_waits WHERE execution_id = ? AND generation NOT IN
		(SELECT generation FROM notice_waits WHERE execution_id = ? ORDER BY generation DESC LIMIT ?)`, w.Execution, w.Execution, keep)
	return err
}

// NoticeRoute reads a route's state document.
func NoticeRoute(c Conn, repo, route string) (string, bool, error) {
	doc, found := "", false
	err := c.Query("SELECT doc FROM notice_routes WHERE repo = ? AND route = ?", []any{repo, route}, func(r Row) error {
		doc, found = r.Text(0), true
		return nil
	})
	return doc, found, err
}

// PutNoticeRoute writes a route's state document.
func PutNoticeRoute(c Conn, repo, route, doc string) error {
	_, err := c.Exec(`INSERT INTO notice_routes(repo, route, doc) VALUES (?,?,?) ON CONFLICT(repo, route) DO UPDATE SET doc = excluded.doc`, repo, route, doc)
	return err
}

// NoticeApproval reads an execution's approval document.
func NoticeApproval(c Conn, execution string) (string, bool, error) {
	doc, found := "", false
	err := c.Query("SELECT doc FROM notice_approvals WHERE execution_id = ?", []any{execution}, func(r Row) error {
		doc, found = r.Text(0), true
		return nil
	})
	return doc, found, err
}

// PutNoticeApproval writes an execution's approval document.
func PutNoticeApproval(c Conn, execution, doc string) error {
	_, err := c.Exec(`INSERT INTO notice_approvals(execution_id, doc) VALUES (?,?) ON CONFLICT(execution_id) DO UPDATE SET doc = excluded.doc`, execution, doc)
	return err
}

// DeleteNoticeApproval removes an execution's approval.
func DeleteNoticeApproval(c Conn, execution string) error {
	_, err := c.Exec("DELETE FROM notice_approvals WHERE execution_id = ?", execution)
	return err
}

// TypedSubmit is one person's typed submit.
type TypedSubmit struct {
	ID, Execution, State, Doc string
	Created, Updated          int64
}

// PutTypedSubmit writes a typed submit by id.
func PutTypedSubmit(c Conn, s TypedSubmit) error {
	_, err := c.Exec(`INSERT INTO typed_submits(id, execution_id, state, doc, created, updated) VALUES (?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET state = excluded.state, doc = excluded.doc, updated = excluded.updated`,
		s.ID, s.Execution, s.State, s.Doc, s.Created, s.Updated)
	return err
}

// TypedSubmits lists an execution's typed submits, or every one when
// execution is empty, oldest first.
func TypedSubmits(c Conn, execution string) ([]TypedSubmit, error) {
	q, args := "SELECT id, execution_id, state, doc, created, updated FROM typed_submits", []any{}
	if execution != "" {
		q, args = q+" WHERE execution_id = ?", []any{execution}
	}
	var out []TypedSubmit
	err := c.Query(q+" ORDER BY created, id", args, func(r Row) error {
		out = append(out, TypedSubmit{ID: r.Text(0), Execution: r.Text(1), State: r.Text(2), Doc: r.Text(3), Created: r.Int64(4), Updated: r.Int64(5)})
		return nil
	})
	return out, err
}
