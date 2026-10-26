package message

import (
	"fmt"
)

// Usage is message.sh's grammar line.
const Usage = "message: status|wait|read [claim]|release <claim>|ack <claim> <id> <record>|send <to> <file> <kind> [reply]|hold <name> <reason>|unhold <name>"

// Help is `herdsman message --help`: the grammar and each command's exit codes.
const Help = `usage: herdsman message [--json] <action> [args]   (HERDSMAN_NAME: your enrolled name)
  status                         channel metadata, read-only (--json: one object)
  wait                           wait for a message or a health change (--json: one event object)
  read [claim]                   claim ready messages; print manifests, never bodies
  release <claim>                release a fully acknowledged claim
  ack <claim> <id> <record>      acknowledge a claimed message after durable intake
  send <to> <file> <kind> [reply]
  hold <name> <reason> | unhold <name>
  register propose <name> adjutant|orchestrator | approve <checksum> | retire <name> <checksum>
  courier                        SWEEP=60 ALERT_AFTER=600 RUN_FOR=0
  card-watch [--json]            INTERVAL=5 RUN_FOR=0 (it prints no event lines; alerts go to herdr)
  resume-goal <name>             RESUME_TARGET_SUM (--json: {"type":"resume","status":"held|noop|sent","text"})
  authority                      RELAY or HUMAN (--json: {"type":"authority","status":"relay|human","text"})
  leak-gate <file> [heading...]  LEAK_ALLOW_FILE LEAK_PATTERN
settings: SWEEP, ALERT_AFTER, RUN_FOR, INTERVAL, RECHECK, DEADLINE, HEALTH_AFTER and EVENT_READ_MAX are
durations (seconds, or with an s, m, h or d suffix); LOCK_WAIT is a count. A bad value exits 2 first.

exit codes:
  0  done (wait: a message or TICK; resume-goal: NOOP or SENT; authority: RELAY; leak-gate: clean)
  1  wait: HEALTH; leak-gate: a leak or a missing section; HERDSMAN_NAME or HERDSMAN_INBOX unset
  2  usage, an invalid setting, or a refusal (the reason on stderr)
  3  authority: HUMAN; resume-goal: HELD
`

// Main is `herdsman message <action> ...`: message.sh's grammar with
// HERDSMAN_NAME as the caller's enrolled name, and the commands message.sh
// does not front (register, courier, card-watch, resume-goal, authority,
// leak-gate), which take their scripts' arguments.
func (e *Env) Main(args []string) int {
	var rest []string
	for _, a := range args {
		switch a {
		case "--json":
			e.JSON = true
		case "-h", "--help":
			fmt.Fprint(e.Stdout, Help)
			return 0
		default:
			rest = append(rest, a)
		}
	}
	args = rest
	if len(args) > 0 && args[0] == "help" {
		fmt.Fprint(e.Stdout, Help)
		return 0
	}
	if len(args) > 0 {
		switch args[0] {
		case "register":
			return e.Register(args[1:])
		case "courier":
			return e.Courier(args[1:])
		case "card-watch":
			return e.CardWatch(args[1:])
		case "resume-goal":
			return e.Resume(args[1:])
		case "authority":
			return e.Authority(args[1:])
		case "leak-gate":
			return LeakGate(args[1:], e.env, e.Stdout, e.Stderr)
		}
	}
	me := e.env("HERDSMAN_NAME")
	if me == "" {
		fmt.Fprintln(e.Stderr, "HERDSMAN_NAME: set the enrolled name from session configuration")
		return 1
	}
	action := "status"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	usage := func() int {
		fmt.Fprintln(e.Stderr, Usage)
		return 2
	}
	n := len(args)
	switch action {
	case "status":
		if n != 0 {
			return usage()
		}
		return e.Status([]string{me})
	case "wait":
		if n != 0 {
			return usage()
		}
		return e.Wait([]string{me})
	case "read":
		if n > 1 {
			return usage()
		}
		return e.Read(append([]string{me}, args...))
	case "release":
		if n != 1 {
			return usage()
		}
		return e.Read([]string{me, args[0], "--release"})
	case "ack":
		if n != 3 {
			return usage()
		}
		return e.Ack(append([]string{me}, args...))
	case "send":
		if n != 3 && n != 4 {
			return usage()
		}
		return e.Send(append([]string{me}, args...))
	case "hold":
		if n != 2 {
			return usage()
		}
		return e.Hold(append([]string{me, "hold"}, args...))
	case "unhold":
		if n != 1 {
			return usage()
		}
		return e.Hold(append([]string{me, "unhold"}, args...))
	}
	return usage()
}

// Authority is message-authority.sh: RELAY and exit 0 under the protected
// setup, HUMAN and exit 3 otherwise.
func (e *Env) Authority(args []string) int {
	if len(args) != 0 {
		return e.finish(die("herdsman message authority takes no arguments"))
	}
	if err := e.open(true); err != nil {
		return e.finish(err)
	}
	l, code := e.ch.Authority(e.ctx(), e.Client)
	status := "relay"
	if code != 0 {
		status = "human"
	}
	e.statusLine("authority", status, l)
	return e.finish(exitCode(code))
}

func exitCode(code int) error {
	if code == 0 {
		return nil
	}
	return exitWith(code)
}

// StatusLine is one --json line of authority or resume-goal: type
// (authority or resume), status (relay or human; held, noop or sent) and
// the text line the command prints without --json.
type StatusLine struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

func (e *Env) statusLine(kind, status, text string) {
	e.event(StatusLine{Type: kind, Status: status, Text: text}, text)
}
