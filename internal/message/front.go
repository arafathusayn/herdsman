package message

import (
	"fmt"
)

// Usage is message.sh's grammar line.
const Usage = "message: status|wait|read [claim]|release <claim>|ack <claim> <id> <record>|send <to> <file> <kind> [reply]|hold <name> <reason>|unhold <name>"

// Main is `herdsman message <action> ...`: message.sh's grammar with
// HERDSMAN_NAME as the caller's enrolled name, and the commands message.sh
// does not front (register, courier, card-watch, resume-goal, authority,
// leak-gate), which take their scripts' arguments.
func (e *Env) Main(args []string) int {
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
	fmt.Fprintln(e.Stdout, l)
	return e.finish(exitCode(code))
}

func exitCode(code int) error {
	if code == 0 {
		return nil
	}
	return exitWith(code)
}
