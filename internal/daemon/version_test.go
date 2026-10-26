package daemon

import (
	"net/http"
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	good := []string{"1.0.0", "0.9.12", "1.2.3-rc.1", "10.20.30-beta-2", "devel+abcdef0", "devel+" + strings.Repeat("a", 40)}
	for _, v := range good {
		if _, err := ParseVersion(v); err != nil {
			t.Errorf("ParseVersion(%q) = %v, want ok", v, err)
		}
	}
	bad := []string{"", "1.0", "v1.0.0", "1.0.0+x", "devel+abc", "devel+ABCDEF0", "devel+" + strings.Repeat("a", 41), "1.0.0-", " 1.0.0", "1.0.0\n", "dev"}
	for _, v := range bad {
		if _, err := ParseVersion(v); err == nil {
			t.Errorf("ParseVersion(%q) passed, want an error", v)
		}
	}
}

func TestVersionOrder(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "10.0.0", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0-rc.a", "1.0.0-rc.1", 1},
		{"1.0.0-rc", "1.0.0-rc.1", -1},
		{"devel+abcdef0", "0.0.1", -1},
		{"0.0.1", "devel+abcdef0", 1},
		{"devel+abcdef0", "devel+abcdef0", 0},
		{"devel+abcdef0", "devel+abcdef1", -1},
		{"devel+abcdef1", "devel+abcdef0", 1},
	}
	for _, c := range cases {
		a, _ := ParseVersion(c.a)
		b, _ := ParseVersion(c.b)
		if got := compareVersions(a, b); got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func hdr(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Add(pairs[i], pairs[i+1])
	}
	return h
}

// TestCheckVersionRows covers each row of the 5.6 refusal table at the
// unit level; TestHelloAndVersionRefusal covers them over the socket.
func TestCheckVersionRows(t *testing.T) {
	const dv = "1.2.0"
	cases := []struct {
		name   string
		h      http.Header
		code   string
		update string
		text   string
		status int
	}{
		{"same", hdr(HeaderVersion, dv, HeaderProtocol, "1"), "", "", "", 0},
		{"missing", hdr(HeaderProtocol, "1"), "version_missing", "client", "This client sent no herdsman version; it is older than the daemon (1.2.0) or not herdsman. Update the client to herdsman 1.2.0.", 400},
		{"missing protocol", hdr(HeaderVersion, dv), "version_missing", "client", "This client sent no herdsman version", 400},
		{"malformed", hdr(HeaderVersion, "nope", HeaderProtocol, "1"), "version_malformed", "client", `This client sent an invalid herdsman version ("nope"). Update the client to herdsman 1.2.0.`, 400},
		{"malformed long", hdr(HeaderVersion, strings.Repeat("x", 80), HeaderProtocol, "1"), "version_malformed", "client", `("` + strings.Repeat("x", 40) + `")`, 400},
		{"repeated", hdr(HeaderVersion, dv, HeaderVersion, dv, HeaderProtocol, "1"), "version_malformed", "client", "invalid herdsman version", 400},
		{"bad protocol", hdr(HeaderVersion, dv, HeaderProtocol, "one"), "version_malformed", "client", "invalid herdsman version", 400},
		{"client older", hdr(HeaderVersion, "1.1.9", HeaderProtocol, "1"), "version_mismatch", "client", "herdsman 1.1.9 is older than the daemon (1.2.0). Update this client: install herdsman 1.2.0 on PATH, then run the command again.", 426},
		{"client older same protocol devel", hdr(HeaderVersion, "devel+abcdef0", HeaderProtocol, "1"), "version_mismatch", "client", "herdsman devel+abcdef0 is older than the daemon", 426},
		{"daemon older", hdr(HeaderVersion, "1.3.0", HeaderProtocol, "1"), "version_mismatch", "daemon", "The herdsman daemon (1.2.0) is older than this client (1.3.0). Restart it to load the installed binary: herdsman daemon restart.", 426},
		{"protocol differs", hdr(HeaderVersion, dv, HeaderProtocol, "7"), "version_mismatch", "daemon", "herdsman 1.2.0 reports protocol 7 but the daemon reports 1; reinstall herdsman and run herdsman daemon restart.", 426},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckVersion(c.h, dv, 1)
			if c.code == "" {
				if err != nil {
					t.Fatalf("got %v, want none", err)
				}
				return
			}
			if err == nil {
				t.Fatal("no refusal")
			}
			if err.Code != c.code || err.Update != c.update || err.Status != c.status || !strings.Contains(err.Message, c.text) {
				t.Fatalf("got %+v, want code %s update %s status %d text %q", err, c.code, c.update, c.status, c.text)
			}
		})
	}
	// Two development builds of different commits differ, in both directions.
	if e := CheckVersion(hdr(HeaderVersion, "devel+abcdef1", HeaderProtocol, "1"), "devel+abcdef0", 1); e == nil || e.Code != "version_mismatch" {
		t.Fatalf("devel builds: %v", e)
	}
}

