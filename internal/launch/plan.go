package launch

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// PlanStep is one prelude step in a dry-run plan.
type PlanStep struct {
	N       int
	Command string
	Retry   Retry
	Once    bool
	Probe   string
	Submit  string
}

// Plan is what a launch would do, computed without any side effect.
type Plan struct {
	Cwd     string
	Timeout time.Duration
	Steps   []PlanStep
	Env     []EnvVar
	Agent   string
}

// NewPlan computes the dry-run plan of a spec.
func NewPlan(spec Spec, stepsDir string) (Plan, error) {
	if err := spec.Validate(); err != nil {
		return Plan{}, err
	}
	plan := Plan{Cwd: spec.Cwd, Timeout: spec.Timeout(), Agent: Command{Argv: spec.Command}.String()}
	for i, step := range spec.Prelude {
		submit, err := Wrap(stepsDir, i+1, step)
		if err != nil {
			return Plan{}, err
		}
		item := PlanStep{
			N:       i + 1,
			Command: step.Command.String(),
			Retry:   step.RetryPolicy(),
			Once:    step.Once,
			Submit:  submit,
		}
		if step.Probe != nil {
			item.Probe = step.Probe.String()
		}
		plan.Steps = append(plan.Steps, item)
	}
	names := make([]string, 0, len(spec.Env))
	for name := range spec.Env {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		plan.Env = append(plan.Env, EnvVar{Name: name, Value: spec.Env[name]})
	}
	return plan, nil
}

// String prints the plan, the commands verbatim.
func (p Plan) String() string {
	var b strings.Builder
	if p.Cwd != "" {
		fmt.Fprintf(&b, "cwd: %s\n", p.Cwd)
	}
	fmt.Fprintf(&b, "prelude timeout: %s\n", p.Timeout)
	for _, step := range p.Steps {
		fmt.Fprintf(&b, "step %d: %s\n", step.N, step.Command)
		retry := string(step.Retry)
		if step.Once {
			retry += " (once)"
		}
		fmt.Fprintf(&b, "  retry: %s\n", retry)
		if step.Probe != "" {
			fmt.Fprintf(&b, "  probe: %s\n", step.Probe)
		}
		fmt.Fprintf(&b, "  submit: %s\n", step.Submit)
	}
	for _, e := range p.Env {
		fmt.Fprintf(&b, "env: %s=%s\n", e.Name, quote(e.Value))
	}
	fmt.Fprintf(&b, "agent: %s\n", p.Agent)
	return b.String()
}
