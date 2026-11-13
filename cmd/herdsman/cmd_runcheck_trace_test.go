package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

func TestCheckNameIsOnlyAProgramLabel(t *testing.T) {
	cases := []struct {
		want string
		args []string
	}{
		{"go", []string{"env", "GOMAXPROCS=1", "go", "test", "-p", "1", "./..."}},
		{"make", []string{"make", "check", "TOKEN=secret"}},
		{"sh", []string{"/bin/sh", "-c", "printf PRIVATE_TOKEN_123"}},
		{"bash", []string{"bash", "-c", "curl -H 'Authorization: x' y"}},
		{"true", []string{"/usr/bin/true"}},
		{"check", []string{"env", "A=1"}},
		{"check", []string{"env", "-u", "SECRET_NAME", "go", "test"}},
		{"check", []string{"env", "-S", "go test"}},
		{"check", []string{"a program with spaces"}},
		{"check", []string{"PASSWORD=x"}},
		{"check", nil},
	}
	for _, c := range cases {
		if got := checkName(c.args); got != c.want {
			t.Errorf("%v: %q, want %q", c.args, got, c.want)
		}
	}
}

func TestACheckSpanHoldsNoShellText(t *testing.T) {
	route := t.TempDir()
	t.Setenv("HERDSMAN_ROUTE_FOLDER", route)
	recordCheckSpan([]string{"/bin/sh", "-c", "printf PRIVATE_TOKEN_123"}, time.Now(), 0, &bytes.Buffer{})
	_, out, _ := runTraceArgs(route, "--json")
	if strings.Contains(out, "PRIVATE_TOKEN_123") || !strings.Contains(out, `"name": "sh"`) {
		t.Fatal(out)
	}
}

func TestACheckInARouteAddsASpan(t *testing.T) {
	route := t.TempDir()
	t.Setenv("HERDSMAN_ROUTE_FOLDER", route)
	var stderr bytes.Buffer
	recordCheckSpan([]string{"env", "A=1"}, time.Now(), 3, &stderr)
	recordCheckSpan([]string{"make", "check"}, time.Now(), 0, &stderr)
	if stderr.Len() != 0 {
		t.Fatal(stderr.String())
	}
	code, out, errOut := runTraceArgs(route, "--json")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	for _, want := range []string{`"name": "make"`, `"name": "check"`, `"exit": 3`} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in %s", want, out)
		}
	}
}

func TestACheckOutsideARouteAddsNothing(t *testing.T) {
	t.Setenv("HERDSMAN_ROUTE_FOLDER", "")
	saved := dialTrace
	t.Cleanup(func() { dialTrace = saved })
	dialTrace = func() (tracev1.TraceService, func(), error) {
		t.Fatal("dialled the daemon for a check outside a route")
		return nil, nil, nil
	}
	recordCheckSpan([]string{"make"}, time.Now(), 0, &bytes.Buffer{})
}

func TestACheckWarnsWhenItsSpanCannotBeRecorded(t *testing.T) {
	t.Setenv("HERDSMAN_ROUTE_FOLDER", t.TempDir())
	saved := dialTrace
	t.Cleanup(func() { dialTrace = saved })
	dialTrace = func() (tracev1.TraceService, func(), error) { return nil, nil, errors.New("down") }
	var stderr bytes.Buffer
	recordCheckSpan([]string{"make"}, time.Now(), 0, &stderr)
	if !strings.Contains(stderr.String(), "no record of this check: down") {
		t.Fatal(stderr.String())
	}
}
