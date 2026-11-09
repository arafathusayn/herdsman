package wtree_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	os.MkdirAll(filepath.Join(w.repo, ".git", "info"), 0o755)
	os.WriteFile(filepath.Join(w.repo, ".git", "info", "exclude"), []byte("blob\n"), 0o644)
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
	if got.PortsHeld != 1 || got.Port == 0 || got.DiskBytes != 4096 {
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

func TestDiskCellCountsOnlyIgnoredFiles(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "one")
	os.WriteFile(filepath.Join(made.Path, "tracked"), make([]byte, 8192), 0o644)
	git(t, made.Path, "add", "tracked")
	git(t, made.Path, "commit", "-q", "-m", "a large tracked file")
	os.WriteFile(filepath.Join(made.Path, "untracked"), make([]byte, 512), 0o644)
	if got := row(t, listFull(t, w).Rows, "one"); got.DiskBytes != 0 {
		t.Fatalf("no ignored file, but %d bytes", got.DiskBytes)
	}
	os.MkdirAll(filepath.Join(w.repo, ".git", "info"), 0o755)
	os.WriteFile(filepath.Join(w.repo, ".git", "info", "exclude"), []byte("cache/\n"), 0o644)
	os.MkdirAll(filepath.Join(made.Path, "cache", "deep"), 0o755)
	os.WriteFile(filepath.Join(made.Path, "cache", "deep", "a"), make([]byte, 100), 0o644)
	os.WriteFile(filepath.Join(made.Path, "cache", "b"), make([]byte, 28), 0o644)
	if got := row(t, listFull(t, w).Rows, "one"); got.DiskBytes != 128 {
		t.Fatalf("%d bytes", got.DiskBytes)
	}
}

func TestCICellReadsBothKindsOfCheck(t *testing.T) {
	for want, rollup := range map[string]string{
		"failing":   `[{"__typename":"StatusContext","state":"FAILURE"}]`,
		"failing ":  `[{"__typename":"CheckRun","conclusion":"SUCCESS","status":"COMPLETED"},{"__typename":"StatusContext","state":"ERROR"}]`,
		"passing":   `[{"__typename":"StatusContext","state":"SUCCESS"},{"__typename":"CheckRun","conclusion":"NEUTRAL","status":"COMPLETED"}]`,
		"pending":   `[{"__typename":"StatusContext","state":"PENDING"},{"__typename":"CheckRun","conclusion":"SUCCESS","status":"COMPLETED"}]`,
		"pending ":  `[{"__typename":"CheckRun","conclusion":"","status":"IN_PROGRESS"}]`,
		"failing  ": `[{"__typename":"CheckRun","conclusion":"STARTUP_FAILURE","status":"COMPLETED"}]`,
	} {
		w := newWorld(t)
		w.create(t, "one")
		w.service.Repo = "acme/repo"
		w.service.GH = fakeGH(t, w, `[{"number":7,"headRefName":"feat/one","statusCheckRollup":`+rollup+`}]`)
		if got := row(t, listFull(t, w).Rows, "one"); got.CI != strings.TrimSpace(want) {
			t.Errorf("%s: %q", rollup, got.CI)
		}
	}
}
