// Package ghtest is a fake `gh` for tests. It models the part of GitHub the
// landing backend uses, and it misbehaves on request. The fake runs as a real
// process, so a test exercises the same pipes, exit codes and kills as `gh`.
//
// A test package calls Main first thing in TestMain; New writes a `gh` script
// that runs the test binary again, and Main turns that run into a client of the
// Fake held by the test.
package ghtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const envAddr = "HERDSMAN_GHTEST_ADDR"

var exit = os.Exit

// Main makes this process the fake `gh` when the script started it, and returns otherwise.
func Main() {
	addr := os.Getenv(envAddr)
	if addr == "" {
		return
	}
	exit(relay(addr, os.Args[1:], os.Getenv("GH_TOKEN"), os.Stdin, os.Stdout, os.Stderr))
}

type wireRequest struct {
	Args  []string
	Stdin []byte
	Token string
}

type frame struct {
	Out  []byte
	Err  []byte
	Exit int
	Done bool
}

// relay sends one invocation to the Fake and copies its answer as it arrives.
func relay(addr string, args []string, token string, in io.Reader, out, errw io.Writer) int {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintln(errw, "ghtest:", err)
		return 1
	}
	defer conn.Close()
	stdin, err := io.ReadAll(in)
	if err == nil {
		err = json.NewEncoder(conn).Encode(wireRequest{Args: args, Stdin: stdin, Token: token})
	}
	if err != nil {
		fmt.Fprintln(errw, "ghtest:", err)
		return 1
	}
	dec := json.NewDecoder(conn)
	for {
		var f frame
		if err := dec.Decode(&f); err != nil {
			fmt.Fprintln(errw, "ghtest:", err)
			return 1
		}
		out.Write(f.Out)
		errw.Write(f.Err)
		if f.Done {
			return f.Exit
		}
	}
}

// PR is a pull request in the fake. Merged implies State "closed".
type PR struct {
	Number         int
	State          string
	Merged         bool
	Draft          bool
	HeadRef        string
	HeadSHA        string
	BaseRef        string
	MergeCommit    string
	MergeableState string
}

// Call is one recorded invocation. Path keeps its escaping and has no query.
type Call struct {
	Args   []string
	Method string
	Path   string
	Query  url.Values
	Body   []byte
	Token  string
}

// Fault replaces the answer to the first matching call, or to every one when
// Always is set. With Apply the fake does the work first, so the service acted
// and only its answer went wrong.
type Fault struct {
	Method string
	Path   string // a substring of the call's path
	Page   int    // when set, only this page of a paged read
	Always bool

	Before func() // runs before the call is handled; the fake is not locked
	Apply  bool

	Lost   bool   // exits 1 with no output, like a dropped connection
	Hang   bool   // writes half of the answer, then stalls until the client dies
	Status int    // answers with this status and Body
	Body   string // used with Status
}

type mergeRequest struct {
	number   int
	uuid     string
	head     string
	method   string
	action   string
	status   string
	sha      string
	message  string
	bypassed bool
}

// Fake is the service's state. Change its exported fields through Configure.
type Fake struct {
	Repo       string
	Rulesets   map[string][]map[string]any // by branch, as the rules API answers
	Protection map[string]bool             // by branch, the strict flag; absent means 404
	CheckRuns  map[string][]map[string]any // by commit
	Statuses   map[string][]map[string]any // by commit
	Token      string                      // what `gh auth token` prints; empty fails

	// Ticks is how many reads pass before an accepted merge finishes.
	Ticks int
	// Hold keeps accepted merges pending until Release.
	Hold bool
	// FailMerge makes accepted merges end as failed with this message.
	FailMerge string

	mu       sync.Mutex
	prs      map[int]*PR
	requests map[string]*mergeRequest
	order    []*mergeRequest
	merges   map[int]int
	faults   []*Fault
	calls    []Call
	serial   int
	ticks    int

	bin string
	ctx context.Context
}

// New starts a fake for repo, stopped when the test ends.
func New(t testing.TB, repo string) *Fake {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &Fake{
		Repo: repo, Rulesets: map[string][]map[string]any{}, Protection: map[string]bool{},
		CheckRuns: map[string][]map[string]any{}, Statuses: map[string][]map[string]any{}, Token: "fake-token",
		prs: map[int]*PR{}, requests: map[string]*mergeRequest{}, merges: map[int]int{}, serial: 100, ctx: ctx,
		bin: filepath.Join(t.TempDir(), "gh"),
	}
	script := fmt.Sprintf("#!/bin/sh\n%s=%s exec %s \"$@\"\n", envAddr, shellQuote(listener.Addr().String()), shellQuote(self))
	if err := os.WriteFile(f.bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				f.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		wg.Wait()
	})
	return f
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Bin is the path of the fake `gh` executable.
func (f *Fake) Bin() string { return f.bin }

