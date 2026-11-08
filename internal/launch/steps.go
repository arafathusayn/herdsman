package launch

import "fmt"

// Action is what resume does with one prelude step.
type Action string

const (
	// ActionSkip leaves a step already recorded done.
	ActionSkip Action = "skip"
	// ActionRun submits the step, after ClearMarkers.
	ActionRun Action = "run"
	// ActionProbe submits the step's probe, after clearing its old result.
	ActionProbe Action = "probe"
	// ActionDone records the step done without running it.
	ActionDone Action = "done"
	// ActionFail stops up: the step ended with a nonzero status.
	ActionFail Action = "fail"
	// ActionStop stops up: only a person can settle the step.
	ActionStop Action = "stop"
)

// Decision is the outcome for one step.
type Decision struct {
	Action Action
	// RC is the exit status of ActionFail.
	RC     int
	Reason string
	// Resume holds the commands a person can continue with after a stop.
	Resume []string
}

// Override is a person's verdict on a step, from --step <n>=<override>.
type Override string

const (
	// OverrideDone says the person checked that the effect happened.
	OverrideDone Override = "done"
	// OverrideRerun says the person checked that it did not.
	OverrideRerun Override = "rerun"
)

// Decide maps a step's record and markers to what resume does:
//
//	recorded done              skip
//	not started                run: the command never ran
//	finished, status 0         done
//	finished, status nonzero   fail
//	uncertain, idempotent      run
//	uncertain, has a probe     probe
//	uncertain, otherwise       stop, with the resume lines
//
// The order of the uncertain rows is the documented one: an idempotent step
// runs again even when it also has a probe.
func Decide(step Step, n int, recordedDone bool, marker Marker) Decision {
	if recordedDone {
		return Decision{Action: ActionSkip, Reason: fmt.Sprintf("step %d is recorded done", n)}
	}
	switch marker.State {
	case MarkerNotStarted:
		return Decision{Action: ActionRun, Reason: fmt.Sprintf("step %d has no start marker, so it never ran", n)}
	case MarkerFinished:
		if marker.RC == 0 {
			return Decision{Action: ActionDone, Reason: fmt.Sprintf("step %d exited 0", n)}
		}
		return Decision{
			Action: ActionFail,
			RC:     marker.RC,
			Reason: fmt.Sprintf("step %d (%s) exited %d", n, step.Command, marker.RC),
			Resume: resumeLines(step, n),
		}
	}
	if step.RetryPolicy() == RetryIdempotent {
		return Decision{Action: ActionRun, Reason: fmt.Sprintf("step %d was interrupted and is idempotent", n)}
	}
	if step.Probe != nil {
		return Decision{Action: ActionProbe, Reason: fmt.Sprintf("step %d was interrupted; its probe decides", n)}
	}
	return Decision{
		Action: ActionStop,
		Reason: fmt.Sprintf("step %d (%s) was interrupted and its effect is unknown", n, step.Command),
		Resume: resumeLines(step, n),
	}
}

// DecideProbe maps the markers of a step's probe to what resume does. A probe
// without a result is run again, since a probe only observes.
func DecideProbe(step Step, n int, marker Marker) Decision {
	if marker.State != MarkerFinished {
		return Decision{Action: ActionProbe, Reason: fmt.Sprintf("the probe of step %d has no result", n)}
	}
	if marker.RC == 0 {
		return Decision{Action: ActionDone, Reason: fmt.Sprintf("the probe of step %d exited 0", n)}
	}
	if step.Once {
		return Decision{
			Action: ActionStop,
			Reason: fmt.Sprintf("the probe of step %d (%s) found no effect, and a once step never runs again", n, step.Command),
			Resume: resumeLines(step, n),
		}
	}
	return Decision{Action: ActionRun, Reason: fmt.Sprintf("the probe of step %d found no effect", n)}
}

// ApplyOverride applies a person's verdict on a stopped step.
func ApplyOverride(step Step, n int, override Override) (Decision, error) {
	switch override {
	case OverrideDone:
		return Decision{Action: ActionDone, Reason: fmt.Sprintf("step %d was marked done by hand", n)}, nil
	case OverrideRerun:
		if step.Once {
			return Decision{}, fmt.Errorf("step %d is once and cannot be rerun", n)
		}
		return Decision{Action: ActionRun, Reason: fmt.Sprintf("step %d was marked for a rerun by hand", n)}, nil
	}
	return Decision{}, fmt.Errorf("step %d: unknown override %q", n, override)
}

func resumeLines(step Step, n int) []string {
	lines := []string{fmt.Sprintf("herdsman up --continue --step %d=%s", n, OverrideDone)}
	if !step.Once {
		lines = append(lines, fmt.Sprintf("herdsman up --continue --step %d=%s", n, OverrideRerun))
	}
	return lines
}
