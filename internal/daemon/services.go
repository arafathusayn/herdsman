package daemon

import (
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
)

// rpcProcedures lists every RPC method the daemon serves.
func (d *Daemon) rpcProcedures() []rpc.Procedure {
	message := &messageService{d: d}
	suggestions := &suggestRPC{s: &SuggestService{}}
	return []rpc.Procedure{
		rpc.Unary(daemonv1.MessageServiceName, "Status", message.Status),
		rpc.Unary(suggestv1.SuggestServiceName, "Search", suggestions.Search),
		rpc.Unary(suggestv1.SuggestServiceName, "Get", suggestions.Get),
		rpc.Unary(suggestv1.SuggestServiceName, "Add", suggestions.Add),
		rpc.Unary(suggestv1.SuggestServiceName, "Plan", suggestions.Plan),
		rpc.Unary(suggestv1.SuggestServiceName, "Sources", suggestions.Sources),
		rpc.Unary(suggestv1.SuggestServiceName, "SourceAdd", suggestions.SourceAdd),
		rpc.Unary(suggestv1.SuggestServiceName, "SourceRemove", suggestions.SourceRemove),
		rpc.Unary(suggestv1.SuggestServiceName, "SourcePreview", suggestions.SourcePreview),
		rpc.Unary(suggestv1.SuggestServiceName, "SourcePin", suggestions.SourcePin),
	}
}
