package hc_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hclive"
)

// The remote records of the Events gate (spec section 17) against two
// private real cores. Core A reaches core B as machine "remote" with
// `hc machine add --via`, a local command in place of ssh. One terminal is
// started through A (`hc run --on remote`), one on B directly. A's
// subscription and terminal.list show both as remote records, and their
// label changes and their ends arrive on A's stream with the address kept.
// Core's own payloads are written to the test log.
//
// hc 0.1.20 sends no "machine" field in a terminal record, an event or a
// snapshot: a remote record is known by its address, "<machine>/t<n>".
// terminal.list also names the machines added. The test accepts a later
// core that adds the field, as long as it names the machine.
func TestLiveRemoteRecords(t *testing.T) {
	a := hclive.Start(t, hclive.Options{})
	b := hclive.Start(t, hclive.Options{})
	a.AddMachine(b, "remote")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := hc.Dial(ctx, a.Socket, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	labels := map[string]string{"herdsman.live": "remote"}
	s, err := hc.Subscribe(ctx, a.Socket, hc.Filter{Labels: labels}, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	next := func(what string, cond func() bool) {
		t.Helper()
		for !cond() {
			u, err := s.Next(ctx)
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			if u.Kind == hc.UpdateEvent {
				t.Logf("core A event: seq %d terminal %s type %s data %s", u.Seq, u.Event.Terminal, u.Event.Type, u.Event.Data)
			}
		}
	}
	address := func(out []byte, err error) string {
		t.Helper()
		var r struct {
			Address string `json:"address"`
		}
		if err != nil || json.Unmarshal(out, &r) != nil || r.Address == "" {
			t.Fatalf("hc run: %v\n%s", err, out)
		}
		return r.Address
	}

	// A local terminal on A, for contrast.
	local, err := c.Run(ctx, hc.RunRequest{Argv: []string{"/bin/sleep", "300"}, Labels: labels, Name: "rt-local"})
	if err != nil {
		t.Fatal(err)
	}
	if remoteOf(local.Address) != "" {
		t.Fatalf("a local terminal has the address %s", local.Address)
	}
	// Started through A on the remote machine.
	out, err := a.HC("run", "--on", "remote", "--bg", "--name", "rt-via-a", "--label", "herdsman.live=remote", "--json", "/bin/sleep", "300")
	t.Logf("core A's answer to hc run --on remote: %s", out)
	viaA := address(out, err)
	// Started on B directly; A shows it under the machine's name.
	out, err = b.HC("run", "--bg", "--name", "rt-on-b", "--label", "herdsman.live=remote", "--json", "/bin/sleep", "300")
	t.Logf("core B's answer to hc run: %s", out)
	onB := "remote/" + address(out, err)
	for _, addr := range []string{viaA, onB} {
		if remoteOf(addr) != "remote" {
			t.Fatalf("the remote terminal has the address %s", addr)
		}
		next("the remote terminal "+addr+" on A's stream", func() bool { _, ok := s.Terminal(addr); return ok })
		raw, err := a.GetRaw(addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("core A's terminal.get %s: %s", addr, mustJSON(raw))
		checkMachineField(t, addr, raw)
		got, err := c.Get(ctx, addr)
		if err != nil || got.Address != addr || !got.Live() {
			t.Fatalf("get %s = %+v, %v", addr, got, err)
		}
	}

	// terminal.list on A: both remote records and the local one, and the
	// machine itself.
	ls, err := c.List(ctx, labels, false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, term := range ls.Terminals {
		seen[term.Address] = true
	}
	if !seen[local.Address] || !seen[viaA] || !seen[onB] || len(ls.Terminals) != 3 {
		t.Fatalf("terminal.list = %+v", ls.Terminals)
	}
	rawList, err := a.ListRaw()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("core A's terminal.list: %s", rawList)
	var list struct {
		Machines []struct {
			Name  string `json:"name"`
			State string `json:"state"`
			Via   bool   `json:"via"`
		} `json:"machines"`
		Terminals []map[string]any `json:"terminals"`
	}
	if err := json.Unmarshal(rawList, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Machines) != 1 || list.Machines[0].Name != "remote" || list.Machines[0].State != "connected" {
		t.Fatalf("machines = %+v", list.Machines)
	}
	for _, term := range list.Terminals {
		addr, _ := term["address"].(string)
		if addr != local.Address {
			checkMachineField(t, addr, term)
		} else if m, ok := term["machine"]; ok && m != nil {
			t.Fatalf("the local terminal names machine %v", m)
		}
	}

	// A label change through A, and one made on B: each arrives on A's
	// stream for the remote address.
	if _, err := c.Label(ctx, viaA, map[string]string{"herdsman.role": "probe"}); err != nil {
		t.Fatalf("label %s through A: %v", viaA, err)
	}
	next("the label set through A", func() bool { got, _ := s.Terminal(viaA); return got.Labels["herdsman.role"] == "probe" })
	if out, err := b.HC("label", strings.TrimPrefix(onB, "remote/"), "herdsman.role=direct", "--json"); err != nil {
		t.Fatalf("label on B: %v\n%s", err, out)
	}
	next("the label set on B", func() bool { got, _ := s.Terminal(onB); return got.Labels["herdsman.role"] == "direct" })
	// A removed label is a membership change, for a remote record too.
	if _, err := c.Label(ctx, onB, map[string]string{"herdsman.live": ""}); err != nil {
		t.Fatal(err)
	}
	next("the remote terminal leaving by label", func() bool { _, ok := s.Terminal(onB); return !ok })
	if _, err := c.Label(ctx, onB, labels); err != nil {
		t.Fatal(err)
	}
	next("the remote terminal joining by label", func() bool { _, ok := s.Terminal(onB); return ok })

	// The ends: one killed through A, one on B.
	if _, err := c.Kill(ctx, viaA); err != nil {
		t.Fatalf("kill %s through A: %v", viaA, err)
	}
	next("the end of the terminal killed through A", func() bool { got, ok := s.Terminal(viaA); return ok && !got.Live() })
	if out, err := b.HC("kill", strings.TrimPrefix(onB, "remote/"), "--json"); err != nil {
		t.Fatalf("kill on B: %v\n%s", err, out)
	}
	next("the end of the terminal killed on B", func() bool { got, ok := s.Terminal(onB); return ok && !got.Live() })
	for _, addr := range []string{viaA, onB} {
		got, _ := s.Terminal(addr)
		if got.Address != addr || got.State != "finished" {
			t.Fatalf("the finished remote record = %+v", got)
		}
		raw, err := a.GetRaw(addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("core A's finished record %s: %s", addr, mustJSON(raw))
		checkMachineField(t, addr, raw)
	}
	if got, ok := s.Terminal(local.Address); !ok || !got.Live() {
		t.Fatalf("the local terminal = %+v", got)
	}
	// The remote record that joined by label came back whole from Get.
	for _, addr := range s.Incomplete() {
		if _, err := c.Get(ctx, addr); err != nil {
			t.Fatalf("get the incomplete %s: %v", addr, err)
		}
	}
}

// remoteOf is the machine a core address names, empty for this core's own.
func remoteOf(address string) string {
	m, _, ok := strings.Cut(address, "/")
	if !ok {
		return ""
	}
	return m
}

// checkMachineField accepts a remote record with no machine field (hc
// 0.1.20) or one that names the machine of its address.
func checkMachineField(t *testing.T, address string, raw map[string]any) {
	t.Helper()
	m, ok := raw["machine"]
	switch {
	case !ok:
		t.Logf("%s: no machine field; the address names machine %q", address, remoteOf(address))
	case m != remoteOf(address):
		t.Fatalf("%s: machine %v", address, m)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(b)
}
