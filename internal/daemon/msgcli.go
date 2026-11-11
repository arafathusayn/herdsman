package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/routedb"
	"github.com/arafathusayn/herdsman/internal/settings"
	"github.com/arafathusayn/herdsman/internal/statedir"
)

// MessageEnv is one run of a `herdsman message` command on the daemon
// path (6.2): its environment, output and clock.
type MessageEnv struct {
	Getenv  func(string) string
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
	JSON    bool
	Route   string // --route; "" takes HERDSMAN_ROUTE
	Now     func() time.Time
	Sleep   func(time.Duration)
	// Keychain reads keychain credentials (nil: the system's).
	Keychain Keychain
	// AttemptTimeout bounds one attempt of a keyed request (0: 10 s).
	AttemptTimeout time.Duration
}

// MessageHelp is the daemon path's grammar and exit codes.
const MessageHelp = `usage: herdsman message [--json] [--route <route>] <action> [args]
  (the daemon path: HERDSMAN_ROUTE or --route names the route; HERDSMAN_NAME your enrolled name)
  status                          the route's status (falls back to the files, read-only, when the daemon is down)
  wait [--after <cursor>] [--types <list>] [--deadline <duration>]
                                  wait for one event, print it with its cursor, exit
  read [claim]                    claim ready messages; print manifests, never bodies
  release <claim>                 release a fully acknowledged claim
  ack <claim> <id> <record>       acknowledge a claimed message after durable intake
  send <to> <file> <kind> [reply]
  hold <name> <reason> | unhold <name>
  enroll <name> adjutant|orchestrator   enroll this pane (herdr agent start) as a party
  authority                       RELAY or HUMAN
  resume-goal <name>              herdr-core: raise needs_attention goal_paused and the line a person submits; HELD
settings: DEADLINE (wait, default 270s) and HEALTH_GRACE (wait, default 30s) are durations
(seconds, or with an s, m, h or d suffix). A bad value exits 2 first.

exit codes:
  0   done (wait: an event or TICK; authority: RELAY)
  1   wait: HEALTH (daemon unreachable, version mismatch, resync, route removed); status: daemon down
  2   usage, an invalid setting, or a refusal (the reason on stderr)
  3   authority: HUMAN; human_boundary; resume-goal: HELD
  75  retryable: busy, route_unowned, herdr_unreachable, daemon not answering
`

func (e *MessageEnv) env(k string) string {
	if e.Getenv == nil {
		return os.Getenv(k)
	}
	return e.Getenv(k)
}

func (e *MessageEnv) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

func (e *MessageEnv) sleep(d time.Duration) {
	if e.Sleep == nil {
		time.Sleep(d)
		return
	}
	e.Sleep(d)
}

// fail prints a refusal in the command's form and returns its exit code.
func (e *MessageEnv) fail(action string, err error) int {
	if a, ok := AsAPIError(err); ok {
		if e.JSON {
			b, _ := encode(errorBody{Error: a})
			fmt.Fprintln(e.Stdout, string(b))
		}
		fmt.Fprintf(e.Stderr, "herdsman message %s: %s\n", action, a.Message)
		return ExitCode(a.Code)
	}
	if IsDown(err) {
		if other := e.otherHome(); other != "" {
			fmt.Fprintf(e.Stderr, "herdsman message %s: %s\n", action, other)
			return 75
		}
		fmt.Fprintf(e.Stderr, "herdsman message %s: %v\n", action, err)
		return 75
	}
	fmt.Fprintf(e.Stderr, "herdsman message %s: %v\n", action, err)
	return 2
}

// otherHome is the 3.6 diagnostic when this private home's daemon does
// not answer but a live daemon of this account uses another private home
// ("" otherwise).
func (e *MessageEnv) otherHome() string { return OtherHome(e.env) }

