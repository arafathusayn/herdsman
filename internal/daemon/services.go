package daemon

import (
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
)

// rpcProcedures lists every RPC method the daemon serves.
func (d *Daemon) rpcProcedures() []rpc.Procedure {
	m := &messageService{d: d}
	recovery := &opsService{d: d}
	routes := &routeService{d: d}
	suggestions := &suggestRPC{s: &SuggestService{}, inspect: d.cfg.Inspect}
	settings := &configRPC{}
	eventRPC := newEventService(d.hub, d.admitEvents, releaseContext)
	name := daemonv1.MessageServiceName
	return append(eventRPC.procedures(), []rpc.Procedure{
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
		rpc.Unary(daemonv1.OpsServiceName, "Undo", recovery.Undo),
		rpc.Unary(daemonv1.OpsServiceName, "Export", recovery.Export),
		rpc.Unary(daemonv1.OpsServiceName, "Import", recovery.Import),
		rpc.Unary(daemonv1.OpsServiceName, "Records", recovery.Records),
		rpc.Unary(daemonv1.RouteServiceName, "Up", routes.Up),
		rpc.Unary(daemonv1.RouteServiceName, "Get", routes.Get),
		rpc.Unary(daemonv1.RouteServiceName, "List", routes.List),
		rpc.Unary(configv1.ConfigServiceName, "Export", settings.Export),
		rpc.Unary(configv1.ConfigServiceName, "Get", settings.Get),
		rpc.Unary(configv1.ConfigServiceName, "Set", settings.Set),
		rpc.Unary(configv1.ConfigServiceName, "Show", settings.Show),
		rpc.Unary(configv1.ConfigServiceName, "Unset", settings.Unset),
		rpc.Unary(configv1.ConfigServiceName, "Validate", settings.Validate),
		rpc.Unary(suggestv1.SuggestServiceName, "Search", suggestions.Search),
		rpc.Unary(suggestv1.SuggestServiceName, "Get", suggestions.Get),
		rpc.Unary(suggestv1.SuggestServiceName, "Add", suggestions.Add),
		rpc.Unary(suggestv1.SuggestServiceName, "Plan", suggestions.Plan),
		rpc.Unary(suggestv1.SuggestServiceName, "Sources", suggestions.Sources),
		rpc.Unary(suggestv1.SuggestServiceName, "SourceAdd", suggestions.SourceAdd),
		rpc.Unary(suggestv1.SuggestServiceName, "SourceRemove", suggestions.SourceRemove),
		rpc.Unary(suggestv1.SuggestServiceName, "SourcePreview", suggestions.SourcePreview),
		rpc.Unary(suggestv1.SuggestServiceName, "SourcePin", suggestions.SourcePin),
		rpc.Unary(daemonv1.RouteServiceName, "Pause", routes.Pause),
		rpc.Unary(daemonv1.RouteServiceName, "Resume", routes.Resume),
		rpc.Unary(daemonv1.RouteServiceName, "Stop", routes.Stop),
		rpc.Unary(daemonv1.RouteServiceName, "Delete", routes.Delete),
	}...)
}
