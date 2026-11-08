package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"

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
