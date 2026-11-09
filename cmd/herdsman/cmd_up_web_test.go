package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// webStandIn is an executable that counts its starts, prints body and exits with code.
func webStandIn(t *testing.T, dir, body string, code int) (exe, count string) {
	t.Helper()
	exe, count = filepath.Join(dir, "web-stand-in"), filepath.Join(dir, "starts")
	script := fmt.Sprintf("#!/bin/sh\necho x >> %q\nprintf '%%s\\n' %q\nexit %d\n", count, body, code)
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return exe, count
}

func webStarts(t *testing.T, count string) int {
	t.Helper()
	b, err := os.ReadFile(count)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "x")
}

func listening(t *testing.T) (string, net.Listener) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return "http://" + l.Addr().String() + "/#t=abc", l
}

func TestUpWebPrintsTheNewAddress(t *testing.T) {
	dir := t.TempDir()
	url, _ := listening(t)
	exe, count := webStandIn(t, dir, url, 0)
	var out bytes.Buffer
	if err := upWebProcess(exe, dir, "inbox", 5*time.Second, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), url) || webStarts(t, count) != 1 {
		t.Fatalf("out %q, starts %d", out.String(), webStarts(t, count))
	}
}

func TestUpWebDoesNotStartASecondProcess(t *testing.T) {
	dir := t.TempDir()
	url, _ := listening(t)
	if err := os.WriteFile(filepath.Join(dir, "web.log"), []byte("serving\n"+url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, count := webStandIn(t, dir, "should not run", 0)
	var out bytes.Buffer
	if err := upWebProcess(exe, dir, "inbox", 5*time.Second, &out); err != nil {
		t.Fatal(err)
	}
	if webStarts(t, count) != 0 || !strings.Contains(out.String(), url) {
		t.Fatalf("out %q, starts %d", out.String(), webStarts(t, count))
	}
}

// An address in the log from an earlier run proves nothing about this one.
func TestUpWebReportsAProcessThatExits(t *testing.T) {
	dir := t.TempDir()
	url, l := listening(t)
	l.Close()
	if err := os.WriteFile(filepath.Join(dir, "web.log"), []byte(url+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, count := webStandIn(t, dir, "address already in use", 1)
	var out bytes.Buffer
	err := upWebProcess(exe, dir, "inbox", 5*time.Second, &out)
	if err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("err = %v, out %q", err, out.String())
	}
	if webStarts(t, count) != 1 || strings.Contains(out.String(), url) {
		t.Fatalf("out %q, starts %d", out.String(), webStarts(t, count))
	}
}

func TestUpWebStartsAfterAnAddressThatIsGone(t *testing.T) {
	dir := t.TempDir()
	stale, l := listening(t)
	l.Close()
	if err := os.WriteFile(filepath.Join(dir, "web.log"), []byte(stale+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, _ := listening(t)
	exe, count := webStandIn(t, dir, fresh, 0)
	var out bytes.Buffer
	if err := upWebProcess(exe, dir, "inbox", 5*time.Second, &out); err != nil {
		t.Fatal(err)
	}
	if webStarts(t, count) != 1 || !strings.Contains(out.String(), fresh) || strings.Contains(out.String(), stale) {
		t.Fatalf("out %q, starts %d", out.String(), webStarts(t, count))
	}
}