// Configure changes the exported fields under the fake's lock. Requests arrive
// on other goroutines, so a test sets them through this.
func (f *Fake) Configure(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change()
}

// AddPR adds a pull request. An empty State means open.
func (f *Fake) AddPR(pr PR) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pr.State == "" {
		pr.State = "open"
	}
	f.prs[pr.Number] = &pr
}

// PushHead moves a pull request's head, as a push would.
func (f *Fake) PushHead(number int, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs[number].HeadSHA = sha
}

// Retarget changes a pull request's base, as an edit would.
func (f *Fake) Retarget(number int, base string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs[number].BaseRef = base
}

// MergeNow merges a pull request outside any request, as another client would.
func (f *Fake) MergeNow(number int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.land(f.prs[number])
}

// Close closes a pull request without merging it.
func (f *Fake) Close(number int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs[number].State = "closed"
}

// Release lets held merges finish on the next read.
func (f *Fake) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Hold = false
}

// Pending records a request that another client made, still pending.
func (f *Fake) Pending(number int, uuid, head, method, action string, bypass bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.track(&mergeRequest{number: number, uuid: uuid, head: head, method: method, action: action, status: "pending", bypassed: bypass})
}

// Inject adds a fault.
func (f *Fake) Inject(fault Fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, &fault)
}

// Merges counts how many times a pull request actually merged.
func (f *Fake) Merges(number int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.merges[number]
}

// Count counts the calls with this method whose path contains the substring.
func (f *Fake) Count(method, path string) int {
	n := 0
	for _, c := range f.Calls() {
		if c.Method == method && strings.Contains(c.Path, path) {
			n++
		}
	}
	return n
}

// Calls returns every call so far.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type result struct {
	stdout string
	stderr string
	exit   int
	hang   bool
}

func (f *Fake) serve(conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(f.ctx, func() { conn.Close() })
	defer stop()
	var req wireRequest
	if json.NewDecoder(conn).Decode(&req) != nil {
		return
	}
	res := f.dispatch(req)
	enc := json.NewEncoder(conn)
	if res.stdout != "" {
		enc.Encode(frame{Out: []byte(res.stdout)})
	}
	if res.hang {
		conn.Read(make([]byte, 1))
		return
	}
	enc.Encode(frame{Err: []byte(res.stderr), Exit: res.exit, Done: true})
}

func (f *Fake) dispatch(req wireRequest) result {
	call, parsed := parseArgs(req)
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if len(req.Args) >= 2 && req.Args[0] == "auth" && req.Args[1] == "token" {
		return f.authToken()
	}
	if !parsed {
		return result{stderr: fmt.Sprintf("ghtest: unsupported arguments %q\n", req.Args), exit: 2}
	}
	fault := f.takeFault(call)
	if fault != nil && fault.Before != nil {
		fault.Before()
	}
	var res result
	if fault == nil || fault.Apply {
		f.mu.Lock()
		res = f.respond(call)
		f.mu.Unlock()
	}
	switch {
	case fault == nil:
	case fault.Lost:
		return result{stderr: "gh: connection reset by peer\n", exit: 1}
	case fault.Hang:
		return result{stdout: res.stdout[:len(res.stdout)/2], hang: true}
	case fault.Status != 0:
		return reply(fault.Status, fault.Body)
	}
	return res
}

func (f *Fake) authToken() result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Token == "" {
		return result{stderr: "no oauth token found\n", exit: 1}
	}
	return result{stdout: f.Token + "\n"}
}

