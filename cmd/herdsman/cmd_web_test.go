package main

import (
	"net/url"
	"path/filepath"
	"slices"
	"testing"
)

func TestGatewayOnlyOnLoopback(t *testing.T) {
	getenv := func(name string) string {
		if name == "HERDSMAN_PRIVATE_HOME" {
			return "/private-home"
		}
		return ""
	}
	want := filepath.Join("/private-home", "run", "daemon.sock")
	for _, host := range []string{"127.0.0.1", "::1", "localhost"} {
		got, err := gatewaySocket(host, getenv)
		if err != nil || got != want {
			t.Errorf("host %s: socket %q, error %v", host, got, err)
		}
	}
	for _, host := range []string{"192.168.1.20", "100.64.0.7", "box.tailnet.ts.net"} {
		got, err := gatewaySocket(host, getenv)
		if err != nil || got != "" {
			t.Errorf("host %s turned the gateway on: socket %q, error %v", host, got, err)
		}
	}
}

func TestGatewayOriginsNameOnlyTheHostedApp(t *testing.T) {
	if got := gatewayOrigins(nil); got != nil {
		t.Fatalf("no hosted app: %v", got)
	}
	hosted, err := url.Parse("https://app.example/some/path?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if got := gatewayOrigins(hosted); !slices.Equal(got, []string{"https://app.example"}) {
		t.Fatalf("origins %v", got)
	}
}
