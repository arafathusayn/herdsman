package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/secrets"
)

type mapKeychain map[string]string

func (m mapKeychain) Load(service, account string) (string, error) {
	v, ok := m[service+"/"+account]
	if !ok {
		return "", os.ErrNotExist
	}
	return v, nil
}

func TestWebSecretsRedactEveryLogDestination(t *testing.T) {
	old := log.Writer()
	t.Cleanup(func() { log.SetOutput(old) })
	var stderr bytes.Buffer
	const value = "sk-log-Secret-value"
	getenv := func(string) string { return "T=s/a" }
	r, errLog, err := webSecrets(getenv, mapKeychain{"s/a": value}, &stderr)
	if err != nil || !r.Active() {
		t.Fatalf("%v %v", r, err)
	}
	log.Printf("failed with %s", value)
	if _, err := errLog.Write([]byte("server " + value + "\n")); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); strings.Contains(got, value) || strings.Count(got, secrets.Marker) != 2 {
		t.Fatalf("log = %q", got)
	}
}

func TestWebSecretsFailClosed(t *testing.T) {
	var stderr bytes.Buffer
	if _, _, err := webSecrets(func(string) string { return "T=s/a" }, mapKeychain{}, &stderr); err == nil {
		t.Fatal("started without a listed secret")
	}
}

func TestWebSecretsUnsetChangesNothing(t *testing.T) {
	old := log.Writer()
	t.Cleanup(func() { log.SetOutput(old) })
	var stderr bytes.Buffer
	r, errLog, err := webSecrets(func(string) string { return "" }, mapKeychain{}, &stderr)
	if err != nil || r.Active() || errLog != &stderr || log.Writer() != old {
		t.Fatalf("%v %v %v", r, errLog, err)
	}
}

// printer registers a verb that prints like a pane consumer does.
func printer(t *testing.T, name, text string) {
	t.Helper()
	commands[name] = command{name: name, run: func(_ []string, _ io.Reader, stdout, stderr io.Writer) int {
		mid := len(text) / 2
		fmt.Fprint(stdout, text[:mid])
		fmt.Fprint(stdout, text[mid:]+"\n")
		fmt.Fprintln(stderr, text)
		return 0
	}}
	t.Cleanup(func() { delete(commands, name) })
}

func withVerbKeychain(t *testing.T, refs string, k secrets.Keychain) {
	t.Helper()
	t.Setenv(secrets.RefsVariable, refs)
	old := verbKeychain
	verbKeychain = k
	t.Cleanup(func() { verbKeychain = old })
}