// OtherHome names the private home of a live daemon of this account when
// it is not the selected one ("" otherwise): the account lock, not its
// content, decides it is live. A lock that cannot be checked, or a held
// lock whose record is refused or names no private home, is no answer of
// "none": it names the file to inspect, so the caller fails closed.
func OtherHome(getenv func(string) string) string {
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	ph, err := routedb.PrivateHome("", getenv)
	if home == "" || err != nil || getenv(statedir.Env) != "" {
		// A daemon on an overridden state directory has its own lock, so
		// the account's regular daemon is never "the other one" for it.
		return ""
	}
	held, err := accountLockState(home)
	if err != nil {
		return err.Error()
	}
	if !held {
		return ""
	}
	l, err := readAccountLock(home)
	if err == nil && l.PrivateHome == "" {
		err = errors.New("no private_home")
	}
	if err != nil {
		return fmt.Sprintf("the account lock is held but its record is refused (%v); inspect %s", err, AccountLockPath(home))
	}
	if l.PrivateHome == ph {
		return ""
	}
	return "the running daemon uses " + l.PrivateHome + "; set HERDSMAN_PRIVATE_HOME"
}

func (e *MessageEnv) usage(msg string) int {
	fmt.Fprintf(e.Stderr, "herdsman message: %s\n%s", msg, MessageHelp)
	return 2
}

// client is the party's client: socket, pane, name and credential.
func (e *MessageEnv) client(route, name string) (*Client, string, error) {
	ph, err := routedb.PrivateHome("", e.env)
	if err != nil {
		return nil, "", err
	}
	c := NewClient(SocketPath(ph), e.Version)
	c.Pane, c.Name = e.env("HERDR_PANE_ID"), name
	rf, _ := ReadRoutes(ph)
	dir := rf.Routes[route].Inbox
	if dir != "" && name != "" {
		creds := map[string]credential{}
		if b, err := readSmall(filepath.Join(dir, "state", "credentials.json"), 64<<10); err == nil {
			_ = json.Unmarshal(b, &creds)
		}
		switch creds[name].Option {
		case "file":
			if b, err := readSmall(filepath.Join(dir, "state", "credentials", name), 256); err == nil {
				c.Credential = strings.TrimSpace(string(b))
			}
		case "keychain":
			kc := e.Keychain
			if kc == nil && runtime.GOOS == "darwin" {
				kc = SystemKeychain()
			}
			if kc != nil {
				if s, err := kc.Load("herdsman/"+route, name); err == nil {
					c.Credential = s
				}
			}
		}
	}
	return c, dir, nil
}

