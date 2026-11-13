package daemon

import (
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
)

// rpcProcedures lists every RPC method the daemon serves.
func (d *Daemon) rpcProcedures() []rpc.Procedure {
	m := &messageService{d: d}
	recovery := &opsService{d: d}
	routes := &routeService{d: d}
	runtimes := &runtimeService{d: d}
	projectContext := &projectContextService{}
	suggestions := &suggestRPC{s: &SuggestService{}, inspect: d.cfg.Inspect}
	records := &recordsRPC{d: d}
	projects, tasks := projectRPC{records}, taskRPC{records}
	eventRPC := newEventService(d.hub, d.admitEvents, releaseContext)
	name := daemonv1.MessageServiceName
	worktrees := newTaskRPC(d)
	ledgers := ledgerRPC{d: d, inspect: d.cfg.Inspect}
	return append(eventRPC.procedures(), []rpc.Procedure{
		rpc.Unary(daemonv1.TaskServiceName, "New", worktrees.New),
		rpc.Unary(daemonv1.TaskServiceName, "Go", worktrees.Go),
		rpc.Unary(daemonv1.LedgerServiceName, "NewWorktree", ledgers.NewWorktree),
		rpc.Unary(daemonv1.LedgerServiceName, "Resources", ledgers.Resources),
		rpc.Unary(daemonv1.LedgerServiceName, "Plan", ledgers.Plan),
		rpc.Unary(daemonv1.LedgerServiceName, "AddResource", ledgers.AddResource),
		rpc.Unary(daemonv1.LedgerServiceName, "TeardownPlans", ledgers.TeardownPlans),
		rpc.Unary(daemonv1.LedgerServiceName, "Teardown", ledgers.Teardown),
		rpc.Unary(daemonv1.LedgerServiceName, "RegisterResource", ledgers.RegisterResource),
		rpc.Unary(daemonv1.LedgerServiceName, "LedgerHealth", ledgers.LedgerHealth),
		rpc.Unary(name, "Hello", m.Hello),
		rpc.Unary(name, "Health", m.Health),
		rpc.Unary(name, "Shutdown", m.Shutdown),
		rpc.Unary(name, "ListRoutes", m.ListRoutes),
		rpc.Unary(name, "CreateRoute", m.CreateRoute),
		rpc.Unary(name, "AdoptRoute", m.AdoptRoute),
		rpc.Unary(name, "RemoveRoute", m.RemoveRoute),
		rpc.Unary(name, "ResetEvents", m.ResetEvents),
		rpc.Unary(name, "Status", m.Status),
		rpc.Unary(name, "GetSetup", m.GetSetup),
		rpc.Unary(name, "PutSetup", m.PutSetup),
		rpc.Unary(name, "Enroll", m.Enroll),
		rpc.Unary(name, "ApprovePair", m.ApprovePair),
		rpc.Unary(name, "RetirePair", m.RetirePair),
		rpc.Unary(name, "RevokeCredential", m.RevokeCredential),
		rpc.Unary(name, "Send", m.Send),
		rpc.Unary(name, "Claim", m.Claim),
		rpc.Unary(name, "Ack", m.Ack),
		rpc.Unary(name, "Release", m.Release),
		rpc.Unary(name, "Hold", m.Hold),
		rpc.Unary(name, "Unhold", m.Unhold),
		rpc.Unary(name, "Authority", m.Authority),
		rpc.Unary(name, "DaemonStatus", m.DaemonStatus),
		rpc.Unary(name, "ListOperations", m.ListOperations),
		rpc.Unary(name, "GetOperation", m.GetOperation),
		rpc.Unary(name, "ResolveOperation", m.ResolveOperation),
		rpc.Unary(name, "Wait", m.Wait),
		rpc.Unary(name, "ResumeGoal", m.ResumeGoal),
		rpc.Unary(daemonv1.OpsServiceName, "Undo", recovery.Undo),
		rpc.Unary(daemonv1.OpsServiceName, "Export", recovery.Export),
		rpc.Unary(daemonv1.OpsServiceName, "Import", recovery.Import),
		rpc.Unary(daemonv1.OpsServiceName, "Records", recovery.Records),
		rpc.Unary(daemonv1.RouteServiceName, "Up", routes.Up),
		rpc.Unary(daemonv1.RouteServiceName, "Get", routes.Get),
		rpc.Unary(daemonv1.RouteServiceName, "List", routes.List),
		rpc.Unary(daemonv1.ProjectServiceName, "Create", projects.Create),
		rpc.Unary(daemonv1.ProjectServiceName, "Get", projects.Get),
		rpc.Unary(daemonv1.ProjectServiceName, "List", projects.List),
		rpc.Unary(daemonv1.ProjectServiceName, "Update", projects.Update),
		rpc.Unary(daemonv1.ProjectServiceName, "Delete", projects.Delete),
		rpc.Unary(daemonv1.ProjectServiceName, "Bind", projects.Bind),
		rpc.Unary(daemonv1.ProjectServiceName, "Unbind", projects.Unbind),
		rpc.Unary(daemonv1.TaskServiceName, "Create", tasks.Create),
		rpc.Unary(daemonv1.TaskServiceName, "Get", tasks.Get),
		rpc.Unary(daemonv1.TaskServiceName, "List", tasks.List),
		rpc.Unary(daemonv1.TaskServiceName, "Update", tasks.Update),
		rpc.Unary(daemonv1.TaskServiceName, "Delete", tasks.Delete),
		rpc.Unary(daemonv1.TaskServiceName, "Link", tasks.Link),
		rpc.Unary(suggestv1.SuggestServiceName, "Search", suggestions.Search),
		rpc.Unary(suggestv1.SuggestServiceName, "Get", suggestions.Get),
		rpc.Unary(suggestv1.SuggestServiceName, "Add", suggestions.Add),
		rpc.Unary(suggestv1.SuggestServiceName, "Plan", suggestions.Plan),
		rpc.Unary(suggestv1.SuggestServiceName, "Sources", suggestions.Sources),
		rpc.Unary(suggestv1.SuggestServiceName, "SourceAdd", suggestions.SourceAdd),
		rpc.Unary(suggestv1.SuggestServiceName, "SourceRemove", suggestions.SourceRemove),
		rpc.Unary(suggestv1.SuggestServiceName, "SourcePreview", suggestions.SourcePreview),
		rpc.Unary(suggestv1.SuggestServiceName, "SourcePin", suggestions.SourcePin),
		rpc.Unary(daemonv1.ProjectServiceName, "ContextInit", projectContext.ContextInit),
		rpc.Unary(daemonv1.ProjectServiceName, "ContextCheck", projectContext.ContextCheck),
		rpc.Unary(daemonv1.ProjectServiceName, "ContextNote", projectContext.ContextNote),
		rpc.Unary(daemonv1.ProjectServiceName, "Log", projectContext.Log),
		rpc.Unary(daemonv1.RouteServiceName, "Pause", routes.Pause),
		rpc.Unary(daemonv1.RouteServiceName, "Resume", routes.Resume),
		rpc.Unary(daemonv1.RouteServiceName, "Stop", routes.Stop),
		rpc.Unary(daemonv1.RouteServiceName, "Delete", routes.Delete),
		rpc.Unary(daemonv1.RuntimeServiceName, "ListExecutions", runtimes.ListExecutions),
		rpc.Unary(daemonv1.RuntimeServiceName, "GetExecution", runtimes.GetExecution),
		rpc.Unary(daemonv1.RuntimeServiceName, "Doctor", runtimes.Doctor),
		rpc.Unary(daemonv1.RuntimeServiceName, "GetDefault", runtimes.GetDefault),
		rpc.Unary(daemonv1.RuntimeServiceName, "SetDefault", runtimes.SetDefault),
		rpc.Unary(daemonv1.RuntimeServiceName, "Submit", runtimes.Submit),
		rpc.Unary(daemonv1.RuntimeServiceName, "Show", runtimes.Show),
		rpc.Unary(daemonv1.RuntimeServiceName, "Read", runtimes.Read),
	}...)
}