func TestEveryVerbRedactsItsOutput(t *testing.T) {
	const value = "sk-verb-Secret-value"
	printer(t, "print-pane", "fg: token "+value)
	withVerbKeychain(t, "T=s/a", mapKeychain{"s/a": value})
	out, errOut, code := call(t, "print-pane")
	if code != 0 || strings.Contains(out+errOut, value) || !strings.Contains(out, secrets.Marker) || !strings.Contains(errOut, secrets.Marker) {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
}

func TestStatusVerbNeverPrintsAKnownSecret(t *testing.T) {
	const value = "sk-status-Secret-value"
	t.Cleanup(func() { herdr.SetPaneFilter(nil) })
	fake := herdrtest.Start(t)
	fake.Handle("pane.read", func(p map[string]any) (any, string) {
		return map[string]any{"read": map[string]any{"pane_id": p["pane_id"], "text": "Thinking with " + value}}, ""
	})
	t.Setenv("HERDR_SOCKET_PATH", fake.Socket)
	t.Setenv("HERDSMAN_STATUS_SPECS", "im-a:w1:p1:")
	withVerbKeychain(t, "T=s/a", mapKeychain{"s/a": value})
	for _, args := range [][]string{{"status"}, {"status", "--json"}} {
		out, errOut, code := call(t, args...)
		if code != 0 || strings.Contains(out+errOut, value) || !strings.Contains(out, "Thinking") {
			t.Errorf("%v: %d %q %q", args, code, out, errOut)
		}
	}
}

func TestPaneVerbsAreNotExempt(t *testing.T) {
	for _, verb := range []string{"status", "message"} {
		if _, ok := commands[verb]; !ok || rawOutputVerbs[verb] {
			t.Errorf("%s: registered %v, exempt %v", verb, ok, rawOutputVerbs[verb])
		}
	}
}

func TestJSONVerbKeepsItsDelimiters(t *testing.T) {
	const quoted = `value"`
	commands["print-json"] = command{name: "print-json", run: func(_ []string, _ io.Reader, stdout, _ io.Writer) int {
		b, _ := json.Marshal(map[string]string{"a": "value", "b": quoted})
		fmt.Fprintln(stdout, string(b))
		return 0
	}}
	t.Cleanup(func() { delete(commands, "print-json") })
	withVerbKeychain(t, "T=s/a", mapKeychain{"s/a": quoted})
	out, _, _ := call(t, "print-json", "--json")
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["a"] != "value" || got["b"] != secrets.Marker {
		t.Fatalf("%v %q", err, out)
	}
}

func TestVerbRefusesWhenASecretCannotBeRead(t *testing.T) {
	printer(t, "print-pane", "text")
	withVerbKeychain(t, "T=s/a", mapKeychain{})
	var out, errOut bytes.Buffer
	if err := run([]string{"print-pane"}, &out, &errOut); err == nil || out.Len() != 0 {
		t.Fatalf("%v %q", err, out.String())
	}
}

func TestDiagnosticVerbsNeedNoKeychain(t *testing.T) {
	printer(t, "doctor-like", "text")
	rawOutputVerbs["doctor-like"] = true
	t.Cleanup(func() { delete(rawOutputVerbs, "doctor-like") })
	withVerbKeychain(t, "T=s/a", mapKeychain{})
	if out, _, code := call(t, "doctor-like"); code != 0 || !strings.Contains(out, "text") {
		t.Fatalf("%d %q", code, out)
	}
	for _, verb := range []string{"doctor", "daemon"} {
		if !rawOutputVerbs[verb] {
			t.Errorf("%s must keep its real streams", verb)
		}
	}
}

func TestVerbWithoutSecretsGetsItsOwnStreams(t *testing.T) {
	var saw io.Writer
	commands["print-raw"] = command{name: "print-raw", run: func(_ []string, _ io.Reader, stdout, _ io.Writer) int { saw = stdout; return 0 }}
	t.Cleanup(func() { delete(commands, "print-raw") })
	withVerbKeychain(t, "", mapKeychain{})
	var out, errOut bytes.Buffer
	if err := run([]string{"print-raw"}, &out, &errOut); err != nil || saw != io.Writer(&out) {
		t.Fatalf("%v, wrapped without secrets", err)
	}
}

func TestVerbExitCodeSurvivesRedaction(t *testing.T) {
	commands["print-fail"] = command{name: "print-fail", run: func(_ []string, _ io.Reader, stdout, _ io.Writer) int { fmt.Fprint(stdout, "ab"); return 3 }}
	t.Cleanup(func() { delete(commands, "print-fail") })
	withVerbKeychain(t, "T=s/a", mapKeychain{"s/a": "abcdefgh"})
	out, _, code := call(t, "print-fail")
	if code != 3 || out != "ab" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestDoctorProbesReadTheConfiguration(t *testing.T) {
	if configuredDoctorProbes().Profile == nil {
		t.Fatal("doctor has no way to read the profile")
	}
}

func TestVerbFiltersPaneReads(t *testing.T) {
	const value = "sk-pane-Secret-value"
	printer(t, "print-pane", "text")
	withVerbKeychain(t, "T=s/a", mapKeychain{"s/a": value})
	t.Cleanup(func() { herdr.SetPaneFilter(nil) })
	fake := herdrtest.Start(t)
	fake.Handle("pane.read", func(p map[string]any) (any, string) {
		return map[string]any{"read": map[string]any{"pane_id": p["pane_id"], "text": value}}, ""
	})
	call(t, "print-pane")
	got, err := herdr.New(fake.Socket).ReadText(context.Background(), "w1:p1", herdr.SourceVisible, 5)
	if err != nil || got != secrets.Marker {
		t.Fatalf("%q %v", got, err)
	}
}

func TestWebAgentRefusal(t *testing.T) {
	if got := webAgentRefusal("restricted"); !strings.Contains(got, "restricted") {
		t.Errorf("restricted: %q", got)
	}
	for _, p := range []string{"", "trusted-local"} {
		if got := webAgentRefusal(p); got != "" {
			t.Errorf("%q: %q", p, got)
		}
	}
}