// parseArgs reads the `gh api` form the backend uses. Anything else is unsupported,
// so a new flag cannot pass unnoticed.
func parseArgs(req wireRequest) (Call, bool) {
	call := Call{Args: req.Args, Method: "GET", Body: req.Stdin, Token: req.Token}
	if len(req.Args) == 0 || req.Args[0] != "api" {
		return call, false
	}
	endpoint := ""
	for i := 1; i < len(req.Args); i++ {
		switch arg := req.Args[i]; {
		case arg == "--include" || arg == "-i":
		case (arg == "-X" || arg == "--input") && i+1 < len(req.Args):
			i++
			if arg == "-X" {
				call.Method = req.Args[i]
			}
		case strings.HasPrefix(arg, "-") || endpoint != "":
			return call, false
		default:
			endpoint = arg
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil || endpoint == "" {
		return call, false
	}
	call.Path, call.Query = u.EscapedPath(), u.Query()
	return call, true
}

func (f *Fake) takeFault(call Call) *Fault {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, fault := range f.faults {
		if (fault.Method != "" && fault.Method != call.Method) || !strings.Contains(call.Path, fault.Path) {
			continue
		}
		if fault.Page != 0 && call.Query.Get("page") != strconv.Itoa(fault.Page) {
			continue
		}
		if !fault.Always {
			f.faults = slices.Delete(f.faults, i, i+1)
		}
		return fault
	}
	return nil
}

func reply(status int, body string) result {
	res := result{stdout: fmt.Sprintf("HTTP/2.0 %d Fake\r\nContent-Type: application/json\r\n\r\n%s", status, body)}
	if status >= 400 {
		res.stderr, res.exit = fmt.Sprintf("gh: fake (HTTP %d)\n", status), 1
	}
	return res
}

func replyJSON(status int, v any) result {
	body, _ := json.Marshal(v) // only maps of plain values reach here
	return reply(status, string(body))
}

func notFound(message string) result { return replyJSON(404, map[string]any{"message": message}) }

// respond answers one API call; the caller holds the lock.
func (f *Fake) respond(call Call) result {
	segments := strings.Split(strings.TrimPrefix(call.Path, "/"), "/")
	prefix := append([]string{"repos"}, strings.Split(f.Repo, "/")...)
	if len(segments) < 3 || !slices.Equal(segments[:3], prefix) {
		return notFound("no such repository")
	}
	rest := segments[3:]
	for i, s := range rest {
		if plain, err := url.PathUnescape(s); err == nil {
			rest[i] = plain
		}
	}
	route := call.Method + " " + strings.Join(routeShape(rest), "/")
	switch route {
	case "GET pulls":
		return f.listPRs(call.Query)
	case "POST pulls":
		return f.createPR(call.Body)
	case "GET pulls/*":
		return f.getPR(rest[1])
	case "PUT pulls/*/merge-async":
		return f.requestMerge(rest[1], call.Body)
	case "GET pulls/*/merge-async/*":
		return f.getRequest(rest[3])
	case "GET rules/branches/*":
		return replyJSON(200, page(f.Rulesets[rest[2]], call.Query))
	case "GET branches/*/protection/required_status_checks":
		return f.protection(rest[1])
	case "GET commits/*/check-runs":
		return replyJSON(200, map[string]any{"total_count": len(f.CheckRuns[rest[1]]), "check_runs": page(f.CheckRuns[rest[1]], call.Query)})
	case "GET commits/*/status":
		return replyJSON(200, map[string]any{"total_count": len(f.Statuses[rest[1]]), "statuses": page(f.Statuses[rest[1]], call.Query)})
	}
	return notFound("no such route: " + route)
}

// routeShape replaces each path's variable segments with * for matching.
func routeShape(rest []string) []string {
	shape := slices.Clone(rest)
	switch {
	case len(rest) >= 2 && (rest[0] == "pulls" || rest[0] == "branches" || rest[0] == "commits"):
		shape[1] = "*"
		if rest[0] == "pulls" && len(rest) == 4 {
			shape[3] = "*"
		}
	case len(rest) == 3 && rest[0] == "rules" && rest[1] == "branches":
		shape[2] = "*"
	}
	return shape
}

func page(items []map[string]any, q url.Values) []map[string]any {
	perPage, err := strconv.Atoi(q.Get("per_page"))
	if err != nil {
		perPage = 30
	}
	number, err := strconv.Atoi(q.Get("page"))
	if err != nil {
		number = 1
	}
	start := min((number-1)*perPage, len(items))
	return append([]map[string]any{}, items[start:min(start+perPage, len(items))]...)
}

func (f *Fake) protection(branch string) result {
	strict, ok := f.Protection[branch]
	if !ok {
		return notFound("Branch not protected")
	}
	return replyJSON(200, map[string]any{"strict": strict, "contexts": []string{"test"}})
}

func (f *Fake) prJSON(pr *PR) map[string]any {
	return map[string]any{
		"number": pr.Number, "state": pr.State, "merged": pr.Merged, "draft": pr.Draft,
		"html_url":         fmt.Sprintf("https://github.test/%s/pull/%d", f.Repo, pr.Number),
		"merge_commit_sha": pr.MergeCommit, "mergeable_state": pr.MergeableState,
		"head": map[string]any{"ref": pr.HeadRef, "sha": pr.HeadSHA},
		"base": map[string]any{"ref": pr.BaseRef},
	}
}

func (f *Fake) listPRs(q url.Values) result {
	list := []map[string]any{}
	for _, number := range sortedKeys(f.prs) {
		pr := f.prs[number]
		_, branch, _ := strings.Cut(q.Get("head"), ":")
		if pr.State == q.Get("state") && pr.HeadRef == branch && pr.BaseRef == q.Get("base") {
			list = append(list, f.prJSON(pr))
		}
	}
	return replyJSON(200, list)
}

func sortedKeys(m map[int]*PR) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (f *Fake) createPR(body []byte) result {
	var in struct {
		Title, Head, Base, Body string
		Draft                   bool
	}
	if json.Unmarshal(body, &in) != nil || in.Title == "" || in.Head == "" || in.Base == "" {
		return replyJSON(422, map[string]any{"message": "Validation Failed"})
	}
	for _, pr := range f.prs {
		if pr.State == "open" && pr.HeadRef == in.Head && pr.BaseRef == in.Base {
			return replyJSON(422, map[string]any{"message": "A pull request already exists"})
		}
	}
	f.serial++
	pr := &PR{Number: f.serial, State: "open", Draft: in.Draft, HeadRef: in.Head, BaseRef: in.Base, HeadSHA: strings.Repeat("e", 40)}
	f.prs[pr.Number] = pr
	return replyJSON(201, f.prJSON(pr))
}

func (f *Fake) getPR(number string) result {
	f.advance()
	n, err := strconv.Atoi(number)
	if err != nil || f.prs[n] == nil {
		return notFound("Not Found")
	}
	return replyJSON(200, f.prJSON(f.prs[n]))
}

func (f *Fake) requestMerge(number string, body []byte) result {
	n, err := strconv.Atoi(number)
	if err != nil || f.prs[n] == nil {
		return notFound("Not Found")
	}
	pr := f.prs[n]
	if pr.Merged {
		return replyJSON(200, map[string]any{"status": "merged", "uuid": nil, "details": map[string]any{"sha": pr.MergeCommit}})
	}
	if pr.State != "open" || pr.Draft {
		return replyJSON(400, map[string]any{"status": "failed", "details": map[string]any{"message": "pull request is not mergeable"}})
	}
	for _, req := range f.order {
		if req.number == n && req.status == "pending" {
			return replyJSON(409, map[string]any{"status": "pending", "details": map[string]any{
				"uuid": req.uuid, "expected_head_sha": req.head, "merge_method": req.method,
				"merge_action": req.action, "bypass_rules": req.bypassed,
			}})
		}
	}
	var in struct {
		MergeMethod string `json:"merge_method"`
		MergeAction string `json:"merge_action"`
		SHA         string `json:"sha"`
	}
	if json.Unmarshal(body, &in) != nil || in.SHA == "" {
		return replyJSON(422, map[string]any{"message": "Validation Failed"})
	}
	f.serial++
	req := &mergeRequest{number: n, uuid: fmt.Sprintf("uuid-%d", f.serial), head: in.SHA, method: in.MergeMethod, action: in.MergeAction, status: "pending"}
	f.track(req)
	return replyJSON(202, map[string]any{"status": "pending", "uuid": nil, "details": map[string]any{"uuid": req.uuid}})
}

func (f *Fake) track(req *mergeRequest) {
	f.requests[req.uuid] = req
	f.order = append(f.order, req)
	f.ticks = f.Ticks
}

func (f *Fake) getRequest(uuid string) result {
	f.advance()
	req := f.requests[uuid]
	if req == nil {
		return notFound("Not Found")
	}
	details := map[string]any{"uuid": req.uuid}
	if req.sha != "" {
		details["sha"] = req.sha
	}
	if req.message != "" {
		details["message"] = req.message
	}
	return replyJSON(200, map[string]any{"status": req.status, "details": details})
}

// advance runs one step of the merges in flight; the caller holds the lock.
func (f *Fake) advance() {
	if f.Hold {
		return
	}
	if f.ticks > 0 {
		f.ticks--
		return
	}
	for _, req := range f.order {
		if req.status != "pending" {
			continue
		}
		pr := f.prs[req.number]
		switch {
		case pr.HeadSHA != req.head:
			req.status, req.message = "failed", "head commit changed"
		case pr.Merged || pr.State != "open":
			req.status, req.message = "failed", "pull request is closed"
		case f.FailMerge != "":
			req.status, req.message = "failed", f.FailMerge
		default:
			f.land(pr)
			req.status, req.sha = "merged", pr.MergeCommit
		}
	}
}

func (f *Fake) land(pr *PR) {
	f.serial++
	pr.State, pr.Merged, pr.MergeCommit = "closed", true, fmt.Sprintf("%040x", f.serial)
	f.merges[pr.Number]++
}
