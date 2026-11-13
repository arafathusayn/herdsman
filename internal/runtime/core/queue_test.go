package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
)

func TestCodexQueueRequiresProcessSessionVersionAndClaimGates(t *testing.T) {
	ctx := testCtx(t)
	w := newWorld(t)
	_, e := w.up("r", "worker", "implementer", "codex")
	e.Cwd = w.dir
	e.Observation.Process = runtime.ProcessLive
	e.Observation.Readiness = runtime.ReadinessReady
	e.Observation.Visibility = runtime.VisibilityVerified
	e.Observation.Program = runtime.ProgramDone
	e.Conversation = &runtime.Conversation{Value: sessA, Provenance: core.ProvenanceCodexRollout}
	e, err := w.records.Put(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	roots := core.SessionRootsOf(w.dir, "")
	w.p.Sessions = &roots
	binary := filepath.Join(w.dir, "codex")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo codex-cli 0.164.0; exit; fi\nprintf '%s\\n' \"$@\" > \"$HOME/queued\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	w.procs.Put(e.Evidence.PID, w.holder, binary, "--no-daemon")
	svc := notice.New(notice.NewMemStore(), coretest.Executions{Records: w.records}, notice.Config{
		Attended: func(string) bool { return true }, Queued: func(string) bool { return true },
		Queue: func(ctx context.Context, e runtime.Execution, line string) error {
			return w.p.QueueCodex(ctx, e, line, map[string]string{"HOME": w.dir, "PATH": "/usr/bin:/bin"})
		},
	})
	defer svc.Close()
	w.p.Notices = svc
	if err := svc.Approve(e, ""); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Issue(ctx, notice.IssueRequest{Key: "task", Kind: notice.KindBrief, Execution: e.ID, Content: "private task"})
	if err != nil {
		t.Fatal(err)
	}
	transcript := codexRollout(t, roots.Codex, e.Cwd, sessA, time.Now().Add(time.Second))
	// A matching cwd alone is not authority to queue to a conversation.
	if err := svc.QueueWake(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "queued")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("queued without session evidence")
	}
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("{\"notice\":\"" + n.ID + "\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.QueueWake(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(w.dir, "queued"))
	if err != nil || string(got) != "queue\n--thread\n"+sessA+"\n--message\n"+notice.QueueLine+"\n" {
		t.Fatalf("queue args %q: %v", got, err)
	}
	if w.core.Calls(hc.MethodSend) != 0 || w.core.Calls("terminal.keys") != 0 {
		t.Fatal("queue wrote terminal input")
	}
	if err := os.Remove(filepath.Join(w.dir, "queued")); err != nil {
		t.Fatal(err)
	}
	if err := svc.ObserveCall(e); err != nil {
		t.Fatal(err)
	}
	// Refuse an unqualified binary, then a replacement at the core address.
	if err := os.WriteFile(binary, []byte(strings.ReplaceAll(script, "0.164.0", "0.999.0")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := svc.QueueWake(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "queued")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("queued to unqualified version")
	}
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	w.setProcess(e.TerminalAddress, w.procs.Add(w.holder, "shell"))
	if err := svc.QueueWake(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "queued")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("queued to replaced execution")
	}
}
