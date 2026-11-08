package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The labels the container detector reads.
const (
	LabelExec           = "herdsman.exec"
	LabelComposeProject = "com.docker.compose.project"
	LabelComposeDir     = "com.docker.compose.project.working_dir"
)

// Container is one container. `docker inspect` gives exact labels and mount
// sources. `docker ps --format json` joins labels without escaping, so a value
// can spell another label: rows of that form have LabelsExact false, and their
// labels never authorize anything.
type Container struct {
	ID      string
	Name    string
	State   string
	Labels  map[string]string
	Mounts  []string
	Created time.Time
	// LabelsExact is set when the labels came from a JSON object.
	LabelsExact bool
}

// Resource is a volume or network row. The volume listing has no creation
// time; a live reader joins CreatedAt from `docker volume inspect`.
type Resource struct {
	Name    string
	Labels  map[string]string
	Created time.Time
	// LabelsExact is set when the labels came from a JSON object.
	LabelsExact bool
}

type rawContainer struct {
	ID        string
	Names     json.RawMessage
	State     string
	Labels    json.RawMessage
	Mounts    json.RawMessage
	CreatedAt string
	Created   json.RawMessage
}

type rawResource struct {
	Name      string
	Labels    json.RawMessage
	CreatedAt string
	Created   string
}

// ParseContainers reads docker's one object per line or podman's array.
func ParseContainers(raw []byte) ([]Container, error) {
	if err := checkSize(raw); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	var rows []rawContainer
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("ledger: container array: %w", err)
		}
	} else {
		for _, line := range outputLines(trimmed) {
			var row rawContainer
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				return nil, fmt.Errorf("ledger: container line: %w", err)
			}
			rows = append(rows, row)
		}
	}
	var containers []Container
	for _, row := range rows {
		container, err := row.decode()
		if err != nil {
			return nil, err
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func (r rawContainer) decode() (Container, error) {
	if r.ID == "" {
		return Container{}, fmt.Errorf("ledger: container row without an id")
	}
	names, err := stringList(r.Names)
	if err != nil {
		return Container{}, fmt.Errorf("ledger: container %s names: %w", r.ID, err)
	}
	labels, exact, err := labelMap(r.Labels)
	if err != nil {
		return Container{}, fmt.Errorf("ledger: container %s labels: %w", r.ID, err)
	}
	mounts, err := stringList(r.Mounts)
	if err != nil {
		return Container{}, fmt.Errorf("ledger: container %s mounts: %w", r.ID, err)
	}
	return Container{ID: r.ID, Name: strings.Join(names, ","), State: r.State, Labels: labels, LabelsExact: exact, Mounts: mounts, Created: parseCreated(r.CreatedAt, r.Created)}, nil
}

type rawInspect struct {
	ID      string `json:"Id"`
	Name    string
	Created string
	State   struct{ Status string }
	Config  struct{ Labels map[string]string }
	Mounts  []struct{ Type, Source string }
}

// ParseContainerInspect reads the array `docker inspect` prints. Only bind
// mounts keep a source, since a volume's source is docker's own folder.
func ParseContainerInspect(raw []byte) ([]Container, error) {
	if err := checkSize(raw); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var rows []rawInspect
	if err := json.Unmarshal(trimmed, &rows); err != nil {
		return nil, fmt.Errorf("ledger: container inspect: %w", err)
	}
	containers := make([]Container, 0, len(rows))
	for _, row := range rows {
		if row.ID == "" {
			return nil, fmt.Errorf("ledger: inspect row without an id")
		}
		var mounts []string
		for _, mount := range row.Mounts {
			if mount.Type == "bind" {
				mounts = append(mounts, mount.Source)
			}
		}
		containers = append(containers, Container{
			ID: row.ID, Name: strings.TrimPrefix(row.Name, "/"), State: row.State.Status,
			Labels: row.Config.Labels, LabelsExact: true, Mounts: mounts, Created: parseCreated(row.Created, nil),
		})
	}
	return containers, nil
}

// ParseResources reads the array of `docker volume inspect` or `docker network
// inspect`, or the one object per line of `ls --format json`, whose labels are
// joined and so not exact.
func ParseResources(raw []byte) ([]Resource, error) {
	if err := checkSize(raw); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	var rows []rawResource
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("ledger: resource array: %w", err)
		}
	} else {
		for _, line := range outputLines(trimmed) {
			var row rawResource
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				return nil, fmt.Errorf("ledger: resource line: %w", err)
			}
			rows = append(rows, row)
		}
	}
	var resources []Resource
	for _, row := range rows {
		if row.Name == "" {
			return nil, fmt.Errorf("ledger: resource row without a name")
		}
		labels, exact, err := labelMap(row.Labels)
		if err != nil {
			return nil, fmt.Errorf("ledger: resource %s labels: %w", row.Name, err)
		}
		resources = append(resources, Resource{Name: row.Name, Labels: labels, LabelsExact: exact, Created: parseCreated(row.CreatedAt+row.Created, nil)})
	}
	return resources, nil
}

// stringList accepts docker's comma-joined string and podman's array.
func stringList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var joined string
	if err := json.Unmarshal(raw, &joined); err != nil {
		return nil, err
	}
	if joined == "" {
		return nil, nil
	}
	return strings.Split(joined, ","), nil
}

