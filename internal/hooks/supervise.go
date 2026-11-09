package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

const (
	// superviseEnv tells a copy of this program to run a saved plan and exit.
	superviseEnv = "HERDSMAN_HOOK_SUPERVISE"
	planFile     = "plan.json"
)

// savedRun is what a detached copy of this program needs to run a hook.
type savedRun struct {
	Runner Runner
	Hook   string
	Folder string
	Plan   Plan
}

// Any program that can start a background hook can also run one, whatever its
// main function does, so the copy is caught here and not in a command.
func init() {
	spec := os.Getenv(superviseEnv)
	if spec == "" {
		return
	}
	// The hook's own commands may run this program again.
	os.Unsetenv(superviseEnv)
	os.Exit(supervise(spec))
}

func (r *Runner) savePlan(path, hook, folder string, plan Plan) error {
	data, err := json.Marshal(savedRun{Runner: *r, Hook: hook, Folder: folder, Plan: plan})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("hooks: save plan: %w", err)
	}
	return nil
}

// supervise runs a saved plan to its end. Its exit code is the process's.
func supervise(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hooks:", err)
		return 2
	}
	var saved savedRun
	if err := json.Unmarshal(data, &saved); err != nil {
		fmt.Fprintln(os.Stderr, "hooks:", path, err)
		return 2
	}
	if _, err := saved.Runner.execute(context.Background(), saved.Hook, saved.Plan, saved.Folder); err != nil {
		fmt.Fprintln(os.Stderr, "hooks:", err)
		return 1
	}
	return 0
}
