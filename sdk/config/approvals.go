package config

import (
	"fmt"
	"slices"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/toml"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// DecodeApprovals reads the approvals file: for each project, the exact hook
// templates a person approved.
func DecodeApprovals(data []byte) (map[string][]string, error) {
	root, err := toml.Decode(data)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	table, ok := root.Get("approvals")
	if !ok {
		return out, nil
	}
	for _, project := range table.Keys() {
		entry, _ := table.Get(project)
		templates, ok := entry.Lookup("templates")
		if !ok || templates.Kind != tree.Array {
			return nil, fmt.Errorf("approvals.%s: expected a templates array", project)
		}
		for _, item := range templates.Items {
			if item.Kind != tree.String {
				return nil, fmt.Errorf("approvals.%s: a template must be a string", project)
			}
			out[project] = append(out[project], item.Str)
		}
	}
	return out, nil
}

// EncodeApprovals writes the approvals file in a stable order.
func EncodeApprovals(approvals map[string][]string) ([]byte, error) {
	root := tree.NewTable()
	schema := tree.NewTable()
	schema.Set("version", tree.NewString(SchemaVersion))
	root.Set("schema", schema)
	table := tree.NewTable()
	projects := make([]string, 0, len(approvals))
	for project := range approvals {
		projects = append(projects, project)
	}
	slices.Sort(projects)
	for _, project := range projects {
		templates := tree.NewArray()
		for _, template := range approvals[project] {
			templates.Items = append(templates.Items, tree.NewString(template))
		}
		entry := tree.NewTable()
		entry.Set("templates", templates)
		table.Set(project, entry)
	}
	root.Set("approvals", table)
	return toml.Encode(root)
}
