package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/wtree"
)

var upstreamTrack = regexp.MustCompile(`(ahead|behind) (\d+)`)

// fillUpstreams sets each row's remote branch and how far it has moved.
func (r *repository) fillUpstreams(ctx context.Context, resp *WorktreeListResponse) error {
	out, err := r.git.Run(ctx, r.main, "for-each-ref", "--format=%(refname:short)%00%(upstream:short)%00%(upstream:track)", "refs/heads")
	if err != nil {
		return err
	}
	type upstream struct {
		name          string
		ahead, behind int
	}
	found := map[string]upstream{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out.Stdout)), "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 || fields[1] == "" {
			continue
		}
		next := upstream{name: fields[1]}
		for _, match := range upstreamTrack.FindAllStringSubmatch(fields[2], -1) {
			count, _ := strconv.Atoi(match[2])
			if match[1] == "ahead" {
				next.ahead = count
			} else {
				next.behind = count
			}
		}
		found[fields[0]] = next
	}
	for i := range resp.Rows {
		if up, ok := found[resp.Rows[i].Branch]; ok {
			resp.Rows[i].Remote, resp.Rows[i].RemoteAhead, resp.Rows[i].RemoteBehind = up.name, up.ahead, up.behind
		}
	}
	return nil
}

// fillAgents names the agent in each worktree's herdr pane. No herdr means no
// agents to show, which is not a failure.
func (s *WorktreeService) fillAgents(ctx context.Context, resp *WorktreeListResponse) error {
	client := s.herdrClient(ctx)
	if client == nil {
		return nil
	}
	panes, err := client.PaneList(ctx)
	if err != nil {
		return err
	}
	agents, err := client.AgentList(ctx)
	if err != nil {
		return err
	}
	byPane := make(map[string]string, len(agents))
	for _, agent := range agents {
		if agent.Name != nil {
			byPane[agent.PaneID] = *agent.Name
		}
	}
	for i := range resp.Rows {
		for _, pane := range panes {
			if pane.Cwd != nil && *pane.Cwd == resp.Rows[i].Path && byPane[pane.ID] != "" {
				resp.Rows[i].Agent = byPane[pane.ID]
			}
		}
	}
	return nil
}

type pullState struct {
	Number         int           `json:"number"`
	HeadRefName    string        `json:"headRefName"`
	ReviewDecision string        `json:"reviewDecision"`
	Rollup         []rollupEntry `json:"statusCheckRollup"`
}

// rollupEntry is one of GitHub's two kinds of check: a check run reports a
// conclusion, and a commit status reports a state.
type rollupEntry struct {
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	State      string `json:"state"`
}

func (e rollupEntry) verdict() string {
	result := e.Conclusion
	if result == "" {
		result = e.State
	}
	switch result {
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return "failing"
	case "", "PENDING", "EXPECTED":
		return "pending"
	}
	return "passing"
}

func (p pullState) checks() string {
	result := ""
	for _, check := range p.Rollup {
		switch check.verdict() {
		case "failing":
			return "failing"
		case "pending":
			result = "pending"
		default:
			if result == "" {
				result = "passing"
			}
		}
	}
	return result
}

// fillPullRequests reads the open pull requests in one call. A machine with no
// gh or a repository with no origin simply has nothing to show.
func (s *WorktreeService) fillPullRequests(ctx context.Context, r *repository, resp *WorktreeListResponse) error {
	repo, err := s.repoName(ctx, r)
	if err != nil {
		return nil
	}
	program := s.GH
	if program == "" {
		program = "gh"
	}
	out, err := exec.CommandContext(ctx, program, "pr", "list", "--repo", repo, "--state", "open", "--limit", "200",
		"--json", "number,headRefName,reviewDecision,statusCheckRollup").Output()
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gh pr list: %w", err)
	}
	var pulls []pullState
	if err := json.Unmarshal(out, &pulls); err != nil {
		return fmt.Errorf("gh pr list: %w", err)
	}
	for i := range resp.Rows {
		for _, pull := range pulls {
			if pull.HeadRefName == resp.Rows[i].Branch {
				resp.Rows[i].PR, resp.Rows[i].Review, resp.Rows[i].CI = pull.Number, pull.ReviewDecision, pull.checks()
			}
		}
	}
	return nil
}

// sizeOf adds up the logical size of the regular files at or under path.
func sizeOf(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // A file that vanished or is unreadable counts as nothing.
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ignoredBytes is the logical size of the worktree's ignored files, the data a
// clean would have to decide about. Git names an ignored folder once.
func (r *repository) ignoredBytes(ctx context.Context, path string) (int64, error) {
	out, err := r.git.Run(ctx, path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return 0, err
	}
	var total int64
	for name := range strings.SplitSeq(strings.TrimRight(string(out.Stdout), "\x00"), "\x00") {
		if name != "" {
			total += sizeOf(filepath.Join(path, name))
		}
	}
	return total, nil
}

// fillLocal sets the cells that come from this repository's records and disk.
func (r *repository) fillLocal(ctx context.Context, resp *WorktreeListResponse) error {
	var failure error
	for i := range resp.Rows {
		row := &resp.Rows[i]
		if row.Path == "" {
			continue
		}
		row.Port = wtree.HashPort(row.Task)
		size, err := r.ignoredBytes(ctx, row.Path)
		if err != nil {
			failure = errors.Join(failure, err)
		}
		row.DiskBytes = size
		resources, err := r.state.Resources(ctx, row.Task)
		if err != nil {
			failure = errors.Join(failure, err)
			continue
		}
		for _, resource := range resources {
			switch resource.Kind {
			case wtree.KindPort:
				row.PortsHeld++
			case wtree.KindContainer, wtree.KindCompose:
				row.Containers++
			}
		}
	}
	return failure
}

// fillFull adds the slow cells. Each source that fails costs only its cells.
func (s *WorktreeService) fillFull(ctx context.Context, r *repository, resp *WorktreeListResponse) {
	for name, err := range map[string]error{
		"remote":        r.fillUpstreams(ctx, resp),
		"agents":        s.fillAgents(ctx, resp),
		"pull requests": s.fillPullRequests(ctx, r, resp),
		"local cells":   r.fillLocal(ctx, resp),
	} {
		if err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("could not read %s: %v", name, err))
		}
	}
	resp.Summary.Worktrees = len(resp.Rows)
	for _, row := range resp.Rows {
		if row.Status != "" {
			resp.Summary.Dirty++
		}
	}
}