// RunMessage is `herdsman message <action>` on the daemon path.
func RunMessage(e *MessageEnv, args []string) int {
	route := e.Route
	if route == "" {
		route = e.env("HERDSMAN_ROUTE")
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	if args[0] == "help" || slices.ContainsFunc(args, func(a string) bool { return a == "-h" || a == "--help" }) {
		fmt.Fprint(e.Stdout, MessageHelp)
		return 0
	}
	action, rest := args[0], args[1:]
	if !validName(route) {
		return e.usage("HERDSMAN_ROUTE or --route must name the route")
	}
	me := e.env("HERDSMAN_NAME")
	if action == "enroll" {
		if len(rest) != 2 {
			return e.usage("enroll <name> adjutant|orchestrator")
		}
		me = rest[0]
	} else if me == "" {
		fmt.Fprintln(e.Stderr, "HERDSMAN_NAME: set the enrolled name from session configuration")
		return 1
	}
	c, dir, err := e.client(route, me)
	if err != nil {
		return e.fail(action, err)
	}
	defer c.Close()
	if action == "enroll" {
		c.Name = ""
	}
	ctx := context.Background()
	switch action {
	case "status":
		if len(rest) != 0 {
			return e.usage("status takes no arguments")
		}
		return e.status(ctx, c, route, dir)
	case "wait":
		return e.wait(c, route, me, rest)
	case "read":
		if len(rest) > 1 {
			return e.usage("read [claim]")
		}
		q := ClaimRequest{ReadKey: randomHex(16)}
		if len(rest) == 1 {
			q = ClaimRequest{Token: rest[0], ExtendKey: randomHex(16)}
		}
		var res ClaimResult
		if err := e.retry(ctx, func(ctx context.Context) error { var err error; res, err = c.Claim(ctx, route, q); return err }); err != nil {
			return e.fail(action, err)
		}
		e.line(obj("type", "claim", "token", res.Claim))
		for _, m := range res.Manifest {
			e.line(obj("type", "manifest", "id", m.ID, "from", m.From, "to", m.To, "kind", m.Kind, "time", m.Time,
				"reply_to", m.ReplyTo, "path", m.Path, "claim", res.Claim, "authority", "data-only"))
		}
		e.line(obj("type", "summary", "pending_new", res.Summary.PendingNew, "quarantine", res.Summary.Quarantine))
		return 0
	case "release":
		if len(rest) != 1 {
			return e.usage("release <claim>")
		}
		if err := c.Release(ctx, route, rest[0]); err != nil {
			return e.fail(action, err)
		}
		fmt.Fprintln(e.Stdout, `{"type":"released"}`)
		return 0
	case "ack":
		if len(rest) != 3 || !inbox.IDOK(rest[1]) {
			return e.usage("ack <claim> <id> <processing-record-file>")
		}
		if !inbox.Bounded("", rest[2]) {
			return e.fail(action, Errf("bad_request", "nonempty processing record required"))
		}
		rec, err := inbox.ReadBounded(rest[2], inbox.MaxSize)
		if err != nil || len(rec) == 0 {
			return e.fail(action, Errf("bad_request", "nonempty processing record required"))
		}
		var res AckResult
		q := AckRequest{ID: rest[1], Record: string(rec)}
		if err := e.retry(ctx, func(ctx context.Context) error {
			var err error
			res, err = c.Ack(ctx, route, rest[0], q)
			return err
		}); err != nil {
			return e.fail(action, err)
		}
		if e.JSON {
			e.line(obj("type", "acknowledged", "id", rest[1], "already", res.Already))
		} else {
			fmt.Fprintf(e.Stdout, "acknowledged %s\n", rest[1])
		}
		return 0
	case "send":
		if len(rest) != 3 && len(rest) != 4 {
			return e.usage("send <to> <file> <kind> [reply]")
		}
		q := SendRequest{To: rest[0], Kind: rest[2], Key: randomHex(16)}
		if len(rest) == 4 {
			q.ReplyTo = rest[3]
		}
		if !inbox.Bounded("", rest[1]) {
			return e.fail(action, Errf("bad_request", "body must be a bounded readable regular file"))
		}
		body, err := os.ReadFile(rest[1])
		if err != nil || !utf8.Valid(body) || len(body) > 4*inbox.MaxBody {
			return e.fail(action, Errf("bad_request", "body must be a bounded readable regular UTF-8 file"))
		}
		q.Body = string(body)
		var res SendResult
		if err := e.retry(ctx, func(ctx context.Context) error { var err error; res, err = c.Send(ctx, route, q); return err }); err != nil {
			return e.fail(action, err)
		}
		if e.JSON {
			e.line(obj("type", "queued", "id", res.ID, "seq", res.Seq))
		} else {
			fmt.Fprintf(e.Stdout, "%s queued; delivery requires receipt or status confirmation\n", res.ID)
		}
		return 0
	case "hold", "unhold":
		if (action == "hold" && len(rest) != 2) || (action == "unhold" && len(rest) != 1) {
			return e.usage("hold <name> <reason> | unhold <name>")
		}
		var res HoldResult
		var err error
		if action == "hold" {
			res, err = c.Hold(ctx, route, rest[0], rest[1])
		} else {
			res, err = c.Unhold(ctx, route, rest[0])
		}
		if err != nil {
			return e.fail(action, err)
		}
		switch {
		case e.JSON:
			e.line(obj("holds", res.Holds))
		case action == "hold":
			fmt.Fprintf(e.Stdout, "HOLD %s by %s\n", rest[0], me)
		case len(res.Holds) > 0:
			fmt.Fprintf(e.Stdout, "UNHOLD %s by %s; still held by: %s\n", rest[0], me, strings.Join(slices.Sorted(maps.Keys(res.Holds)), " "))
		default:
			fmt.Fprintf(e.Stdout, "UNHOLD %s by %s; no hold remains\n", rest[0], me)
		}
		return 0
	case "resume-goal":
		// On herdr-core nothing is typed: the daemon raises needs_attention
		// and the answer is HELD (exit 3), as the file-mode command's.
		if len(rest) != 1 {
			return e.usage("resume-goal <name>")
		}
		res, err := c.ResumeGoal(ctx, route, rest[0])
		if err != nil {
			return e.fail(action, err)
		}
		text := fmt.Sprintf("HELD %s: a person submits %s in %s's terminal (execution %s)", res.Reason, res.Line, rest[0], res.Execution)
		if e.JSON {
			e.line(obj("type", "resume", "status", res.Status, "reason", res.Reason, "line", res.Line, "execution", res.Execution, "text", text))
		} else {
			fmt.Fprintln(e.Stdout, text)
		}
		return 3
	case "enroll":
		if rest[1] != "adjutant" && rest[1] != "orchestrator" {
			return e.usage("enroll <name> adjutant|orchestrator")
		}
		sum, err := c.Enroll(ctx, route, EnrollRequest{Name: rest[0], Role: rest[1]})
		if err != nil {
			return e.fail(action, err)
		}
		if e.JSON {
			e.line(obj("pending_sha256", sum))
		} else {
			fmt.Fprintf(e.Stdout, "pending registry %s\n", sum)
		}
		return 0
	case "authority":
		if len(rest) != 0 {
			return e.usage("authority takes no arguments")
		}
		res, err := c.Authority(ctx, route)
		if err != nil {
			return e.fail(action, err)
		}
		if e.JSON {
			e.line(obj("authority", res.Authority, "reasons", res.Reasons))
		} else if res.Authority == "RELAY" {
			fmt.Fprintln(e.Stdout, inbox.RelayLine)
		} else {
			fmt.Fprintln(e.Stdout, inbox.HumanLine)
		}
		if res.Authority != "RELAY" {
			return 3
		}
		return 0
	}
	return e.usage("unknown action " + strconv.Quote(action))
}

func (e *MessageEnv) line(v any) {
	b, _ := encode(v)
	fmt.Fprintln(e.Stdout, string(b))
}

// retry repeats a keyed request after a lost connection or a retryable
// refusal inside one 60 s window, backing off 0.2 s doubling to 5 s (5.4).
// Every attempt and every wait is capped to what is left of the window,
// and no attempt starts after it.
func (e *MessageEnv) retry(ctx context.Context, fn func(context.Context) error) error {
	end := e.now().Add(retryWindow)
	wait := 200 * time.Millisecond
	per := e.AttemptTimeout
	if per == 0 {
		per = 10 * time.Second
	}
	for {
		actx, cancel := context.WithTimeout(ctx, min(per, end.Sub(e.now())))
		err := fn(actx)
		cancel()
		if err == nil {
			return nil
		}
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && !IsDown(err) {
			err = &DownError{Err: err}
		}
		a, isAPI := AsAPIError(err)
		retryable := IsDown(err) || (isAPI && a.Retryable && a.Code != "internal")
		left := end.Sub(e.now())
		if !retryable || left <= 0 {
			return err
		}
		e.sleep(min(wait, left))
		if !e.now().Before(end) {
			return err
		}
		wait = min(wait*2, 5*time.Second)
	}
}

// retryWindow is the keyed requests' retry window (5.4).
const retryWindow = 60 * time.Second

// status asks the daemon; when it does not answer within 1 s, it reads
// the files read-only and reports daemon=down with exit 1 (4.5).
func (e *MessageEnv) status(ctx context.Context, c *Client, route, dir string) int {
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	out, err := c.Status(cctx, route)
	cancel()
	code := 0
	if err != nil {
		if !IsDown(err) && !errors.Is(err, context.DeadlineExceeded) {
			if _, ok := AsAPIError(err); ok {
				return e.fail("status", err)
			}
		}
		ph, _ := routedb.PrivateHome("", e.env)
		rep, mode, serr := offlineStatus(route, dir, ph)
		if serr != nil {
			fmt.Fprintf(e.Stderr, "herdsman message status: %v\n", serr)
		}
		out = RouteStatus{Daemon: DaemonState{State: "down", HeartbeatAge: heartbeatAge(ph, e.now())}, StatusReport: rep}
		if b, err := readSmall(filepath.Join(ph, "daemon", "daemon.json"), 4096); err == nil {
			var info DaemonInfo
			if json.Unmarshal(b, &info) == nil {
				out.Daemon.Version = info.Version
			}
		}
		rt := &inbox.Root{Dir: dir}
		boxes := make([]string, 0, len(out.Boxes))
		for _, b := range out.Boxes {
			boxes = append(boxes, b.Name)
		}
		out.Messages, out.Alerts = []MessageHeader{}, []json.RawMessage{}
		if dir != "" && mode == routedb.Files {
			out.Messages, out.Alerts = statusDetail(rt, boxes, rt.AckValid)
		}
		code = 1
	}
	if e.JSON {
		e.line(out)
		return code
	}
	d := out.Daemon
	if d.State == "down" {
		age := "unknown"
		if d.HeartbeatAge != nil {
			age = strconv.FormatInt(*d.HeartbeatAge, 10) + "s"
		}
		fmt.Fprintf(e.Stdout, "daemon=down heartbeat_age=%s version=%s\n", age, d.Version)
	} else {
		fmt.Fprintf(e.Stdout, "daemon=up version=%s\n", d.Version)
	}
	s := out.StatusReport
	fmt.Fprintf(e.Stdout, "route %s epoch %s last_seq %d status %s\n", route, s.Epoch, s.LastSeq, s.Status)
	fmt.Fprintf(e.Stdout, "registry %s %s\n", s.Registry.State, s.Registry.PendingSHA256)
	for _, p := range s.Peers {
		fmt.Fprintf(e.Stdout, "peer %s role %s pane %s session %s\n", p.Name, p.Role, p.Pane, p.Session)
	}
	for _, b := range s.Boxes {
		fmt.Fprintf(e.Stdout, "box %s new %d ready %d cur %d unacked %d quarantine %d claim %v\n", b.Name, b.New, b.Ready, b.Cur, b.Unacked, b.Quarantine, b.Claim)
	}
	for _, h := range s.Holds {
		fmt.Fprintf(e.Stdout, "hold %s by %s\n", h.Target, strings.Join(h.Owners, " "))
	}
	return code
}

// offlineStatus is the status while the daemon does not answer (S16), by
// the inbox's mode: a contract 3 inbox's route database through the
// store's reader (Store; it creates nothing and a missing database is
// reported), an inbox with no owner record or one of contract 2 or lower
// from its files (Files). Any other owner record is refused and nothing
// is read (Refused). The message list and alerts are read from the files
// in Files mode only.
func offlineStatus(route, dir, privateHome string) (st StatusReport, mode routedb.Mode, err error) {
	if dir == "" {
		// No inbox (the route is in no routes.json): nothing to read, and
		// never the working folder's state/owner.json.
		return FileStatus(route, ""), routedb.Files, nil
	}
	mode, p, cerr := routedb.Classify(dir, privateHome, route)
	if mode == routedb.Files {
		return FileStatus(route, dir), mode, nil
	}
	st = StatusReport{Route: route, Inbox: dir, Status: "daemon-down", Flags: []string{"daemon-down"}, Peers: []PeerInfo{}, Boxes: []BoxCount{}, Holds: []HoldInfo{}}
	fail := func(status string, e error) (StatusReport, routedb.Mode, error) {
		st.Status, st.Flags = status, append(st.Flags, status)
		return st, mode, e
	}
	if mode == routedb.Refused {
		return fail("owner-refused", fmt.Errorf("owner record refused, nothing was read: %v", cerr))
	}
	s, err := routedb.Read(p)
	if errors.Is(err, os.ErrNotExist) {
		return fail("store-missing", fmt.Errorf("route database %s is missing; nothing was created (start the daemon)", p))
	}
	if err != nil {
		return fail("store-unreadable", fmt.Errorf("route database %s: %v", p, err))
	}
	st.Epoch, st.LastSeq = s.Epoch, s.LastSeq
	st.Registry = RegistryState{State: "pending", PendingSHA256: s.PendingSHA256}
	if s.Approved {
		st.Registry.State = "approved"
	}
	for _, x := range s.Peers {
		st.Peers = append(st.Peers, PeerInfo{Name: x.Name, Role: x.Role, Pane: x.Pane, Session: x.Session})
	}
	for _, b := range s.Boxes {
		st.Boxes = append(st.Boxes, BoxCount{Name: b.Name, New: b.New, Ready: b.Ready, Cur: b.Cur, Unacked: b.Unacked,
			Quarantine: b.Quarantine, Claim: b.Claim, Orphan: b.Orphan})
	}
	for _, h := range s.Holds {
		hi := HoldInfo{Target: h.Target, Owners: []string{}}
		for _, o := range h.Owners {
			hi.Owners = append(hi.Owners, o.Owner)
		}
		st.Holds = append(st.Holds, hi)
	}
	return st, mode, nil
}

// FileStatus reads a daemon-owned inbox's status from its files alone:
// read-only, no lock, no Herdr call, no bodies, no claim tokens; a bad
// mailbox is reported and skipped (4.5, D25). Saved holds are listed as
// the live reader lists them. The last sequence comes
// from a bounded tail of the journal.
func FileStatus(route, dir string) StatusReport {
	st := StatusReport{Route: route, Inbox: dir, Status: "daemon-down", Flags: []string{"daemon-down"}, Peers: []PeerInfo{}, Boxes: []BoxCount{}, Holds: []HoldInfo{}}
	if dir == "" {
		return st
	}
	if o, ok := ReadOwner(dir); ok {
		st.Epoch = o.Epoch
	}
	st.LastSeq = tailSeq(filepath.Join(dir, "state", "events", journalCurrent))
	rt := &inbox.Root{Dir: dir}
	if ab, ok := inbox.ReadStrict(dir, filepath.Join(dir, "peers.approved")); ok {
		st.Peers = peerInfos(ab)
	}
	if _, ok := rt.Registry(); ok {
		st.Registry.State = "approved"
	} else {
		st.Registry.State = "pending"
	}
	ents, _ := os.ReadDir(dir)
	for _, en := range ents {
		n := en.Name()
		if !inbox.NameOK(n) {
			continue
		}
		box := filepath.Join(dir, n)
		if en.Type()&os.ModeSymlink != 0 || !en.IsDir() {
			if en.Type()&os.ModeSymlink != 0 {
				st.Flags = append(st.Flags, "linked-mailbox:"+n)
			}
			continue
		}
		st.Boxes = append(st.Boxes, BoxCount{Name: n, New: inbox.CountState(filepath.Join(box, "new")), Ready: inbox.CountState(filepath.Join(box, "ready")),
			Cur: inbox.CountState(filepath.Join(box, "cur")), Quarantine: inbox.CountState(filepath.Join(box, "quarantine")),
			Unacked: len(rt.Unacked(box)), Claim: rt.ClaimValid(box), Orphan: !hasPeer(st.Peers, n)})
	}
	st.Holds = append(st.Holds, holdInfos(dir)...)
	return st
}

// tailSeq is the last record's seq from the journal's last 4 KiB.
func tailSeq(p string) int64 {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	off := max(st.Size()-4096, 0)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return 0
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r Record
		if json.Unmarshal([]byte(lines[i]), &r) == nil && r.Seq > 0 {
			return r.Seq
		}
	}
	return 0
}

