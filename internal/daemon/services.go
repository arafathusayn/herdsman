package daemon

import (
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// rpcProcedures lists every RPC method the daemon serves.
func (d *Daemon) rpcProcedures() []rpc.Procedure {
	message := &messageService{d: d}
	recovery := &opsService{d: d}
	routes := &routeService{d: d}
	return []rpc.Procedure{
		rpc.Unary(daemonv1.MessageServiceName, "Status", message.Status),
		rpc.Unary(daemonv1.OpsServiceName, "Undo", recovery.Undo),
		rpc.Unary(daemonv1.OpsServiceName, "Export", recovery.Export),
		rpc.Unary(daemonv1.OpsServiceName, "Import", recovery.Import),
		rpc.Unary(daemonv1.OpsServiceName, "Records", recovery.Records),
		rpc.Unary(daemonv1.RouteServiceName, "Up", routes.Up),
		rpc.Unary(daemonv1.RouteServiceName, "Get", routes.Get),
		rpc.Unary(daemonv1.RouteServiceName, "List", routes.List),
	}
}
