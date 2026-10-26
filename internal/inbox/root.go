package inbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Options are the settings of the scripts' root_init, from the environment
// the commands read (HERDSMAN_INBOX, HERDSMAN_PREFLIGHT, HEALTH_AFTER,
// LOCK_WAIT) and the caller.
type Options struct {
	Dir         string // HERDSMAN_INBOX
	Preflight   string // HERDSMAN_PREFLIGHT, the setup proof
	HealthAfter string // HEALTH_AFTER; "" takes 180 or the stored value
	LockWait    string // LOCK_WAIT; "" takes 6
	// ReadOnly opens the inbox as a reader: a stale proof or timing file is
	// recorded, not fatal, and no state folder is created.
	ReadOnly bool
	// Installed is the folder of the installed channel scripts whose files
	// the setup proof must list (see PreflightValid): the skill's installed
	// scripts/messages folder, absolute, from HERDSMAN_MESSAGES_DIR in the
	// commands. "" (or a folder without message-lib.sh) makes every proof stale.
	Installed string

	Now   func() time.Time    // nil: time.Now
	Sleep func(time.Duration) // nil: time.Sleep
}

// Root is an opened inbox.
type Root struct {
	Dir         string
	Preflight   string
	Installed   string
	HealthAfter int
	LockWait    int
	ReadOnly    bool
	// ProofStale is set when the setup proof does not verify.
	ProofStale bool
	// TimingStale is set when state/timing.json is invalid or conflicts.
	TimingStale bool

	now   func() time.Time
	sleep func(time.Duration)
}

// Now returns the root's clock.
func (r *Root) Now() time.Time { return r.now() }

// Path joins names under the inbox.
func (r *Root) Path(names ...string) string {
	return filepath.Join(append([]string{r.Dir}, names...)...)
}

// Fatal is an error the scripts report with `die`: the message goes to
// stderr and the command exits 2.
type Fatal struct{ Msg string }

func (e *Fatal) Error() string { return e.Msg }

func die(format string, a ...any) error { return &Fatal{Msg: fmt.Sprintf(format, a...)} }

// adminDirs are the inbox's administrative folders: when present, each
// must be a real folder.
var adminDirs = []string{"state", "alerts", "ids", "enrolled", "card-events", "courier.lock", "watch.lock", "channel.lock", "herdr-mutation.lock"}

// Open checks and opens an inbox as the scripts' root_init does: the
// folder absolute, canonical, a real folder owned and writable by this
// user with mode 700; the administrative state real folders; the timing
// file and the setup proof valid (or, read-only, flagged). It needs no
// program: the scripts' check for herdr, jq and nc has no counterpart.
// Every error is a *Fatal with the scripts' message.
func Open(o Options) (*Root, error) {
	r := &Root{Dir: o.Dir, Preflight: o.Preflight, Installed: o.Installed, ReadOnly: o.ReadOnly, now: o.Now, sleep: o.Sleep}
	if r.now == nil {
		r.now = time.Now
	}
	if r.sleep == nil {
		r.sleep = time.Sleep
	}
	health := o.HealthAfter
	if health == "" {
		health = "180"
	}
	var err error
	if r.HealthAfter, err = Uint("HEALTH_AFTER", health, 1, 86400); err != nil {
		return nil, &Fatal{Msg: err.Error()}
	}
	wait := o.LockWait
	if wait == "" {
		wait = "6"
	}
	if r.LockWait, err = Uint("LOCK_WAIT", wait, 1, 60); err != nil {
		return nil, &Fatal{Msg: err.Error()}
	}
	if r.Dir == "" {
		return nil, die("HERDSMAN_INBOX: set HERDSMAN_INBOX")
	}
	if !filepath.IsAbs(r.Dir) {
		return nil, die("inbox must be absolute")
	}
	st, err := os.Lstat(r.Dir)
	if err != nil || !st.IsDir() {
		return nil, die("pre-create a private inbox directory")
	}
	if real, err := filepath.EvalSymlinks(r.Dir); err != nil || real != r.Dir {
		return nil, die("inbox must be canonical")
	}
	if !ownedWritable(r.Dir, st) {
		return nil, die("inbox must be owned and writable")
	}
	if st.Mode().Perm() != 0o700 {
		return nil, die("inbox mode must be 700")
	}
	for _, d := range adminDirs {
		p := r.Path(d)
		if Exists(p) {
			if lst, err := os.Lstat(p); err != nil || !lst.IsDir() {
				return nil, die("symlink or invalid administrative state")
			}
		}
	}
	for _, leaf := range []string{"peers", "peers.approved", "courier.log", "courier.log.previous"} {
		if err := SafeLeaf(r.Dir, r.Path(leaf)); err != nil {
			return nil, &Fatal{Msg: err.Error()}
		}
	}
	if timing := r.Path("state", "timing.json"); Exists(timing) {
		stored, ok := storedHealth(r.Dir, timing)
		if ok && (o.HealthAfter == "" || o.HealthAfter == fmt.Sprint(stored)) {
			r.HealthAfter = stored
		} else {
			r.TimingStale = true
			if !r.ReadOnly {
				return nil, die("invalid or conflicting shared timing configuration")
			}
		}
	}
	// A daemon-owned inbox (contract 2) takes only the version 2 proof.
	valid := ProofV2Valid
	if !DaemonOwned(r.Dir) {
		valid = func(string) bool { return r.PreflightValid() }
	}
	if !valid(r.Dir) {
		r.ProofStale = true
		if !r.ReadOnly {
			return nil, die("sandbox policy or protected installation proof stale; repeat human setup")
		}
	}
	// A linked or non-folder entry of these was refused with adminDirs above.
	for _, d := range []string{"state", "ids", "enrolled", "alerts"} {
		if !r.ReadOnly {
			if err := os.MkdirAll(r.Path(d), 0o700); err != nil {
				return nil, die("cannot create %s: %v", d, err)
			}
		}
	}
	return r, nil
}

func ownedWritable(dir string, st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Geteuid() {
		return false
	}
	return syscall.Access(dir, 2) == nil // W_OK
}

// storedHealth reads the health interval of state/timing.json.
func storedHealth(root, p string) (int, bool) {
	b, ok := ReadStrict(root, p)
	if !ok {
		return 0, false
	}
	v, _ := Decode(b) // ReadStrict checked it
	n, ok := Int(Object(v), "health")
	if !ok || n < 1 || n > 86400 {
		return 0, false
	}
	return int(n), true
}

// IsFatal returns the message of a *Fatal error.
func IsFatal(err error) (string, bool) {
	if f, ok := errors.AsType[*Fatal](err); ok {
		return f.Msg, true
	}
	return "", false
}
