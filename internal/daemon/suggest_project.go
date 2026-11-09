package daemon

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/project"
)

const (
	sourcesKey = "suggest.sources"
	// recentEventCount is how far back the ranking looks in a project's history.
	recentEventCount = 20
	eventHeaderLimit = 64
)

// productionSuggest is the service over the real machine. account gives the
// account store when this process has one.
func productionSuggest(account func() store.Conn) *SuggestService {
	s := &SuggestService{ContextDir: projectContextDir}
	if account != nil {
		s.Labels = func(dir string) []string { return projectLabels(account(), dir) }
	}
	return s
}

func isFolder(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// projectContextDir is the context repo of the project dir is bound to: the
// bound folder, once it has the context repo's layout. A folder that cannot be
// read is treated as having none, because ranking must not fail over it.
func projectContextDir(dir string) string {
	binding, ok, err := project.FindBinding(dir)
	if err != nil || !ok {
		return ""
	}
	if !isFolder(filepath.Join(binding.Folder, "events")) || !isFolder(filepath.Join(binding.Folder, "suggest")) {
		return ""
	}
	return binding.Folder
}

// projectLabels gathers the labels on the tasks of the project dir is bound to.
func projectLabels(db store.Conn, dir string) []string {
	if db == nil {
		return nil
	}
	binding, ok, err := project.FindBinding(dir)
	if err != nil || !ok {
		return nil
	}
	tasks, err := project.New(db, nil).ListTasks(project.TaskFilter{ProjectID: binding.ID})
	if err != nil {
		return nil
	}
	var labels []string
	for _, task := range tasks {
		labels = append(labels, task.Labels...)
	}
	slices.Sort(labels)
	return slices.Compact(labels)
}

// recentEventKinds reads the kinds of the newest events. An event's file name
// is a time-ordered id, so names sort by age.
func recentEventKinds(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, "events"))
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".md") && entry.Type().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	slices.Reverse(names)
	var kinds []string
	for _, name := range names[:min(len(names), recentEventCount)] {
		if kind := eventKind(filepath.Join(root, "events", name)); kind != "" {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// eventKind reads the kind out of an event's header, without the body.
func eventKind(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	lines := bufio.NewScanner(file)
	for count := 0; count < eventHeaderLimit && lines.Scan(); count++ {
		if count > 0 && lines.Text() == "---" {
			return ""
		}
		if value, ok := strings.CutPrefix(lines.Text(), "kind:"); ok {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	return ""
}
