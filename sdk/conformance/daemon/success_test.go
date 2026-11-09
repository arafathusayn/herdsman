package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const (
	paneHeader = "Herdsman-Pane"
	nameHeader = "Herdsman-Name"
)

// scriptedHerdr answers the calls a daemon makes about its panes, from a table
// the test fills, on a socket no real Herdr session shares.
type scriptedHerdr struct {
	socket string
	mu     sync.Mutex
	agents map[string]map[string]any
}

func startHerdr(t *testing.T) *scriptedHerdr {
	t.Helper()
	dir, err := os.MkdirTemp("", "hch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h := &scriptedHerdr{socket: filepath.Join(dir, "h.sock"), agents: map[string]map[string]any{}}
	listener, err := net.Listen("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go h.serve(conn)
		}
	}()
	return h
}

func (h *scriptedHerdr) addPane(pane, session string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agents[pane] = map[string]any{
		"pane_id": pane, "terminal_id": "term-" + pane, "workspace_id": "workspace", "tab_id": "tab",
		"focused": false, "revision": 1, "agent": "claude", "interactive_ready": true, "launch_pending": false,
		"agent_status":  "idle",
		"agent_session": map[string]any{"source": "hook", "agent": "claude", "kind": "id", "value": session},
	}
}

func (h *scriptedHerdr) answer(method string, params map[string]any) (any, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := make([]map[string]any, 0, len(h.agents))
	panes := make([]map[string]any, 0, len(h.agents))
	for _, a := range h.agents {
		list = append(list, a)
		panes = append(panes, map[string]any{"pane_id": a["pane_id"], "tab_id": a["tab_id"], "workspace_id": a["workspace_id"], "focused": true})
	}
	switch method {
	case "ping":
		return map[string]any{"type": "pong", "version": "0.9.4", "protocol": 22}, ""
	case "events.subscribe":
		return map[string]any{"type": "subscription_started"}, ""
	case "agent.get":
		target, _ := params["target"].(string)
		if a, ok := h.agents[target]; ok {
			return map[string]any{"type": "agent_info", "agent": a}, ""
		}
		return nil, "agent_not_found"
	case "agent.list":
		return map[string]any{"type": "agent_list", "agents": list}, ""
	case "pane.list":
		return map[string]any{"type": "pane_list", "panes": panes}, ""
	case "pane.read":
		pane, _ := params["pane_id"].(string)
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": pane, "text": ""}}, ""
	case "notification.show":
		return map[string]any{"type": "notification_show", "shown": true}, ""
	case "agent.prompt", "tab.focus", "pane.report_metadata":
		return map[string]any{"type": "ok"}, ""
	}
	return nil, "unknown_method"
}

