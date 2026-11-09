package wtree_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

func listFull(t *testing.T, w *world) daemon.WorktreeListResponse {
	t.Helper()
	list, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: w.repo, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestFullListFillsEveryCell(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "one")
	os.WriteFile(filepath.Join(made.Path, "blob"), make([]byte, 4096), 0o644)
	os.WriteFile(filepath.Join(made.Path, "dirty"), nil, 0o644)
	git(t, made.Path, "push", "-q", "-u", "origin", "HEAD")
	git(t, made.Path, "commit", "-q", "--allow-empty", "-m", "local")
	if _, err := w.service.ResourceAdd(context.Background(), daemon.WorktreeResourceAddRequest{Dir: made.Path, Task: "one", Execution: "e", Kind: "port", ID: "4000"}); err != nil {
		t.Fatal(err)
	}
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, `[{"number":7,"headRefName":"feat/one","reviewDecision":"APPROVED","statusCheckRollup":[{"conclusion":"SUCCESS"},{"conclusion":"FAILURE"}]}]`)
	server := herdrtest.Start(t)
	w.service.HerdrSocket = server.Socket
	server.Handle("pane.list", func(map[string]any) (any, string) {
		return map[string]any{"panes": []map[string]any{{"pane_id": "p1", "cwd": made.Path}}}, ""
	})
	server.Handle("agent.list", func(map[string]any) (any, string) {
		return map[string]any{"agents": []map[string]any{{"name": "worker", "pane_id": "p1"}}}, ""
	})

	list := listFull(t, w)
	got := row(t, list.Rows, "one")
	if got.Remote != "origin/feat/one" || got.RemoteAhead != 1 || got.RemoteBehind != 0 {
		t.Fatalf("remote: %+v", got)
	}
	if got.Agent != "worker" || got.PR != 7 || got.Review != "APPROVED" || got.CI != "failing" {
		t.Fatalf("service cells: %+v", got)
	}
	if got.PortsHeld != 1 || got.Port == 0 || got.DiskBytes < 4096 {
		t.Fatalf("local cells: %+v", got)
	}
	if list.Summary.Worktrees != 2 || list.Summary.Dirty != 1 {
		t.Fatalf("%+v", list.Summary)
	}
}

func TestFullListSaysWhatItCouldNotRead(t *testing.T) {
	w := newWorld(t)
	w.create(t, "one")
	w.service.Repo = "acme/repo"
	path := filepath.Join(w.root, "bad-gh")
	os.WriteFile(path, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	w.service.GH = path
	if list := listFull(t, w); len(list.Warnings) == 0 {
		t.Fatalf("%+v", list)
	}
}

func TestFullListWithoutGhIsQuiet(t *testing.T) {
	w := newWorld(t)
	w.create(t, "one")
	w.service.Repo = "acme/repo"
	if list := listFull(t, w); len(list.Warnings) != 0 {
		t.Fatalf("%v", list.Warnings)
	}
}

func TestPlainListAsksNoService(t *testing.T) {
	w := newWorld(t)
	w.create(t, "one")
	w.service.Repo = "acme/repo"
	marker := filepath.Join(w.root, "asked")
	path := filepath.Join(w.root, "gh")
	os.WriteFile(path, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	w.service.GH = path
	if _, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: w.repo}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a plain list called GitHub")
	}
}

func TestSkeletonListHasNamesAndNoCollectedCells(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "one")
	git(t, made.Path, "commit", "-q", "--allow-empty", "-m", "work")
	list, err := w.service.List(context.Background(), daemon.WorktreeListRequest{Dir: w.repo, Skeleton: true})
	if err != nil {
		t.Fatal(err)
	}
	got := row(t, list.Rows, "one")
	if got.Branch != "feat/one" || got.Subject != "" || got.HasBase {
		t.Fatalf("%+v", got)
	}
}
