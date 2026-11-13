package core

import (
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// CallerExecution identifies a caller on a daemon socket by its process
// (spec section 6): it walks from the socket peer up its ancestors until one
// is a registered core execution's process, the pid with the recorded start
// time. Whatever the caller presents is a hint for messages only. A recovery
// shell at an execution's address is another process and is not that
// execution; a process that left the tree on purpose is an unknown caller.
func CallerExecution(procs ProcessTable, peer int, executions []runtime.Execution) (runtime.Execution, bool) {
	byPID := map[int][]runtime.Execution{}
	for _, e := range executions {
		if running(e) && registered(e) {
			byPID[e.Evidence.PID] = append(byPID[e.Evidence.PID], e)
		}
	}
	if len(byPID) == 0 {
		return runtime.Execution{}, false
	}
	for _, pid := range ancestorsOf(procs, peer) {
		candidates := byPID[pid]
		if len(candidates) == 0 {
			continue
		}
		start, err := procs.Identify(pid)
		if err != nil {
			continue
		}
		for _, e := range candidates {
			if e.Evidence.Start == StartString(start) {
				return e, true
			}
		}
		// The pid was reused: what runs there now is not the execution. Its
		// own ancestors may still be one, so the walk goes on.
	}
	return runtime.Execution{}, false
}
