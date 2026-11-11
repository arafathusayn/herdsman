package config

import (
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/proto"
)

// SchemaVersion is the contract version this build writes and understands.
const SchemaVersion = "1.0.0"

// NewFileBackend is the execution backend a new config file names. It is
// written by InitFile only and is never a default of Load: a file without a
// [runtime] backend falls back to the installation default, so an existing
// file keeps the backend it has. While herdr-core ships as an experiment
// (selected only with experimental = true in [runtime.core]), a new file names
// the Herdr TUI; the release where core graduates changes this one constant.
const NewFileBackend = "herdr-tui"

// Defaults returns the settings used when nothing overrides them. It is the
// one place a default is written.
func Defaults() *configv1.Config {
	return &configv1.Config{
		Schema:   &configv1.Schema{Version: proto.String(SchemaVersion)},
		Worktree: &configv1.Worktree{Path: proto.String("{{ repo_path }}/../{{ repo }}.worktrees/{{ task }}")},
		Branch: &configv1.Branch{
			Name:  proto.String("{{ type }}/{{ task }}"),
			Types: []string{"build", "chore", "ci", "docs", "feat", "fix", "perf", "refactor", "test"},
		},
		Setup: &configv1.Setup{Ports: proto.Int32(10)},
		Teardown: &configv1.Teardown{
			Grace:      proto.Int32(10),
			Containers: proto.String("compose"),
			Volumes:    configv1.VolumePolicy_VOLUME_POLICY_ON_CLEAN,
		},
		Merge: &configv1.Merge{
			Strategy: configv1.MergeStrategy_MERGE_STRATEGY_SQUASH,
			Land:     configv1.LandMode_LAND_MODE_PR,
			Verify:   proto.Bool(true),
			Clean:    proto.Bool(true),
		},
		Commit: &configv1.Commit{
			Stage:      configv1.StagePolicy_STAGE_POLICY_ALL,
			Convention: proto.String("conventional"),
			Generation: &configv1.CommitGeneration{
				Enabled: proto.Bool(true),
				Command: proto.String("auto"),
				Summary: proto.Bool(true),
			},
		},
	}
}

// NewFile returns what InitFile writes: the defaults, plus the execution
// backend, so the choice is visible where it applies. UpdateFile never adds it.
func NewFile() *configv1.Config {
	c := Defaults()
	c.Runtime = &configv1.Runtime{Backend: proto.String(NewFileBackend)}
	return c
}