// TestClientVersionCheck is the client's side of 5.6: the same table,
// read from the daemon's hello.
func TestClientVersionCheck(t *testing.T) {
	if err := ClientCheck("1.0.0", 1, Hello{DaemonVersion: "1.0.0", Protocol: 1}); err != nil {
		t.Fatal(err)
	}
	e := ClientCheck("1.0.0", 1, Hello{DaemonVersion: "0.9.0", Protocol: 1})
	if e == nil || e.Update != "daemon" || !strings.Contains(e.Message, "The herdsman daemon (0.9.0) is older than this client (1.0.0)") {
		t.Fatalf("older daemon: %+v", e)
	}
	e = ClientCheck("1.0.0", 1, Hello{DaemonVersion: "1.1.0", Protocol: 1})
	if e == nil || e.Update != "client" || !strings.Contains(e.Message, "herdsman 1.0.0 is older than the daemon (1.1.0)") {
		t.Fatalf("newer daemon: %+v", e)
	}
	e = ClientCheck("1.0.0", 1, Hello{DaemonVersion: "1.0.0", Protocol: 2})
	if e == nil || e.Update != "daemon" {
		t.Fatalf("protocol: %+v", e)
	}
	e = ClientCheck("1.0.0", 1, Hello{DaemonVersion: "garbage", Protocol: 1})
	if e == nil || e.Code != "version_malformed" {
		t.Fatalf("garbage: %+v", e)
	}
}

func TestBuildVersion(t *testing.T) {
	if got := BuildVersion("v1.2.3", "none"); got != "1.2.3" {
		t.Fatalf("release: %s", got)
	}
	if got := BuildVersion("dev", "0123456789abcdef"); got != "devel+0123456789abcdef" {
		t.Fatalf("dev: %s", got)
	}
	if got := BuildVersion("dev", "none"); !strings.HasPrefix(got, "devel+") {
		t.Fatalf("dev without commit: %s", got)
	} else if _, err := ParseVersion(got); err != nil {
		t.Fatalf("dev without commit is not a valid version: %s", got)
	}
}

func FuzzVersionHeader(f *testing.F) {
	for _, s := range []string{"1.0.0", "devel+abcdef0", "", "1.0.0-rc.1", "x", "1.0.0-", "devel+", "99999999999999999999.0.0"} {
		f.Add(s, "1")
	}
	f.Fuzz(func(t *testing.T, v, p string) {
		e := CheckVersion(hdr(HeaderVersion, v, HeaderProtocol, p), "1.0.0", 1)
		if e != nil && (e.Code == "" || e.Status == 0 || e.Message == "" || strings.ContainsAny(e.Message, "\n\r")) {
			t.Fatalf("bad refusal %+v", e)
		}
		if pv, err := ParseVersion(v); err == nil {
			if compareVersions(pv, pv) != 0 {
				t.Fatalf("not reflexive: %q", v)
			}
		}
	})
}

// TestExactVersionSpellings: versions are compared as exact strings; a
// spelling that is semantically equal ("01.2.0", "1.2.0+x") is still a
// mismatch on both sides (5.6).
func TestExactVersionSpellings(t *testing.T) {
	for _, v := range []string{"01.2.0", "1.02.0", "1.2.00"} {
		if _, err := ParseVersion(v); err != nil {
			continue // refused as malformed: also not accepted
		}
		if e := CheckVersion(hdr(HeaderVersion, v, HeaderProtocol, "1"), "1.2.0", 1); e == nil || e.Code != "version_mismatch" {
			t.Fatalf("daemon accepted %q for 1.2.0: %v", v, e)
		}
		if e := ClientCheck(v, 1, Hello{DaemonVersion: "1.2.0", Protocol: 1, MinProtocol: 1}); e == nil || e.Code != "version_mismatch" {
			t.Fatalf("client accepted daemon 1.2.0 for %q: %v", v, e)
		}
	}
	if _, err := ParseVersion("01.2.0"); err != nil {
		t.Fatal("the case must reach the comparison")
	}
}