// parseDuration is the duration setting (A25) through file mode's parser,
// settings.Duration: plain seconds or with an s, m, h or d suffix, blanks
// around it allowed, clamped to lo..hi. An empty value is refused, as the
// callers have no default to fall back to.
func parseDuration(v string, lo, hi time.Duration) (time.Duration, error) {
	d, err := settings.Duration("", v, 0, hi)
	if v == "" || err != nil {
		return 0, errors.New("not a duration (seconds, or with an s, m, h or d suffix)")
	}
	return max(d, lo), nil
}

// DefaultWaitDeadline is wait's TICK deadline: 4 minutes 30 seconds, so
// a harness woken by the TICK still has a warm prompt cache (the user's
// decision, design section 2).
const DefaultWaitDeadline = 270 * time.Second

// waitTypes are the event types wait exits on (6.1).
var waitTypes = []string{"message", "receipt", "notice", "health", "pair", "question", "card"}

// wait is `herdsman message wait` (6.1): one event, printed with its
// cursor, then exit.
func (e *MessageEnv) wait(c *Client, route, me string, args []string) int {
	after, types := "", slices.Clone(waitTypes)
	deadlineText := e.env("DEADLINE")
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return e.usage("wait [--after <cursor>] [--types <list>] [--deadline <duration>]")
		}
		switch args[i] {
		case "--after":
			after = args[i+1]
			if _, _, ok := parseCursor(after); !ok {
				return e.usage("--after takes a cursor <epoch>-<seq>")
			}
		case "--types":
			types = strings.Split(args[i+1], ",")
			for _, t := range types {
				if !slices.Contains(waitTypes, t) {
					return e.usage("--types lists " + strings.Join(waitTypes, ","))
				}
			}
		case "--deadline":
			deadlineText = args[i+1]
		default:
			return e.usage("unknown wait option " + strconv.Quote(args[i]))
		}
		i++
	}
	deadline := DefaultWaitDeadline
	if deadlineText != "" {
		d, err := parseDuration(deadlineText, time.Second, 24*time.Hour)
		if err != nil {
			fmt.Fprintf(e.Stderr, "herdsman message wait: DEADLINE: %v (got %q)\n", err, deadlineText)
			return 2
		}
		deadline = d
	}
	grace := 30 * time.Second
	if v := e.env("HEALTH_GRACE"); v != "" {
		d, err := parseDuration(v, time.Second, 24*time.Hour)
		if err != nil {
			fmt.Fprintf(e.Stderr, "herdsman message wait: HEALTH_GRACE: %v (got %q)\n", err, v)
			return 2
		}
		grace = d
	}
	w := &waiter{e: e, c: c, route: route, me: me, cursor: after, types: types, grace: grace}
	return w.run(deadline)
}

