// Package github lands changes on GitHub through the `gh` command.
//
// Every call goes through `gh api --include`, so an answer carries its HTTP
// status even when gh exits non-zero. A call whose status cannot be read is
// status 0: the request may or may not have reached GitHub.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/landing"
)

// Options tunes a Backend. Zero values take the defaults.
type Options struct {
	Bin          string        // default "gh"
	Account      string        // when set, calls use this gh account's token
	CallTimeout  time.Duration // one gh run, default 60s
	PollInterval time.Duration // between readings of a merge, default 5s
	PollTimeout  time.Duration // how long Merge waits for a final state, default 10m
}

// Backend is the GitHub landing backend for one repository.
type Backend struct {
	repo  string
	owner string
	opt   Options

	mu    sync.Mutex
	token string
}

var _ landing.Backend = (*Backend)(nil)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

// New returns a backend for repo, written owner/name.
func New(repo string, opt Options) (*Backend, error) {
	if !repoPattern.MatchString(repo) {
		return nil, fmt.Errorf("github: invalid repository %q", repo)
	}
	if opt.Bin == "" {
		opt.Bin = "gh"
	}
	if opt.CallTimeout <= 0 {
		opt.CallTimeout = 60 * time.Second
	}
	if opt.PollInterval <= 0 {
		opt.PollInterval = 5 * time.Second
	}
	if opt.PollTimeout <= 0 {
		opt.PollTimeout = 10 * time.Minute
	}
	owner, _, _ := strings.Cut(repo, "/")
	return &Backend{repo: repo, owner: owner, opt: opt}, nil
}

func (b *Backend) api(path string) string { return "repos/" + b.repo + "/" + path }

// Open returns the open pull request for the head and base, creating it if
// there is none. Creation is sent at most once: whatever its answer, the
// pull request is then looked up, so a lost response cannot make a second one.
func (b *Backend) Open(ctx context.Context, req landing.OpenRequest) (landing.PullRequest, error) {
	if req.Head == "" || req.Base == "" || req.Title == "" {
		return landing.PullRequest{}, errors.New("github: open needs a head, a base and a title")
	}
	if pr, found, err := b.find(ctx, req); err != nil || found {
		return pr, err
	}
	r := b.call(ctx, "POST", b.api("pulls"), map[string]any{
		"title": req.Title, "head": req.Head, "base": req.Base, "body": req.Body, "draft": req.Draft,
	})
	if r.status == 201 {
		if pr, err := decodePR(r.body); err == nil {
			return pr.pullRequest(), nil
		}
	}
	pr, found, err := b.find(ctx, req)
	if err != nil {
		return landing.PullRequest{}, fmt.Errorf("github: open: %s; then: %w", r.reason, err)
	}
	if !found {
		return landing.PullRequest{}, fmt.Errorf("github: open: %s", r.reason)
	}
	return pr, nil
}

func (b *Backend) find(ctx context.Context, req landing.OpenRequest) (landing.PullRequest, bool, error) {
	q := url.Values{"state": {"open"}, "head": {b.owner + ":" + req.Head}, "base": {req.Base}, "per_page": {"100"}}
	body, err := b.get(ctx, b.api("pulls?"+q.Encode()))
	if err != nil {
		return landing.PullRequest{}, false, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return landing.PullRequest{}, false, fmt.Errorf("github: pull request list: %w", err)
	}
	if len(raw) == 0 {
		return landing.PullRequest{}, false, nil
	}
	pr, err := decodePR(raw[0])
	if err != nil {
		return landing.PullRequest{}, false, err
	}
	return pr.pullRequest(), true, nil
}

