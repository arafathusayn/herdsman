// Package daemon is `herdsman daemon`: one long-running process per user
// account that owns every route's private inbox, writes its files through
// one route actor per route, commits every operation in a per-route journal
// before its files change, and serves the adjutant–orchestrator channel on
// a Unix socket (HTTP requests and Server-Sent Events). It also holds the
// clients of that socket: the daemon path of `herdsman message ...` and
// the `herdsman daemon ...` admin commands.
//
// It names no model and no effort level anywhere (A28).
package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

// Protocol is the wire format's number; it only describes the format in
// diagnostics. Compatibility is version equality (5.6).
const Protocol = 1

// The version headers every request carries (5.1, 5.6).
const (
	HeaderVersion  = "Herdsman-Version"
	HeaderProtocol = "Herdsman-Protocol"
	HeaderPane     = "Herdsman-Pane"
	HeaderName     = "Herdsman-Name"
)

var versionRe = regexp.MustCompile(`^(\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?|devel\+[0-9a-f]{7,40})$`)

// Version is a parsed herdsman version: a release (major.minor.patch with
// an optional pre-release) or a development build (devel+<commit>).
type Version struct {
	Raw   string
	Devel bool
	Nums  [3]uint64
	Pre   []string
}

// ParseVersion is the strict parser of 5.6.
func ParseVersion(s string) (Version, error) {
	if !versionRe.MatchString(s) || strings.HasSuffix(s, "-") {
		return Version{}, errors.New("invalid version")
	}
	v := Version{Raw: s}
	if strings.HasPrefix(s, "devel+") {
		v.Devel = true
		return v, nil
	}
	core, pre, hasPre := strings.Cut(s, "-")
	for i, p := range strings.Split(core, ".") {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Version{}, errors.New("invalid version")
		}
		v.Nums[i] = n
	}
	if hasPre {
		v.Pre = strings.Split(pre, ".")
		for _, p := range v.Pre {
			if p == "" {
				return Version{}, errors.New("invalid version")
			}
		}
	}
	return v, nil
}

// compareVersions orders two versions: semantic version order for
// releases; a development build is older than any release, and two
// development builds order by their commit text (they only ever need to
// be equal or not).
func compareVersions(a, b Version) int {
	switch {
	case a.Devel && b.Devel:
		return strings.Compare(a.Raw, b.Raw)
	case a.Devel:
		return -1
	case b.Devel:
		return 1
	}
	for i := range a.Nums {
		if a.Nums[i] != b.Nums[i] {
			if a.Nums[i] < b.Nums[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := comparePre(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

func comparePre(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		if an < bn {
			return -1
		} else if an > bn {
			return 1
		}
		return 0
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func versionErr(code, update, msg string) *APIError {
	e := Errf(code, "%s", msg)
	e.Update = update
	return e
}

// CheckVersion checks a request's version headers against the daemon's
// version (5.6). nil means the request may go on.
func CheckVersion(h http.Header, daemonVersion string, protocol int) *APIError {
	vs, ps := h.Values(HeaderVersion), h.Values(HeaderProtocol)
	if len(vs) == 0 || len(ps) == 0 {
		return versionErr("version_missing", "client", fmt.Sprintf("This client sent no herdsman version; it is older than the daemon (%s) or not herdsman. Update the client to herdsman %s.", daemonVersion, daemonVersion))
	}
	malformed := func(v string) *APIError {
		v = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return '?'
			}
			return r
		}, clip(v, 40))
		return versionErr("version_malformed", "client", fmt.Sprintf("This client sent an invalid herdsman version (%q). Update the client to herdsman %s.", v, daemonVersion))
	}
	if len(vs) > 1 || len(ps) > 1 {
		return malformed(strings.Join(vs, ","))
	}
	cv, err := ParseVersion(vs[0])
	if err != nil {
		return malformed(vs[0])
	}
	cp, err := strconv.Atoi(ps[0])
	if err != nil || cp < 0 || strconv.Itoa(cp) != ps[0] {
		return malformed(vs[0])
	}
	dv, _ := ParseVersion(daemonVersion)
	return compare(cv, cp, dv, protocol)
}

// compare is the version table of 5.6 for a client version cv with
// protocol cp against a daemon dv with protocol dp.
func compare(cv Version, cp int, dv Version, dp int) *APIError {
	if cv.Raw == dv.Raw {
		if cp != dp {
			return versionErr("version_mismatch", "daemon", fmt.Sprintf("herdsman %s reports protocol %d but the daemon reports %d; reinstall herdsman and run herdsman daemon restart.", cv.Raw, cp, dp))
		}
		return nil
	}
	if compareVersions(cv, dv) < 0 {
		return versionErr("version_mismatch", "client", fmt.Sprintf("herdsman %s is older than the daemon (%s). Update this client: install herdsman %s on PATH, then run the command again.", cv.Raw, dv.Raw, dv.Raw))
	}
	return versionErr("version_mismatch", "daemon", fmt.Sprintf("The herdsman daemon (%s) is older than this client (%s). Restart it to load the installed binary: herdsman daemon restart.", dv.Raw, cv.Raw))
}

// Hello is the answer of GET /v1/hello.
type Hello struct {
	// DaemonVersion is the daemon's build version (5.6).
	DaemonVersion string `json:"daemon_version"`
	// Protocol is the wire format's number; MinProtocol the oldest it reads.
	Protocol    int `json:"protocol"`
	MinProtocol int `json:"min_protocol"`
	// Features lists optional abilities, such as "keychain".
	Features []string `json:"features"`
	// CredentialOptions are the per-party credential options this daemon
	// offers; DefaultCredential is the one a pair gets unless the human
	// picks another (the user's decision: file).
	CredentialOptions []string `json:"credential_options"`
	DefaultCredential string   `json:"default_credential"`
}

// ClientCheck is the client's check of a daemon's hello (5.6): the same
// table as the daemon's, seen from the client.
func ClientCheck(clientVersion string, protocol int, h Hello) *APIError {
	dv, err := ParseVersion(h.DaemonVersion)
	if err != nil {
		return versionErr("version_malformed", "daemon", fmt.Sprintf("The herdsman daemon sent an invalid version (%q). Restart it: herdsman daemon restart.", clip(h.DaemonVersion, 40)))
	}
	cv, err := ParseVersion(clientVersion)
	if err != nil {
		return versionErr("version_malformed", "client", fmt.Sprintf("This client has an invalid herdsman version (%q); reinstall herdsman.", clip(clientVersion, 40)))
	}
	return compare(cv, protocol, dv, h.Protocol)
}

var commitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// BuildVersion turns the release build's version and commit into the
// version the handshake compares: the release without a leading v, or
// devel+<commit> for a development build (from the build's VCS stamp when
// the commit is unknown).
func BuildVersion(version, commit string) string {
	v := strings.TrimPrefix(version, "v")
	if _, err := ParseVersion(v); err == nil && !strings.HasPrefix(v, "devel+") {
		return v
	}
	if commitRe.MatchString(commit) {
		return "devel+" + commit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && commitRe.MatchString(s.Value) {
				return "devel+" + s.Value
			}
		}
	}
	return "devel+0000000"
}
