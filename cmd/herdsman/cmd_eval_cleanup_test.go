package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

type cleanupEvalRoutes struct {
	scriptedRoutes
	stopContext error
	seen        *daemonv1.UpRequest
}

func (s *cleanupEvalRoutes) Up(ctx context.Context, req *daemonv1.UpRequest) (*daemonv1.UpResponse, error) {
	s.seen = req
	return s.scriptedRoutes.Up(ctx, req)
}
func (s *cleanupEvalRoutes) Stop(ctx context.Context, req *daemonv1.RouteServiceStopRequest) (*daemonv1.RouteServiceStopResponse, error) {
	s.stopContext = ctx.Err()
	return s.scriptedRoutes.Stop(ctx, req)
}

func TestEvalCoreSelectionDoesNotStartTUI(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // No TUI executable is available.
	service := &cleanupEvalRoutes{scriptedRoutes: scriptedRoutes{folders: map[string]string{}, agent: func(string, string, string, string) {}}}
	r := evalRunner{routes: &evalRoutes{service: service, socket: "must-not-leak", kinds: []string{"legacy"}, selectBackend: func(context.Context, string, string) (string, error) { return backendCore, nil }}}
	if _, err := r.startEvalRoute(t.Context(), &corev1.RouteId{Value: "eval"}, t.TempDir(), evalArm{Command: "codex", Kind: "codex"}, evalTask{}); err != nil {
		t.Fatal(err)
	}
	if service.seen.GetHerdrSocket() != "" || len(service.seen.GetKinds()) != 0 {
		t.Fatalf("legacy fields in core request: %v", service.seen)
	}
}

func TestEvalStopsRouteAfterCheckLogFailure(t *testing.T) {
	folder := t.TempDir()
	service := &cleanupEvalRoutes{scriptedRoutes: scriptedRoutes{folders: map[string]string{}, agent: func(repo, _, _, _ string) {
		_ = os.WriteFile(filepath.Join(repo, evalDoneFile), nil, 0600)
		_ = os.Mkdir(filepath.Join(filepath.Dir(repo), "check.log"), 0700)
	}}}
	r := evalRunner{folder: folder, timeout: time.Second, service: daemon.NewTraceService(false), routes: &evalRoutes{service: service}}
	err := r.runRoute(t.Context(), evalArm{Name: "a", Command: "codex", Kind: "codex"}, evalTask{ID: "t", Check: "true"}, "")
	if err == nil || service.stopped.Load() != 1 {
		t.Fatalf("error %v, stops %d", err, service.stopped.Load())
	}
}

func TestEvalStopsRouteWithFreshContextAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &cleanupEvalRoutes{scriptedRoutes: scriptedRoutes{folders: map[string]string{}, agent: func(string, string, string, string) { cancel() }}}
	r := evalRunner{folder: t.TempDir(), timeout: time.Second, routes: &evalRoutes{service: service}}
	err := r.runRoute(ctx, evalArm{Name: "a", Command: "codex", Kind: "codex"}, evalTask{ID: "t", Check: "true"}, "")
	if !errors.Is(err, context.Canceled) || service.stopped.Load() != 1 || service.stopContext != nil {
		t.Fatalf("error %v, stops %d, cleanup context %v", err, service.stopped.Load(), service.stopContext)
	}
}