func (h *scriptedHerdr) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var req struct {
		ID     string         `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if json.Unmarshal(line, &req) != nil {
		return
	}
	reply := map[string]any{"id": req.ID}
	if result, code := h.answer(req.Method, req.Params); code != "" {
		reply["error"] = map[string]string{"code": code, "message": code}
	} else {
		reply["result"] = result
	}
	data, _ := json.Marshal(reply)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return
	}
	if req.Method == "events.subscribe" {
		_, _ = reader.ReadBytes('\n') // Held open until the daemon hangs up.
	}
}

// caller is one party's way of reaching the daemon.
type caller struct {
	hc      *http.Client
	options []connect.ClientOption
	pane    string
	name    string
	token   string
}

func callAs[Req, Res any](ctx context.Context, c caller, method string, msg *Req) (*Res, error) {
	req := connect.NewRequest(msg)
	if c.pane != "" {
		req.Header().Set(paneHeader, c.pane)
	}
	if c.name != "" {
		req.Header().Set(nameHeader, c.name)
	}
	if c.token != "" {
		req.Header().Set("Authorization", "Bearer "+c.token)
	}
	res, err := connect.NewClient[Req, Res](c.hc, procedure(method), c.options...).CallUnary(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	return res.Msg, nil
}

// fileInbox lays out a file-mode inbox with an approved pair and returns the
// inbox and the registry's checksum.
func fileInbox(t *testing.T, private, name, adj, orch string, herdr *scriptedHerdr) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(private)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "repo-"+name)
	herdr.addPane(adj, adj+"-session")
	herdr.addPane(orch, orch+"-session")
	party := func(role, pane string) string {
		return fmt.Sprintf(`{"role":%q,"identity":{"pane":%q,"session":{"agent":"claude","kind":"id","source":"hook","value":%q}}}`, role, pane, pane+"-session")
	}
	indented, err := json.MarshalIndent(json.RawMessage(fmt.Sprintf(`{"adj-a":%s,"orch-a":%s}`, party("adjutant", adj), party("orchestrator", orch))), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	registry := append(indented, '\n')
	for _, sub := range []string{"state", "ids", "enrolled", "alerts", "adj-a/new", "adj-a/ready", "adj-a/cur", "adj-a/tmp", "adj-a/quarantine", "adj-a/ack", "adj-a/notices", "orch-a/new", "orch-a/ready", "orch-a/cur", "orch-a/tmp", "orch-a/quarantine", "orch-a/ack", "orch-a/notices"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for file, content := range map[string][]byte{
		"peers":          registry,
		"peers.approved": registry,
		"setup.json":     []byte(fmt.Sprintf(`{"inbox":%q,"policy_verified":true,"rules":[],"binary":{"path":"/x","checksum":"1-1"}}`+"\n", dir)),
		"state/sequence": []byte("0\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256(registry)
	return dir, hex.EncodeToString(sum[:])
}

// TestEveryPartyMethodSucceeds drives the methods whose success needs real
// parties, through a stock client in both encodings, and checks both the replies
// and what they did.
func TestEveryPartyMethodSucceeds(t *testing.T) {
	if os.Getenv("HERDSMAN_CONFORMANCE_SOCKET") != "" {
		t.Skip("needs a daemon started against the scripted Herdr")
	}
	herdr := startHerdr(t)
	socket, private := startDaemonWith(t, "HERDR_SOCKET_PATH="+herdr.socket)
	hc := httpClient(t, socket)
	for encoding, options := range map[string][]connect.ClientOption{"proto": nil, "json": {connect.WithProtoJSON()}} {
		t.Run(encoding, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			runParties(ctx, t, hc, options, herdr, private, encoding)
		})
	}
}

func runParties(ctx context.Context, t *testing.T, hc *http.Client, options []connect.ClientOption, herdr *scriptedHerdr, private, encoding string) {
	name := "ok-" + encoding
	admin := caller{hc: hc, options: options}
	created := must[daemonv1.CreateRouteResponse](t)(callAs[daemonv1.CreateRouteRequest, daemonv1.CreateRouteResponse](ctx, admin, "CreateRoute", &daemonv1.CreateRouteRequest{Repo: "repo", Route: route(name), HerdrSocket: herdr.socket}))
	adjName, orchName := "adj-"+encoding, "orch-"+encoding
	adjPane, orchPane := "p1-"+encoding, "p2-"+encoding
	herdr.addPane(adjPane, "s1-"+encoding)
	herdr.addPane(orchPane, "s2-"+encoding)
	adj := caller{hc: hc, options: options, pane: adjPane, name: adjName}
	orch := caller{hc: hc, options: options, pane: orchPane, name: orchName}

	must[daemonv1.EnrollResponse](t)(callAs[daemonv1.EnrollRequest, daemonv1.EnrollResponse](ctx, adj, "Enroll", &daemonv1.EnrollRequest{Route: route(name), Name: adjName, Role: "adjutant"}))
	enrolled := must[daemonv1.EnrollResponse](t)(callAs[daemonv1.EnrollRequest, daemonv1.EnrollResponse](ctx, orch, "Enroll", &daemonv1.EnrollRequest{Route: route(name), Name: orchName, Role: "orchestrator"}))
	if enrolled.GetPendingSha256() == "" {
		t.Fatal("enroll returned no checksum")
	}
	approved := must[daemonv1.ApprovePairResponse](t)(callAs[daemonv1.ApprovePairRequest, daemonv1.ApprovePairResponse](ctx, admin, "ApprovePair", &daemonv1.ApprovePairRequest{Route: route(name), Sha256: enrolled.GetPendingSha256()}))
	if !approved.GetApproved() || len(approved.GetCredentials()) != 2 {
		t.Fatalf("approved %v", approved)
	}
	token := func(party string) string {
		data, err := os.ReadFile(filepath.Join(created.GetInbox(), "state", "credentials", party))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}
	adj.token, orch.token = token(adjName), token(orchName)

	rules := filepath.Join(private, "policy-"+encoding+".sb")
	if err := os.WriteFile(rules, []byte("(allow default)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proof := must[daemonv1.PutSetupResponse](t)(callAs[daemonv1.PutSetupRequest, daemonv1.PutSetupResponse](ctx, admin, "PutSetup", &daemonv1.PutSetupRequest{Route: route(name), Rules: []string{rules}, PolicyVerified: true, BoundaryJson: `{"version":1}`}))
	if proof.GetProofSha256() == "" {
		t.Fatal("setup returned no proof")
	}
	setup := must[daemonv1.GetSetupResponse](t)(callAs[daemonv1.GetSetupRequest, daemonv1.GetSetupResponse](ctx, admin, "GetSetup", &daemonv1.GetSetupRequest{Route: route(name)}))
	if setup.GetProofJson() == "" || len(setup.GetInventory()) == 0 {
		t.Fatalf("setup %v", setup)
	}
	authority := must[daemonv1.AuthorityResponse](t)(callAs[daemonv1.AuthorityRequest, daemonv1.AuthorityResponse](ctx, adj, "Authority", &daemonv1.AuthorityRequest{Route: route(name)}))
	if authority.GetAuthority() == "" {
		t.Fatalf("authority %v", authority)
	}

	sent := must[daemonv1.SendResponse](t)(callAs[daemonv1.SendRequest, daemonv1.SendResponse](ctx, adj, "Send", &daemonv1.SendRequest{Route: route(name), To: orchName, Kind: "brief", Body: "hello", Key: strings.Repeat("a", 32)}))
	if sent.GetId().GetValue() == "" || sent.GetDuplicate() {
		t.Fatalf("sent %v", sent)
	}
	again := must[daemonv1.SendResponse](t)(callAs[daemonv1.SendRequest, daemonv1.SendResponse](ctx, adj, "Send", &daemonv1.SendRequest{Route: route(name), To: orchName, Kind: "brief", Body: "hello", Key: strings.Repeat("a", 32)}))
	if !again.GetDuplicate() || again.GetId().GetValue() != sent.GetId().GetValue() {
		t.Fatalf("retry %v", again)
	}
	preview := must[daemonv1.SendResponse](t)(callAs[daemonv1.SendRequest, daemonv1.SendResponse](ctx, adj, "Send", &daemonv1.SendRequest{Route: route(name), To: orchName, Kind: "brief", Body: "not sent", Key: strings.Repeat("b", 32), DryRun: true}))
	if len(preview.GetPlan().GetSteps()) == 0 || preview.GetId().GetValue() != "" {
		t.Fatalf("preview %v", preview)
	}

	waited := must[daemonv1.WaitResponse](t)(callAs[daemonv1.WaitRequest, daemonv1.WaitResponse](ctx, orch, "Wait", &daemonv1.WaitRequest{Route: route(name), WaitMs: 2000}))
	if len(waited.GetFrames()) == 0 {
		t.Fatalf("wait %v", waited)
	}

	claim := must[daemonv1.ClaimResponse](t)(callAs[daemonv1.ClaimRequest, daemonv1.ClaimResponse](ctx, orch, "Claim", &daemonv1.ClaimRequest{Route: route(name), ReadKey: strings.Repeat("c", 32)}))
	if claim.GetClaim() == "" || len(claim.GetManifest()) != 1 || claim.GetManifest()[0].GetId().GetValue() != sent.GetId().GetValue() {
		t.Fatalf("claim %v", claim)
	}
	acked := must[daemonv1.AckResponse](t)(callAs[daemonv1.AckRequest, daemonv1.AckResponse](ctx, orch, "Ack", &daemonv1.AckRequest{Route: route(name), Claim: claim.GetClaim(), Id: sent.GetId(), Record: "handled"}))
	if acked.GetId().GetValue() != sent.GetId().GetValue() {
		t.Fatalf("ack %v", acked)
	}
	released := must[daemonv1.ReleaseResponse](t)(callAs[daemonv1.ReleaseRequest, daemonv1.ReleaseResponse](ctx, orch, "Release", &daemonv1.ReleaseRequest{Route: route(name), Claim: claim.GetClaim()}))
	if !released.GetReleased() {
		t.Fatalf("release %v", released)
	}

	held := must[daemonv1.HoldResponse](t)(callAs[daemonv1.HoldRequest, daemonv1.HoldResponse](ctx, adj, "Hold", &daemonv1.HoldRequest{Route: route(name), Target: orchName, Reason: "wait"}))
	if _, ok := held.GetHolds()[adjName]; !ok {
		t.Fatalf("hold %v", held)
	}
	unheld := must[daemonv1.UnholdResponse](t)(callAs[daemonv1.UnholdRequest, daemonv1.UnholdResponse](ctx, adj, "Unhold", &daemonv1.UnholdRequest{Route: route(name), Target: orchName}))
	if _, ok := unheld.GetHolds()[adjName]; ok {
		t.Fatalf("unhold %v", unheld)
	}

	revoked := must[daemonv1.RevokeCredentialResponse](t)(callAs[daemonv1.RevokeCredentialRequest, daemonv1.RevokeCredentialResponse](ctx, admin, "RevokeCredential", &daemonv1.RevokeCredentialRequest{Route: route(name), Name: adjName}))
	if !revoked.GetRevoked() {
		t.Fatalf("revoke %v", revoked)
	}
	_, err := callAs[daemonv1.SendRequest, daemonv1.SendResponse](ctx, adj, "Send", &daemonv1.SendRequest{Route: route(name), To: orchName, Kind: "brief", Body: "late", Key: strings.Repeat("d", 32)})
	if connect.CodeOf(errors.Unwrap(err)) != connect.CodeUnauthenticated {
		t.Fatalf("a revoked credential sent: %v", err)
	}
	peers, err := os.ReadFile(filepath.Join(created.GetInbox(), "peers"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(peers)
	retired := must[daemonv1.RetirePairResponse](t)(callAs[daemonv1.RetirePairRequest, daemonv1.RetirePairResponse](ctx, admin, "RetirePair", &daemonv1.RetirePairRequest{Route: route(name), Name: orchName, Sha256: hex.EncodeToString(sum[:])}))
	if retired.GetRetired() != orchName || !retired.GetRevoked() {
		t.Fatalf("retire %v", retired)
	}

	adoptName := "old-" + encoding
	inbox, confirm := fileInbox(t, private, adoptName, "pa-"+encoding, "pb-"+encoding, herdr)
	adopted := must[daemonv1.AdoptRouteResponse](t)(callAs[daemonv1.AdoptRouteRequest, daemonv1.AdoptRouteResponse](ctx, admin, "AdoptRoute", &daemonv1.AdoptRouteRequest{Route: route(adoptName), Inbox: inbox, ConfirmPairSha256: confirm}))
	if adopted.GetRoute().GetValue() != adoptName || adopted.GetEpoch() == "" || !adopted.GetLegacyProof() {
		t.Fatalf("adopt %v", adopted)
	}
	if _, err := os.Stat(filepath.Join(inbox, "state", "credentials", "adj-a")); err != nil {
		t.Fatalf("adoption issued no credential: %v", err)
	}
}
