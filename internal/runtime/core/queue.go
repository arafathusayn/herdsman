package core

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// QueueCodex wakes only a proven local Codex process, at the qualified
// version, through its native queue. Neither terminal input nor task text
// passes through this path. Unknown replies are never safe to retry.
func (p *Provider) QueueCodex(ctx context.Context, want runtime.Execution, line string, login map[string]string) error {
	notReady := func(reason string) error { return fmt.Errorf("%w: %s", notice.ErrQueueNotReady, reason) }
	if line != notice.QueueLine {
		return notReady("only the fixed wake line is permitted")
	}
	if err := p.mutable(); err != nil {
		return notReady(err.Error())
	}
	e, err := p.execution(ctx, runtime.Target{ExecutionID: want.ID})
	if err != nil {
		return notReady(err.Error())
	}
	if e.HarnessKind != "codex" || e.Evidence != want.Evidence || e.Generation != want.Generation || e.EndpointID != want.EndpointID || e.Desired != runtime.DesiredRunning {
		return notReady("the execution changed before its wake")
	}
	if p.Sessions == nil || p.Sessions.Codex == "" || e.Conversation == nil {
		return notReady("the native conversation has not been recorded")
	}
	all, err := p.Records.List(ctx, "", "")
	if err != nil {
		return notReady(err.Error())
	}
	conv := FindConversation(*p.Sessions, ConversationQuery{Kind: e.HarnessKind, Cwd: e.Cwd, Registered: registeredAt(e),
		Taken: takenConversations(all, e.ID), Marks: p.noticeMarks(e.ID)})
	if conv.Status != ConversationFound || conv.Conversation.Value != e.Conversation.Value || !strings.HasSuffix(conv.Conversation.Provenance, ProvenanceByNotice) {
		return notReady("the recorded native conversation has no notice evidence for this execution")
	}
	table, err := p.procs().List(ctx)
	if err != nil {
		return notReady(err.Error())
	}
	binary := ""
	for _, proc := range table {
		if proc.PID == e.Evidence.PID && proc.Name() == "codex" && len(proc.Argv) > 0 && filepath.IsAbs(proc.Argv[0]) && slices.Contains(proc.Argv, "--no-daemon") {
			binary = proc.Argv[0]
		}
	}
	if binary == "" {
		return notReady("the registered process is not a local codex --no-daemon program")
	}
	var env []string
	for _, key := range launch.LoginKeys {
		if value := login[key]; value != "" {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "CODEX_HOME="+filepath.Dir(p.Sessions.Codex))
	version := exec.CommandContext(ctx, binary, "--version")
	version.Env, version.Dir = env, e.Cwd
	raw, err := version.Output()
	if err != nil || strings.TrimSpace(string(raw)) != "codex-cli 0.164.0" {
		return notReady("Codex queue requires the qualified version 0.164.0")
	}
	// Pin the identity for hello and prove the process again immediately
	// before invoking the harness, after potentially slow transcript reads.
	pin := *p
	pin.ExpectCore = e.EndpointID
	c, err := pin.Dial(ctx)
	if err != nil {
		return notReady(err.Error())
	}
	defer c.Close()
	if _, ok, err := Proven(ctx, c, p.procs(), e); err != nil || !ok {
		return notReady("the terminal no longer holds the registered process")
	}
	if p.Notices == nil || !p.Notices.QueueAllowed(e) {
		return notReady("the notice gate closed before its wake")
	}
	cmd := exec.CommandContext(ctx, binary, "queue", "--thread", e.Conversation.Value, "--message", notice.QueueLine)
	cmd.Env, cmd.Dir = env, e.Cwd
	// Output is not session content and is deliberately not persisted. Any
	// error after Start is uncertain: the durable reservation stays pending.
	if err := cmd.Start(); err != nil {
		return notReady("the queue command could not start: " + err.Error())
	}
	return cmd.Wait()
}
