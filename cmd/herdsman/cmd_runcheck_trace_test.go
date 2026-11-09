package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

func TestCheckNameKeepsOnlyTheProgramAndItsFirstWord(t *testing.T) {
	cases := map[string][]string{
		"go test":    {"env", "GOMAXPROCS=1", "go", "test", "-p", "1", "./..."},
		"make check": {"make", "check", "GOFLAGS=-mod=vendor"},
		"true":       {"true"},
		"":           {"env", "A=1"},
	}
	for want, args := range cases {
		if got := checkName(args); got != want {
			t.Errorf("%v: %q, want %q", args, got, want)
		}
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
	for _, want := range []string{`"name": "make check"`, `"name": "check"`, `"exit": 3`} {
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
