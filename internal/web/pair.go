package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"regexp"
	"sync"
	"time"
)

// Pairing: the token in the URL only lets a browser ask to pair. The server
// prints a one-time code in the terminal that runs `herdsman web`, and the
// browser must send that code back. So a leaked URL is not enough: the person
// must also see the terminal. A paired browser gets a device secret, which
// it sends as a bearer token; it is good until the server stops.

const (
	pairTTL      = 2 * time.Minute
	pairAttempts = 5
	maxPending   = 8
	// Guessing is bounded across requests too: after maxFailures wrong codes
	// the link's token is replaced, so each leaked link allows that many
	// guesses in all, and the terminal prints the new link.
	maxFailures = 20
)

// PairNotice is what the terminal shows for one pairing request.
type PairNotice struct {
	Code    string
	Device  string
	Remote  string
	Expires time.Time
}

type pairing struct {
	code     string
	device   string
	expires  time.Time
	attempts int
}

type devices struct {
	mu       sync.Mutex
	now      func() time.Time
	pending  map[string]*pairing // by sha256 of the pairing secret
	paired   map[string]string   // sha256 of the device secret -> device name
	failures int                 // wrong codes since the token was last replaced
}

func newDevices() *devices {
	return &devices{now: time.Now, pending: map[string]*pairing{}, paired: map[string]string{}}
}

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// begin opens a pairing request and returns its secret and code.
func (d *devices) begin(device string) (sec, code string, expires time.Time, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	for k, p := range d.pending {
		if now.After(p.expires) {
			delete(d.pending, k)
		}
	}
	if len(d.pending) >= maxPending {
		return "", "", time.Time{}, fmt.Errorf("too many open pairing requests; wait %s", pairTTL)
	}
	sec, code, expires = secret(), newCode(), now.Add(pairTTL)
	d.pending[digest(sec)] = &pairing{code: code, device: device, expires: expires}
	return sec, code, expires, nil
}

// pending reports whether a pairing secret names a live request.
func (d *devices) isPending(sec string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.pending[digest(sec)]
	return ok && !d.now().After(p.expires)
}

// errPair explains a failed code check to the browser.
type errPair struct {
	msg  string
	left int
	gone bool
	// rotate asks the server to replace the link's token: the wrong codes
	// reached maxFailures, and every open request has ended.
	rotate bool
}

func (e *errPair) Error() string { return e.msg }

// confirm checks a code. On success it ends the request and returns a new
// device secret; a request dies after too many wrong codes or when it expires.
func (d *devices) confirm(sec, code string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := digest(sec)
	p, ok := d.pending[key]
	if !ok || d.now().After(p.expires) {
		delete(d.pending, key)
		return "", &errPair{msg: "This pairing request expired. Open the URL again.", gone: true}
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(p.code)) != 1 {
		p.attempts++
		d.failures++
		if d.failures >= maxFailures {
			d.failures = 0
			clear(d.pending)
			return "", &errPair{msg: "Too many wrong codes for this link. Open the new link that the terminal printed.", gone: true, rotate: true}
		}
		left := pairAttempts - p.attempts
		if left <= 0 {
			delete(d.pending, key)
			return "", &errPair{msg: "Too many wrong codes. Open the URL again for a new code.", gone: true}
		}
		return "", &errPair{msg: "That code is wrong.", left: left}
	}
	delete(d.pending, key)
	dev := secret()
	d.paired[digest(dev)] = p.device
	return dev, nil
}

func (d *devices) isPaired(dev string) bool {
	if dev == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.paired[digest(dev)]
	return ok
}

var (
	uaOS = []struct {
		re   *regexp.Regexp
		name string
	}{
		{regexp.MustCompile(`iPhone`), "iPhone"},
		{regexp.MustCompile(`iPad`), "iPad"},
		{regexp.MustCompile(`Android`), "Android"},
		{regexp.MustCompile(`Macintosh|Mac OS X`), "macOS"},
		{regexp.MustCompile(`Windows`), "Windows"},
		{regexp.MustCompile(`CrOS`), "ChromeOS"},
		{regexp.MustCompile(`Linux`), "Linux"},
	}
	uaBrowser = []struct {
		re   *regexp.Regexp
		name string
	}{
		{regexp.MustCompile(`Edg/`), "Edge"},
		{regexp.MustCompile(`Firefox/|FxiOS/`), "Firefox"},
		{regexp.MustCompile(`Chrome/|CriOS/`), "Chrome"},
		{regexp.MustCompile(`Safari/`), "Safari"},
	}
)

// describeDevice names a browser for the terminal, e.g. "Safari on iPhone".
func describeDevice(ua string) string {
	browser, os := "A browser", "an unknown system"
	for _, b := range uaBrowser {
		if b.re.MatchString(ua) {
			browser = b.name
			break
		}
	}
	for _, o := range uaOS {
		if o.re.MatchString(ua) {
			os = o.name
			break
		}
	}
	return browser + " on " + os
}

func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