// stableStream is how long a stream must stay open before its end resets
// the reconnect backoff.
const stableStream = 5 * time.Second

// waiter is one wait's state: the cursor it resumes from.
type waiter struct {
	e      *MessageEnv
	c      *Client
	route  string
	me     string
	cursor string
	types  []string
	grace  time.Duration
}

func (w *waiter) health(text string, j any) int {
	if w.e.JSON {
		w.e.line(j)
	} else {
		fmt.Fprintln(w.e.Stdout, text)
	}
	return 1
}

func (w *waiter) run(deadline time.Duration) int {
	end := w.e.now().Add(deadline)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(deadline))
	defer cancel()
	var downSince time.Time
	backoff := 200 * time.Millisecond
	for {
		if !w.e.now().Before(end) || ctx.Err() != nil {
			return w.tick()
		}
		// HEALTH_GRACE bounds the opening (hello and the stream's answer);
		// an open stream then stays idle as long as the deadline allows.
		attempt := w.e.now()
		actx, acancel := context.WithCancel(ctx)
		timer := time.AfterFunc(w.grace, acancel)
		ev, err := w.c.Events(actx, w.route, w.cursor)
		if !timer.Stop() && err == nil {
			ev.Close()
			err = &DownError{Err: errors.New("no answer within HEALTH_GRACE")}
		}
		if err != nil {
			acancel()
			if ctx.Err() != nil {
				return w.tick()
			}
			if a, ok := AsAPIError(err); ok {
				if strings.HasPrefix(a.Code, "version_") {
					return w.health("HEALTH version-mismatch: "+a.Message, obj("type", "health", "flags", []string{"version-mismatch"}, "reason", a.Message))
				}
				if !a.Retryable {
					return w.e.fail("wait", err)
				}
			}
			if downSince.IsZero() {
				downSince = attempt
			}
			if w.e.now().Sub(downSince) >= w.grace {
				reason := "the daemon did not answer"
				if other := w.e.otherHome(); other != "" {
					reason = other
				}
				return w.health("HEALTH daemon-unreachable", obj("type", "health", "flags", []string{"daemon-unreachable"}, "reason", reason))
			}
			w.e.sleep(backoff)
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		code, done, frames := w.read(ev)
		ev.Close()
		acancel()
		if done {
			return code
		}
		// A stream that ended is reopened after the backoff too; only a
		// stream that stayed open for stableStream resets it, so a daemon
		// that answers shutdown and closes at once is not retried in a
		// tight loop (6.1).
		if frames > 0 {
			downSince = time.Time{}
		}
		if w.e.now().Sub(attempt) >= stableStream {
			backoff = 200 * time.Millisecond
		}
		w.e.sleep(backoff)
		backoff = min(backoff*2, 5*time.Second)
	}
}

