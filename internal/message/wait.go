package message

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Wait is message-wait.sh <me>: one wake per change of the unread state,
// the sender notices, the overdue deliveries and the health flags; TICK at
// DEADLINE. Change fingerprints persist across runs in state/wait-<me>.
func (e *Env) Wait(args []string) int { return e.finish(e.wait(args)) }

// uintEnv is ${K:-def} checked by uint.
func (e *Env) uintEnv(k, def string, lo, hi int) (int, error) {
	n, err := inbox.Uint(k, e.envDefault(k, def), lo, hi)
	if err != nil {
		return 0, die("%s", err.Error())
	}
	return n, nil
}

// timingSweep reads .sweep of state/timing.json as `jq -er .sweep` feeds
// $((sweep+10)): an integral number. Anything else ends the run with exit
// 1, as the script's failed substitution or arithmetic under set -e.
func (e *Env) timingSweep() (int64, bool, error) {
	p := e.path("state", "timing.json")
	if !inbox.Regular(e.ch.Dir, p) {
		return 60, false, nil
	}
	v, _ := decodeFile(p)
	n, ok := inbox.Int(inbox.Object(v), "sweep")
	if !ok {
		return 0, true, &exitErr{code: 1, msg: "state/timing.json: sweep is not an integer"}
	}
	return n, true, nil
}

func (e *Env) wait(args []string) error {
	if len(args) != 1 || !inbox.NameOK(args[0]) {
		return die("wait <me>")
	}
	me := args[0]
	recheck, err := e.uintEnv("RECHECK", "60", 1, 3600)
	if err != nil {
		return err
	}
	deadline, err := e.uintEnv("DEADLINE", "1500", 1, 1500)
	if err != nil {
		return err
	}
	if err := e.open(false); err != nil {
		return err
	}
	sweep, set, err := e.timingSweep()
	if err != nil {
		return err
	}
	if set && int64(e.ch.HealthAfter) <= sweep+10 {
		return die("health threshold conflicts with courier timing")
	}
	end := e.unix() + int64(deadline)
	next := int64(0)
	var stream Stream
	defer func() {
		if stream != nil {
			stream.Close()
		}
	}()
	box := e.path(me)
	for {
		t := e.unix()
		if t >= next {
			flags := e.healthSnapshot()
			fault := ""
			if len(flags) == 0 {
				if _, err := e.selfPeer(me); err != nil {
					if msg, ok := inbox.IsFatal(err); ok {
						fmt.Fprintln(e.Stderr, msg)
					}
					fault = "identity-unavailable"
				}
			}
			var found []string
			notices := ""
			pending := 0
			if st, err := os.Stat(filepath.Join(box, "new")); err == nil && st.IsDir() {
				for _, f := range e.ch.Unacked(box) {
					found = append(found, filepath.Base(f))
				}
				sort.Strings(found)
				for _, n := range glob(filepath.Join(box, "notices"), "*.json") {
					if !inbox.Regular(e.ch.Dir, n) {
						continue
					}
					d, _ := inbox.DigestFile(n)
					notices += " " + filepath.Base(n) + ":" + d
				}
				for _, f := range inbox.MdFiles(filepath.Join(box, "new")) {
					stamp, err := inbox.Mtime(f)
					if err != nil {
						continue
					}
					if t-stamp > sweep {
						pending++
					}
				}
			}
			flagText := strings.Join(flags, "\n")
			text := strings.Join(found, "\n") + "\n" + notices + "\n" + strconv.Itoa(pending) + "\n" + flagText + "\n" + fault + "\n"
			fingerprint := inbox.Digest([]byte(text))
			state := e.path("state", "wait-"+me)
			previous := ""
			if inbox.Regular(e.ch.Dir, state) {
				previous = inbox.CatText(state)
			}
			if previous != fingerprint {
				if err := e.ch.WriteText(state, fingerprint); err != nil {
					return writeFailed(err)
				}
				switch {
				case flagText+fault != "":
					fmt.Fprintf(e.Stdout, "HEALTH %s %s\n", flagText, fault)
					return exitWith(1)
				case len(found) > 0:
					fmt.Fprintf(e.Stdout, "MESSAGE %s: run herdsman message read\n", me)
					return nil
				case notices != "":
					fmt.Fprintln(e.Stdout, "HEALTH sender notice; inspect herdsman message status")
					return exitWith(1)
				case pending > 0:
					fmt.Fprintln(e.Stdout, "HEALTH pending delivery; inspect herdsman message status")
					return exitWith(1)
				}
			}
			next = t + int64(recheck)
		}
		if t >= end {
			fmt.Fprintln(e.Stdout, "TICK message-wait deadline")
			return nil
		}
		if stream == nil {
			s, err := e.openStream([]map[string]any{{"type": "pane.updated"}})
			if err != nil {
				e.sleep(time.Second)
				continue
			}
			stream = s
		}
		t = e.unix()
		waitFor := next - t
		if waitFor <= 0 {
			continue
		}
		if waitFor > end-t {
			waitFor = end - t
		}
		if waitFor <= 0 {
			continue
		}
		l, ok := e.nextHint(&stream, waitFor)
		if ok {
			if pane, _ := eventPane(l); pane == e.pane() {
				next = 0
			}
		}
	}
}

// openStream is events_open: a subscription, or an error when herdr has no
// socket or refuses it.
func (e *Env) openStream(subs []map[string]any) (Stream, error) {
	if e.env("HERDR_SOCKET_PATH") == "" && e.Events == nil {
		return nil, errNoSocket
	}
	return e.events().Open(e.ctx(), subs)
}

var errNoSocket = &exitErr{code: 1, msg: "no herdr socket"}

// nextHint is events_next: the next channel hint within seconds, capped by
// EVENT_READ_MAX. A timeout or a dropped stream closes the stream (the
// caller opens a new one and rescans); another event is no hint.
func (e *Env) nextHint(stream *Stream, seconds int64) (string, bool) {
	if v := e.env("EVENT_READ_MAX"); v != "" {
		if n, err := inbox.Uint("EVENT_READ_MAX", v, 1, 3600); err == nil && seconds > int64(n) {
			seconds = int64(n)
		}
	}
	l, err := (*stream).Next(time.Duration(seconds) * time.Second)
	if err != nil {
		(*stream).Close()
		*stream = nil
		return "", false
	}
	switch hint(l) {
	case 0:
		return l, true
	case 3:
		return "", false
	}
	(*stream).Close()
	*stream = nil
	return "", false
}