// prJSON is the part of a pull request the backend reads.
type prJSON struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	Draft          bool   `json:"draft"`
	HTMLURL        string `json:"html_url"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// decodePR refuses an answer that would otherwise read as a state: an empty
// head must not look like a moved head.
func decodePR(body []byte) (prJSON, error) {
	var pr prJSON
	if err := json.Unmarshal(body, &pr); err != nil {
		return prJSON{}, fmt.Errorf("github: pull request: %w", err)
	}
	if pr.Number == 0 || pr.State == "" || pr.Head.SHA == "" {
		return prJSON{}, errors.New("github: pull request answer is incomplete")
	}
	if pr.Merged && pr.MergeCommitSHA == "" {
		return prJSON{}, errors.New("github: merged pull request has no merge commit")
	}
	return pr, nil
}

func (pr prJSON) pullRequest() landing.PullRequest {
	return landing.PullRequest{Number: pr.Number, URL: pr.HTMLURL, HeadRef: pr.Head.Ref, HeadSHA: pr.Head.SHA, BaseRef: pr.Base.Ref, Draft: pr.Draft}
}

func (b *Backend) readPR(ctx context.Context, number int) (prJSON, error) {
	body, err := b.get(ctx, b.api("pulls/"+strconv.Itoa(number)))
	if err != nil {
		return prJSON{}, err
	}
	return decodePR(body)
}

// reply is one gh run. status 0 means no HTTP status could be trusted.
type reply struct {
	status int
	body   []byte
	reason string
}

// get reads one resource; anything but a 200 is an error.
func (b *Backend) get(ctx context.Context, path string) ([]byte, error) {
	r := b.call(ctx, "GET", path, nil)
	if r.status != 200 {
		return nil, fmt.Errorf("github: %s: %s", path, r.reason)
	}
	return r.body, nil
}

// call runs `gh api` once, sending input as the JSON body on stdin so no value
// passes through gh's field syntax.
func (b *Backend) call(ctx context.Context, method, path string, input map[string]any) reply {
	args := []string{"api", "--include", "-X", method}
	var stdin []byte
	if input != nil {
		stdin, _ = json.Marshal(input) // only maps of strings and bools reach here
		args = append(args, "--input", "-")
	}
	args = append(args, path)
	token, err := b.accountToken(ctx)
	if err != nil {
		return reply{reason: err.Error()}
	}
	out, stderr, runErr := b.run(ctx, token, stdin, args...)
	status, body, ok := parseHTTP(out)
	if !ok || (runErr != nil && status < 400) {
		return reply{reason: failure(runErr, stderr, "gh answered without a readable status")}
	}
	r := reply{status: status, body: body}
	if status >= 300 {
		r.reason = message(body, status)
	}
	return r
}

func failure(err error, stderr, fallback string) string {
	if text := strings.TrimSpace(stderr); text != "" {
		return text
	}
	if err != nil {
		return err.Error()
	}
	return fallback
}

// message is the service's own words for a refusal, or the status when it gave none.
func message(body []byte, status int) string {
	var a asyncReply
	if json.Unmarshal(body, &a) == nil {
		if a.Details.Message != "" {
			return a.Details.Message
		}
		if a.Message != "" {
			return a.Message
		}
	}
	return "HTTP " + strconv.Itoa(status)
}

func (b *Backend) accountToken(ctx context.Context) (string, error) {
	if b.opt.Account == "" {
		return "", nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.token != "" {
		return b.token, nil
	}
	out, stderr, err := b.run(ctx, "", nil, "auth", "token", "--user", b.opt.Account)
	if err != nil {
		return "", fmt.Errorf("github: no token for %s: %s", b.opt.Account, failure(err, stderr, ""))
	}
	b.token = strings.TrimSpace(string(out))
	return b.token, nil
}

func (b *Backend) run(ctx context.Context, token string, stdin []byte, args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, b.opt.CallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.opt.Bin, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	if token != "" {
		cmd.Env = append(cmd.Env, "GH_TOKEN="+token)
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.String(), err
}

// parseHTTP splits `gh api --include` output into its status and body.
func parseHTTP(out []byte) (int, []byte, bool) {
	sep := []byte("\r\n\r\n")
	head, body, found := bytes.Cut(out, sep)
	if !found {
		sep = []byte("\n\n")
		if head, body, found = bytes.Cut(out, sep); !found {
			return 0, nil, false
		}
	}
	line, _, _ := strings.Cut(string(head), "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, nil, false
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, false
	}
	return status, body, true
}
