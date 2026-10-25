// Package doctor checks that a machine can run a herdsman route: the herdr
// socket, the gh login, the installed binary, the message inbox and its
// setup proof, what a sandbox denies the gates, and the machine load.
package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/machine"
	"github.com/arafathusayn/herdsman/internal/review"
)

// The levels of a check.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
)

// Check is one line of the report, `<level> <check> <detail>`.
//
// JSON fields: level ("ok", "warn" or "fail"), check (herdr, gh, binary,
// inbox, sandbox or load) and detail (free text).
type Check struct {
	Level  string `json:"level"`
	Name   string `json:"check"`
	Detail string `json:"detail"`
}

// Failed says whether any check failed.
func Failed(cs []Check) bool {
	for _, c := range cs {
		if c.Level == Fail {
			return true
		}
	}
	return false
}

// Probes are what the checks read; tests replace each one.
type Probes struct {
	Getenv     func(string) string
	Herdr      *herdr.Client // nil when no socket path resolves
	LookPath   func(string) (string, error)
	GhAuth     func(ctx context.Context, gh string) error
	Executable func() (string, error)
	// GoCache is the default Go build cache folder.
	GoCache func() (string, error)
	// WriteDir writes and removes one temporary file in a folder.
	WriteDir    func(dir string) error
	ReadMemory  func() error
	Renice      func() error
	ReadLoad    func() (machine.Load, error)
	FreePercent func() (int, error)
	CPUs        func() int
	// TempDir is the root of the caches a sandboxed harness can use.
	TempDir string
}

// Default returns the real probes.
func Default() Probes {
	var client *herdr.Client
	if sock, err := herdr.SocketPath(""); err == nil {
		client = herdr.New(sock)
	}
	return Probes{
		Getenv: os.Getenv, Herdr: client, LookPath: exec.LookPath, GhAuth: ghAuth,
		Executable: os.Executable, GoCache: goCache, WriteDir: writeDir,
		ReadMemory: readMemory, Renice: renice,
		ReadLoad: machine.ReadLoad, FreePercent: machine.FreePercent, CPUs: machine.CPUs,
		TempDir: review.TempRoot(),
	}
}

// SettingError is a setting the strict parser refused.
type SettingError struct{ Name, Reason, Value string }

func (e *SettingError) Error() string {
	return fmt.Sprintf("%s: %s (got %q)", e.Name, e.Reason, e.Value)
}

// wholeNumber reads an integer as test(1) does (blanks around it, a plus
// sign, leading zeros read as decimal) within [lo, hi].
func wholeNumber(name, v string, lo, hi int) (int, error) {
	s := strings.Trim(v, " \t\n")
	s = strings.TrimPrefix(s, "+")
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, &SettingError{name, "not a whole number", v}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < int64(lo) || n > int64(hi) {
		return 0, &SettingError{name, fmt.Sprintf("out of range %d to %d", lo, hi), v}
	}
	return int(n), nil
}

// positiveNumber reads a load limit as awk compares it: a finite number
// over zero.
func positiveNumber(name, v string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f <= 0 {
		return 0, &SettingError{name, "not a positive number", v}
	}
	return f, nil
}

type limits struct {
	load      float64
	loadLabel string
	minFree   int
}

// readLimits parses HERDSMAN_LOAD_LIMIT (default the core count) and
// HERDSMAN_MIN_FREE (default 10), as run-check reads them.
func readLimits(p Probes) (limits, error) {
	l := limits{load: float64(p.CPUs()), loadLabel: strconv.Itoa(p.CPUs()), minFree: 10}
	if v := p.Getenv("HERDSMAN_LOAD_LIMIT"); v != "" {
		f, err := positiveNumber("HERDSMAN_LOAD_LIMIT", v)
		if err != nil {
			return l, err
		}
		l.load, l.loadLabel = f, strings.TrimSpace(v)
	}
	if v := p.Getenv("HERDSMAN_MIN_FREE"); v != "" {
		n, err := wholeNumber("HERDSMAN_MIN_FREE", v, 0, 100)
		if err != nil {
			return l, err
		}
		l.minFree = n
	}
	return l, nil
}

// Run parses the settings, then runs every check. A *SettingError comes
// back before any check runs.
func Run(ctx context.Context, p Probes) ([]Check, error) {
	lim, err := readLimits(p)
	if err != nil {
		return nil, err
	}
	var cs []Check
	add := func(level, name, detail string) { cs = append(cs, Check{level, name, detail}) }
	checkHerdr(ctx, p, add)
	checkGh(ctx, p, add)
	checkBinary(p, add)
	if dir := p.Getenv("HERDSMAN_INBOX"); dir != "" {
		checkInbox(p, dir, add)
	}
	checkSandbox(p, add)
	checkLoad(p, lim, add)
	return cs, nil
}

type adder func(level, name, detail string)