func (w *waiter) tick() int {
	if w.e.JSON {
		w.e.line(obj("type", "tick"))
	} else {
		fmt.Fprintln(w.e.Stdout, "TICK message-wait deadline")
	}
	return 0
}

// read handles one connection's frames; done is false to reconnect, and
// n counts the frames the connection delivered.
func (w *waiter) read(ev *EventReader) (code int, done bool, n int) {
	code, done = w.frames(ev, &n)
	return code, done, n
}

func (w *waiter) frames(ev *EventReader, n *int) (int, bool) {
	for {
		f, err := ev.Next()
		if err != nil {
			return 0, false
		}
		*n++
		var data Event
		if json.Unmarshal(f.Data, &data) != nil {
			continue
		}
		switch f.Event {
		case "shutdown":
			return 0, false
		case "route_removed":
			return w.health("HEALTH route-removed", obj("type", "health", "flags", []string{"route-removed"}, "reason", "route removed")), true
		case "reset":
			var last int64
			if data.LastSeq != nil {
				last = *data.LastSeq
			}
			cur := data.Epoch + "-" + strconv.FormatInt(last, 10)
			return w.health("HEALTH resync: "+data.Reason+" cursor "+cur,
				obj("type", "health", "flags", []string{"resync"}, "reason", data.Reason, "cursor", cur)), true
		case "snapshot":
			w.cursor = f.ID
			if len(data.Unread) > 0 && slices.Contains(w.types, "message") {
				if w.e.JSON {
					w.e.line(obj("type", "message", "id", data.Unread[0], "to", w.me, "unread", data.Unread, "cursor", f.ID))
				} else {
					fmt.Fprintf(w.e.Stdout, "MESSAGE %s %s cursor %s: run herdsman message read\n", w.me, data.Unread[0], f.ID)
				}
				return 0, true
			}
			continue
		}
		if f.ID != "" {
			w.cursor = f.ID
		}
		if !slices.Contains(w.types, f.Event) {
			continue
		}
		if w.e.JSON {
			var m map[string]any
			_ = json.Unmarshal(f.Data, &m)
			m["cursor"] = f.ID
			w.e.line(m)
		} else {
			fmt.Fprintln(w.e.Stdout, eventLine(w.me, f.ID, data))
		}
		if f.Event == "health" {
			return 1, true
		}
		return 0, true
	}
}

// eventLine is wait's text line for an event (6.1); callers parse the
// first word.
func eventLine(me, cursor string, ev Event) string {
	switch ev.Type {
	case "message":
		return fmt.Sprintf("MESSAGE %s %s from %s kind %s cursor %s: run herdsman message read", me, ev.ID, ev.From, ev.Kind, cursor)
	case "receipt":
		return fmt.Sprintf("RECEIPT %s by %s cursor %s", ev.ID, ev.By, cursor)
	case "notice":
		return fmt.Sprintf("NOTICE %s %s cursor %s: inspect herdsman message status", ev.ID, ev.State, cursor)
	case "health":
		flags := "ok"
		if len(ev.Flags) > 0 {
			flags = strings.Join(ev.Flags, ",")
		}
		return fmt.Sprintf("HEALTH %s cursor %s", flags, cursor)
	case "pair":
		return fmt.Sprintf("PAIR %s cursor %s", ev.State, cursor)
	}
	return fmt.Sprintf("%s cursor %s", strings.ToUpper(ev.Type), cursor)
}
