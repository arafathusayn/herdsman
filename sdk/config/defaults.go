package config

import (
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/proto"
)

// SchemaVersion is the contract version this build writes and understands.
const SchemaVersion = "1.0.0"

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
