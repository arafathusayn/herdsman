package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

func start(name, folder string) *daemonv1.RouteStart {
	return &daemonv1.RouteStart{Route: &corev1.RouteId{Value: name}, Folder: folder}
}

func TestMatchRouteByNameFolderAndAt(t *testing.T) {
	starts := []*daemonv1.RouteStart{start("harbor", "/r/.herdsman/harbor-2026-11-09"), start("harbor", "/other/.herdsman/harbor-2026-11-01"), start("quay", "/r/.herdsman/quay-2026-11-09")}
	cases := []struct {
		name, arg, repo, cwd, want string
	}{
		{"name in this repository", "harbor", "/r", "/r", "/r/.herdsman/harbor-2026-11-09"},
		{"absolute folder of another repository", "/other/.herdsman/harbor-2026-11-01", "/r", "/r", "/other/.herdsman/harbor-2026-11-01"},
		{"relative folder", ".herdsman/quay-2026-11-09", "/r", "/r", "/r/.herdsman/quay-2026-11-09"},
		{"at, in the folder", "@", "/r", "/r/.herdsman/quay-2026-11-09/reports", "/r/.herdsman/quay-2026-11-09"},
	}
	for _, c := range cases {
		got, err := matchRoute(starts, c.arg, c.repo, c.cwd)
		if err != nil || got.GetFolder() != c.want {
			t.Errorf("%s: %v %v", c.name, got, err)
		}
	}
	if _, err := matchRoute(starts, "@", "/r", "/r"); err == nil {
		t.Error("@ outside a route folder matched")
	}
	if _, err := matchRoute(starts, "nope", "/r", "/r"); err == nil {
		t.Error("an unknown name matched")
	}
	if _, err := matchRoute(starts, "harbor", "/third", "/third"); err == nil {
		t.Error("a name from another repository matched")
	}
}

func TestParseControlTakesTheRouteBeforeOrAfterFlags(t *testing.T) {
	var stderr bytes.Buffer
	name, f, ok := parseControl(deleteVerb, []string{"harbor", "--force", "-y", "--grace", "5"}, &stderr)
	if !ok || name != "harbor" || !f.force || !f.yes || f.grace != 5 {
		t.Fatalf("%q %+v %v", name, f, ok)
	}
	name, f, ok = parseControl(resumeVerb, []string{"-p", "go on", "harbor"}, &stderr)
	if !ok || name != "harbor" || f.note != "go on" {
		t.Fatalf("%q %+v %v", name, f, ok)
	}
	if _, _, ok = parseControl(pauseVerb, nil, &stderr); ok {
		t.Fatal("no route was accepted")
	}
	if _, _, ok = parseControl(pauseVerb, []string{"a", "b"}, &stderr); ok {
		t.Fatal("two routes were accepted")
	}
	if _, _, ok = parseControl(pauseVerb, []string{"--force", "a"}, &stderr); ok {
		t.Fatal("a delete flag was accepted on pause")
	}
}

type fakeTTY struct {
	io.Reader
	bytes.Buffer
}

func (f *fakeTTY) Read(p []byte) (int, error)  { return f.Reader.Read(p) }
func (f *fakeTTY) Write(p []byte) (int, error) { return f.Buffer.Write(p) }
func (f *fakeTTY) Close() error                { return nil }

func TestConfirmNeedsAYesOnATerminal(t *testing.T) {
	answer := func(line string) func() (io.ReadWriteCloser, error) {
		return func() (io.ReadWriteCloser, error) { return &fakeTTY{Reader: strings.NewReader(line)}, nil }
	}
	if err := confirmVerb("stop", answer("y\n")); err != nil {
		t.Fatal(err)
	}
	if err := confirmVerb("stop", answer("n\n")); err == nil {
		t.Fatal("no was taken as yes")
	}
	if err := confirmVerb("stop", answer("")); err == nil {
		t.Fatal("an empty answer was taken as yes")
	}
	err := confirmVerb("stop", func() (io.ReadWriteCloser, error) { return nil, errors.New("no tty") })
	if err == nil || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("got %v", err)
	}
}

func TestRouteStatusReportsAPendingPair(t *testing.T) {
	s := start("harbor", "/r/.herdsman/harbor-2026-11-09")
	s.Control, s.State = "running", "stopped"
	s.Pair = &daemonv1.PairStatus{Checksum: "abc123"}
	var b bytes.Buffer
	routeStatusText(&b, s)
	if !strings.Contains(b.String(), "pair: pending abc123  <-- PAIR PENDING") {
		t.Fatalf("%s", b.String())
	}
	b.Reset()
	if err := routeStatusJSON(&b, s); err != nil || !strings.Contains(b.String(), `"pending":true`) {
		t.Fatalf("%s %v", b.String(), err)
	}
	s.Pair.Approved = true
	b.Reset()
	routeStatusText(&b, s)
	if strings.Contains(b.String(), "PENDING") || !strings.Contains(b.String(), "pair: approved") {
		t.Fatalf("%s", b.String())
	}
	s.Pair = nil
	b.Reset()
	routeStatusText(&b, s)
	if !strings.Contains(b.String(), "pair: none") {
		t.Fatalf("%s", b.String())
	}
}
