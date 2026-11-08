package daemon

import (
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// rpcProcedures lists every RPC method the daemon serves.
func (d *Daemon) rpcProcedures() []rpc.Procedure {
	m := &messageService{d: d}
	name := daemonv1.MessageServiceName
	return []rpc.Procedure{
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
		rpc.Unary(name, "Wait", m.Wait),
	}
}