func checkHerdr(ctx context.Context, p Probes, add adder) {
	if p.Herdr == nil {
		add(Fail, "herdr", "no herdr socket: HOME and XDG_CONFIG_HOME are unset")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	pong, err := p.Herdr.Check(cctx)
	if err != nil {
		add(Fail, "herdr", err.Error())
		return
	}
	detail := fmt.Sprintf("herdr %s protocol %d at %s", pong.Version, pong.Protocol, p.Herdr.Socket)
	if p.Getenv("HERDR_ENV") != "1" {
		add(Warn, "herdr", detail+"; HERDR_ENV is not 1, so this shell is not inside herdr")
		return
	}
	add(OK, "herdr", detail)
}

func checkGh(ctx context.Context, p Probes, add adder) {
	gh, err := p.LookPath("gh")
	if err != nil {
		add(Warn, "gh", "gh not found on PATH")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := p.GhAuth(cctx, gh); err != nil {
		add(Fail, "gh", "gh auth status: "+err.Error())
		return
	}
	add(OK, "gh", "gh auth status: exit 0 ("+gh+")")
}

// ghAuth runs `gh auth status`; only its exit code counts.
func ghAuth(ctx context.Context, gh string) error {
	cmd := exec.CommandContext(ctx, gh, "auth", "status")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

func checkBinary(p Probes, add adder) {
	exe, err := p.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		add(Fail, "binary", err.Error())
		return
	}
	sum, err := fileSHA256(exe)
	if err != nil {
		add(Fail, "binary", err.Error())
		return
	}
	detail := exe + " sha256=" + sum
	onPath, err := p.LookPath("herdsman")
	if err != nil {
		add(Warn, "binary", detail+"; no herdsman on PATH")
		return
	}
	if real, err := filepath.EvalSymlinks(onPath); err == nil {
		onPath = real
	}
	if onPath != exe {
		add(Warn, "binary", detail+"; not the herdsman first on PATH ("+onPath+")")
		return
	}
	add(OK, "binary", detail)
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkInbox opens the inbox read-only, so the doctor creates nothing in
// it, and fails on a stale setup proof as a writer would.
func checkInbox(p Probes, dir string, add adder) {
	r, err := inbox.Open(inbox.Options{
		Dir: dir, Preflight: p.Getenv("HERDSMAN_PREFLIGHT"), Installed: p.Getenv("HERDSMAN_MESSAGES_DIR"),
		HealthAfter: p.Getenv("HEALTH_AFTER"), LockWait: p.Getenv("LOCK_WAIT"), ReadOnly: true,
	})
	switch {
	case err != nil:
		add(Fail, "inbox", err.Error())
	case r.ProofStale:
		add(Fail, "inbox", "sandbox policy or protected installation proof stale; repeat human setup")
	case r.TimingStale:
		add(Fail, "inbox", "invalid or conflicting shared timing configuration")
	default:
		add(OK, "inbox", dir+": setup proof valid")
	}
}

func checkSandbox(p Probes, add adder) {
	denied := false
	cache, err := p.GoCache()
	if err == nil {
		err = p.WriteDir(cache)
	}
	if err != nil {
		denied = true
		add(Warn, "sandbox", "go build cache denied: "+err.Error())
	}
	if err := p.ReadMemory(); err != nil {
		denied = true
		add(Warn, "sandbox", "memory reading denied: "+err.Error())
	}
	if err := p.Renice(); err != nil {
		denied = true
		add(Warn, "sandbox", "setpriority denied: "+err.Error())
	}
	if !denied {
		add(OK, "sandbox", "go build cache, memory reading and setpriority allowed")
		return
	}
	add(Warn, "sandbox", fmt.Sprintf("use GOCACHE=%s STATICCHECK_CACHE=%s",
		filepath.Join(p.TempDir, "herdsman-go-build"), filepath.Join(p.TempDir, "herdsman-staticcheck")))
}

func checkLoad(p Probes, lim limits, add adder) {
	l, err := p.ReadLoad()
	var free int
	if err == nil {
		free, err = p.FreePercent()
	}
	if err != nil {
		add(Warn, "load", "cannot read the machine: "+err.Error())
		return
	}
	detail := fmt.Sprintf("load %s (limit %s), free %d%% (min %d)", machine.FormatLoad(l[0]), lim.loadLabel, free, lim.minFree)
	// As run-check's gate: the load under the limit and free memory at least the minimum.
	if l[0] >= lim.load || free < lim.minFree {
		add(Warn, "load", detail)
		return
	}
	add(OK, "load", detail)
}

// goCache is the default Go build cache: GOCACHE, else go-build in the
// user cache folder, as `go env GOCACHE` reports it.
func goCache() (string, error) {
	if v := os.Getenv("GOCACHE"); v != "" {
		return v, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "go-build"), nil
}

// writeDir creates one temporary file in dir and removes it.
func writeDir(dir string) error {
	f, err := os.CreateTemp(dir, ".herdsman-doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	return errors.Join(f.Close(), os.Remove(name))
}

func readMemory() error {
	_, err := machine.FreePercent()
	return err
}

// renice sets this process's priority to the value it has.
func renice() error {
	prio, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	if err != nil {
		return err
	}
	return syscall.Setpriority(syscall.PRIO_PROCESS, 0, niceOf(prio))
}
