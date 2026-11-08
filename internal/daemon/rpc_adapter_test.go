package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ShutdownRequest is the old shape of a Shutdown call.
type ShutdownRequest struct{ Reason string }

// Do lets the behaviour tests name a call by its old method and path while it
// runs over RPC, with the old status for the outcome. It returns the status a
// test can compare, and the failure as the commands see it.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) (int, error) {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	var result any
	status := 200
	err := func() (err error) {
		take := func(res any, e error) error { result = res; return e }
		switch {
		case method == "GET" && path == "/v1/health":
			return take(c.Health(ctx))
		case method == "POST" && path == "/v1/shutdown":
			status = 202
			return c.Shutdown(ctx, in.(ShutdownRequest).Reason)
		case method == "GET" && path == "/v1/routes":
			routes, e := c.ListRoutes(ctx)
			return take(map[string][]RouteInfo{"routes": routes}, e)
		case method == "POST" && path == "/v1/routes":
			status = 201
			return take(c.CreateRoute(ctx, in.(CreateRequest)))
		case parts[0] != "routes" || len(parts) < 2:
			panic("no test route for " + method + " " + path)
		}
		route, rest := parts[1], strings.Join(parts[2:], "/")
		switch {
		case method == "DELETE" && rest == "":
			return take(map[string]bool{"removed": true, "inbox_kept": true}, c.RemoveRoute(ctx, route))
		case rest == "adopt":
			return take(c.AdoptRoute(ctx, route, in.(AdoptRequest)))
		case rest == "reset-events":
			return take(c.ResetEvents(ctx, route, in.(ResetRequest)))
		case rest == "status":
			return take(c.Status(ctx, route))
		case rest == "setup" && method == "GET":
			return take(c.GetSetup(ctx, route))
		case rest == "setup":
			return take(c.PutSetup(ctx, route, in.(SetupRequest)))
		case rest == "enroll":
			status = 202
			sum, e := c.Enroll(ctx, route, in.(EnrollRequest))
			return take(map[string]string{"pending_sha256": sum}, e)
		case rest == "pair/approve":
			return take(c.ApprovePair(ctx, route, in.(ApproveRequest)))
		case rest == "pair/retire":
			q := in.(RetireRequest)
			return take(map[string]any{"retired": q.Name, "revoked": true}, c.RetirePair(ctx, route, q))
		case rest == "messages":
			res, e := c.Send(ctx, route, in.(SendRequest))
			if e == nil && !res.Duplicate {
				status = 201
			}
			return take(res, e)
		case rest == "claims":
			return take(c.Claim(ctx, route, in.(ClaimRequest)))
		case rest == "authority":
			return take(c.Authority(ctx, route))
		case strings.HasPrefix(rest, "credentials/"):
			return take(map[string]bool{"revoked": true}, c.RevokeCredential(ctx, route, parts[3]))
		case strings.HasPrefix(rest, "holds/") && method == "PUT":
			return take(c.Hold(ctx, route, parts[3], in.(HoldRequest).Reason))
		case strings.HasPrefix(rest, "holds/"):
			return take(c.Unhold(ctx, route, parts[3]))
		case strings.HasPrefix(rest, "claims/") && strings.HasSuffix(rest, "/acks"):
			res, e := c.Ack(ctx, route, parts[3], in.(AckRequest))
			if e == nil && !res.Already {
				status = 201
			}
			return take(res, e)
		case strings.HasPrefix(rest, "claims/") && method == "DELETE":
			return take(map[string]bool{"released": true}, c.Release(ctx, route, parts[3]))
		}
		panic(fmt.Sprintf("no test route for %s %s", method, path))
	}()
	if err != nil {
		if a, ok := AsAPIError(err); ok {
			return a.Status, err
		}
		return 0, err
	}
	if out != nil {
		b, merr := json.Marshal(result)
		if merr != nil {
			return status, merr
		}
		if merr := json.Unmarshal(b, out); merr != nil {
			return status, merr
		}
	}
	return status, nil
}