// labelMap accepts podman's and inspect's object, which is exact, and docker's
// k=v,k=v string, which is not.
func labelMap(raw json.RawMessage) (map[string]string, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var labels map[string]string
	if err := json.Unmarshal(raw, &labels); err == nil {
		return labels, true, nil
	}
	var joined string
	if err := json.Unmarshal(raw, &joined); err != nil {
		return nil, false, err
	}
	return parseLabelList(joined), false, nil
}

// parseLabelList splits k=v,k=v. A piece without = continues the previous
// value, since label values may hold commas.
func parseLabelList(joined string) map[string]string {
	labels := make(map[string]string)
	last := ""
	for _, piece := range strings.Split(joined, ",") {
		if key, value, found := strings.Cut(piece, "="); found {
			labels[key], last = value, key
		} else if last != "" {
			labels[last] += "," + piece
		}
	}
	return labels
}

// parseCreated reads docker's timestamps or podman's epoch seconds. A value it
// cannot read is zero: podman's CreatedAt can be "2 hours ago", and a zero
// time can never match a record that needs a creation time.
func parseCreated(text string, epoch json.RawMessage) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05 -0700 MST", time.RFC3339} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed
		}
	}
	var seconds int64
	if err := json.Unmarshal(epoch, &seconds); err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func containerSignals(scope Scope, c Container) []Signal {
	var signals []Signal
	if scope.Execution != "" && c.Labels[LabelExec] == scope.Execution {
		signals = append(signals, SignalExecLabel)
	}
	if scope.Compose != "" && c.Labels[LabelComposeProject] == scope.Compose {
		signals = append(signals, SignalComposeProject)
	}
	if underPath(scope.Worktree, c.Labels[LabelComposeDir]) {
		signals = append(signals, SignalComposeDir)
	}
	for _, mount := range c.Mounts {
		if underPath(scope.Worktree, mount) {
			signals = append(signals, SignalBindMount)
			break
		}
	}
	return signals
}

// projectProven reports a compose project whose exact labels place at least
// one container under the worktree and none elsewhere. A volume or network has
// no working directory of its own, so this is the only proof of its project.
func (p *planner) projectProven(project string) bool {
	proven := false
	for _, container := range p.s.Containers {
		dir := container.Labels[LabelComposeDir]
		if container.Labels[LabelComposeProject] != project || !container.LabelsExact || dir == "" {
			continue
		}
		if !underPath(p.s.Worktree, dir) {
			return false
		}
		proven = true
	}
	return proven
}

// containers is detector 3. Images are never candidates. A compose record is
// a name, so it counts only for a container whose exact working_dir label is
// under the worktree or whose own id is recorded.
func (p *planner) containers() []*candidate {
	var found []*candidate
	for _, container := range p.s.Containers {
		id := Identity{Kind: KindContainer, ID: container.ID, Start: container.Created}
		c := newCandidate(KindContainer, container.ID, Identity{Kind: KindContainer, ID: container.ID})
		c.ev.Signals = containerSignals(p.s.Scope, container)
		adopted := p.applyRecords(&c.ev, id, true)
		inWorktree := container.LabelsExact && underPath(p.s.Worktree, container.Labels[LabelComposeDir])
		project := Identity{Kind: KindCompose, ID: container.Labels[LabelComposeProject]}
		adopted = p.applyRecords(&c.ev, project, c.ev.Recorded || inWorktree) || adopted
		if !c.ev.hit() {
			continue
		}
		c.ev.Unverified = !container.LabelsExact && !c.ev.Recorded
		if execution := container.Labels[LabelExec]; execution != "" && execution != p.s.Execution {
			c.ev.share("execution:" + execution)
		}
		for _, other := range p.s.Others {
			if len(containerSignals(other, container)) > 0 {
				c.ev.share("task:" + other.Task)
			}
		}
		p.older(&c.ev, container.Created, adopted)
		c.line.Name, c.line.State = container.Name, container.State
		found = append(found, c)
	}
	return found
}

// labelled is the volume and network half of detector 3: by the compose
// project label, or by a record of the resource or of its proven project.
func (p *planner) labelled(kind string, resources []Resource) []*candidate {
	var found []*candidate
	for _, resource := range resources {
		project := resource.Labels[LabelComposeProject]
		id := Identity{Kind: kind, ID: resource.Name, Start: resource.Created}
		c := newCandidate(kind, resource.Name, id)
		if p.s.Compose != "" && project == p.s.Compose {
			c.ev.add(SignalComposeProject)
		}
		adopted := p.applyRecords(&c.ev, id, true)
		proven := resource.LabelsExact && p.projectProven(project)
		adopted = p.applyRecords(&c.ev, Identity{Kind: KindCompose, ID: project}, c.ev.Recorded || proven) || adopted
		if !c.ev.hit() {
			continue
		}
		c.ev.Unverified = !resource.LabelsExact && !c.ev.Recorded
		for _, other := range p.s.Others {
			if other.Compose != "" && project == other.Compose {
				c.ev.share("task:" + other.Task)
			}
		}
		p.older(&c.ev, resource.Created, adopted)
		found = append(found, c)
	}
	return found
}
