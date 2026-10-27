package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	cases := []struct {
		args    []string
		want    string
		wantErr string
	}{
		{args: nil, want: "Usage:"},
		{args: []string{"help"}, want: "herdsman web"},
		{args: []string{"version"}, want: "herdsman dev"},
		{args: []string{"fly"}, wantErr: `unknown command "fly"`},
		// Only a build with the cgoprobe tag knows the probe.
		{args: []string{"--cgo-probe"}, wantErr: `unknown command "--cgo-probe"`},
		{args: []string{"web", "--nope"}, wantErr: "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := run(tc.args, &out, &errOut)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("out = %q, err = %v; want %q", out.String(), err, tc.want)
			}
		})
	}
}

func TestWebRefusesEveryInterface(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::"} {
		var out, errOut bytes.Buffer
		err := run([]string{"web", "--host", host}, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), "every interface") {
			t.Fatalf("--host %s: err = %v", host, err)
		}
	}
}

func TestExtraHosts(t *testing.T) {
	for host, want := range map[string]int{"127.0.0.1": 0, "::1": 0, "localhost": 0, "192.168.1.20": 1, "100.64.0.7": 1, "box.tailnet.ts.net": 1} {
		if got := len(extraHosts(host)); got != want {
			t.Errorf("extraHosts(%q) has %d names, want %d", host, got, want)
		}
	}
}

// Plain HTTP is warned about only where it crosses a network unencrypted:
// not on loopback, and not on a Tailscale address.
func TestPlainHTTPWarning(t *testing.T) {
	for host, warn := range map[string]bool{
		"127.0.0.1": false, "::1": false, "localhost": false,
		"100.64.0.7": false, "100.127.255.1": false, "fd7a:115c:a1e0::1": false,
		"192.168.1.20": true, "10.0.0.5": true, "100.128.0.1": true, "box.local": true,
	} {
		got := plainHTTPWarning(host)
		if (got != "") != warn {
			t.Errorf("plainHTTPWarning(%q) = %q, want a warning: %v", host, got, warn)
		}
		if warn && !strings.Contains(got, "--public-url") {
			t.Errorf("plainHTTPWarning(%q) does not say what to do: %q", host, got)
		}
	}
}

// Stopping the server ends open event streams, so it stops at once and
// without an error.
func TestServeEndsOpenStreams(t *testing.T) {
	entered := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln) }()
	go func() {
		res, err := http.Get("http://" + ln.Addr().String())
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}
	}()
	<-entered
	start := time.Now()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("stopping took %v", d)
	}
}

// web refuses to start when no herdr server answers.
func TestWebNeedsHerdr(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", t.TempDir()+"/missing.sock")
	var out, errOut bytes.Buffer
	err := run([]string{"web", "--port", "0"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientLinkCarriesHostAndTokenInTheFragment(t *testing.T) {
	u, _ := neturl.Parse("https://herdsman.example.com/app/")
	got := clientLink(u, "http://127.0.0.1:7878", "abc123")
	if got != "https://herdsman.example.com/app/#host=http%3A%2F%2F127.0.0.1%3A7878&t=abc123" {
		t.Fatalf("link = %s", got)
	}
}

// The QR code opens the hosted app only when a phone can reach this server
// over HTTPS; otherwise it opens the server's own app.
func TestQRCodeOpensTheHostedAppOnlyOverHTTPS(t *testing.T) {
	client, _ := neturl.Parse(defaultClient)
	public, _ := neturl.Parse("https://box.tailnet.ts.net")
	cases := []struct {
		name           string
		public, client *neturl.URL
		qr, hosted     string
	}{
		{"default", nil, client, "http://192.168.1.5:7878/#t=abc123",
			"https://herdsman.arafat.ai/#host=http%3A%2F%2F192.168.1.5%3A7878&t=abc123"},
		{"public", public, client, "https://herdsman.arafat.ai/#host=https%3A%2F%2Fbox.tailnet.ts.net&t=abc123",
			"https://herdsman.arafat.ai/#host=https%3A%2F%2Fbox.tailnet.ts.net&t=abc123"},
		{"public, no client", public, nil, "https://box.tailnet.ts.net/#t=abc123", ""},
		{"no client", nil, nil, "http://192.168.1.5:7878/#t=abc123", ""},
	}
	for _, c := range cases {
		l := makeLinks("192.168.1.5:7878", "abc123", c.public, c.client)
		if l.qr != c.qr || l.hosted != c.hosted {
			t.Errorf("%s: qr = %s, hosted = %s", c.name, l.qr, l.hosted)
		}
		if l.direct != "http://192.168.1.5:7878/#t=abc123" || l.qrNote == "" {
			t.Errorf("%s: direct = %s, note = %q", c.name, l.direct, l.qrNote)
		}
	}
}

func TestWebChecksNewFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--inbox", "relative/inbox"}, {"--client", "ftp://x"}, {"--client", "herdsman.example.com"},
		{"--public-url", "http://box.tailnet.ts.net"}, {"--public-url", "https://box.tailnet.ts.net/app"},
		{"--public-url", "box.tailnet.ts.net"}, {"--public-url", "https://u:p@box.tailnet.ts.net"},
	} {
		var out, errb bytes.Buffer
		if err := run(append([]string{"web"}, args...), &out, &errb); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}
