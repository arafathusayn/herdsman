package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

const noticeHelp = `usage: herdsman notice wait [--after <id>] [--json]
       herdsman notice read [<id>] [--json]
       herdsman notice ack <id> --token <token> --outcome <outcome> --record <text|@file> [--json]

How a managed agent on herdr-core pulls its instructions. The commands reach
the daemon's worker socket, which the core wrapper names in
HERDSMAN_NOTICE_SOCKET, and answer only for the calling agent's own
execution, identified by its process. Every reply prints the next command.

exit codes:
  0  done: something to act on was printed
  1  the daemon refused, or this is not a managed execution
  2  bad usage
  3  wait only: this wait was replaced by a newer one, or the execution stopped
`

const noticeWaitHelp = `usage: herdsman notice wait [--after <id>] [--json]

Waits until a notice of this execution can be claimed, and prints its id and
the command that reads it. Hold it as a background command. With --after,
the wait says that you work on that claim: it is answered by the route's
final-report notice, by the claim when recovery is due, or by the next notice
once the claim is acknowledged. One command polls many times; a newer wait of
the same execution replaces it, and it exits with code 3.

exit codes:
  0  a notice or an open claim was printed
  1  the daemon refused, or this is not a managed execution
  2  bad usage
  3  replaced by a newer wait, or the execution stopped
`

const noticeReadHelp = `usage: herdsman notice read [<id>] [--json]

Claims a notice and prints its content and claim token. A repeat read prints
the same token, marked as claimed before; it is not permission to do the work
again. Without an id it prints the open claim, or claims the next notice. A
claim that a gate refuses leaves the notice issued and says why.

exit codes:
  0  printed
  1  the daemon refused, or this is not a managed execution
  2  bad usage
`

const noticeAckHelp = `usage: herdsman notice ack <id> --token <token> --outcome <outcome> --record <text|@file> [--json]

Closes a claim. The outcome is completed, interrupted (the work stopped
part-way; the record says how far) or abandoned (you will not go on, and why).
The record is required for every outcome; @file reads it from a file. An
acknowledgement is accepted in every route state.

exit codes:
  0  acknowledged
  1  the daemon refused, or this is not a managed execution
  2  bad usage
`

// dialNotice reaches the worker socket; tests replace it.
var dialNotice = func() (daemonv1.NoticeService, func(), error) {
	return client.DialNotice("", os.Getenv, nil)
}

// noticePause is the wait between retries of a poll that failed: the backoff
// of the reconcilers, from a quarter second up to five.
var noticePause = func(attempt int) time.Duration {
	d := 250 * time.Millisecond << min(attempt, 5)
	return min(d, 5*time.Second)
}

func init() {
	register(command{name: "notice", summary: "A managed agent's instructions on herdr-core: notice wait, notice read, notice ack.", help: noticeHelp,
		subHelp: map[string]string{"wait": noticeWaitHelp, "read": noticeReadHelp, "ack": noticeAckHelp}, run: runNotice})
}

var noticeVerbs = map[string]func([]string, io.Writer, io.Writer) int{
	"wait": runNoticeWait,
	"read": runNoticeRead,
	"ack":  runNoticeAck,
}

func runNotice(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, noticeHelp)
		return 2
	}
	run, ok := noticeVerbs[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "notice: unknown verb %q\n%s", args[0], noticeHelp)
		return 2
	}
	return run(args[1:], stdout, stderr)
}

func newWaitID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// retryable is a failure a poll tries again: busy, a lost connection, or a
// daemon that is restarting.
func retryable(err error) bool {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code == rpc.Unavailable || e.Code == rpc.Canceled || e.Code == rpc.DeadlineExceeded || e.Code == rpc.Unknown
	}
	return true // a transport failure: the socket went away
}

func runNoticeWait(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("notice wait", stderr)
	after := fs.String("after", "", "the notice whose claim you work on")
	asJSON := fs.Bool("json", false, "print the answer as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 0 {
		return usageError(stderr, "notice wait takes no argument")
	}
	service, closeService, err := dialNotice()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	// One command, many polls: its first call asks for the wait with a wait
	// id of its own; each later poll renews with the generation it got. On a
	// failure it repeats its first call while it has no generation.
	req := &daemonv1.NoticeServiceWaitRequest{WaitId: newWaitID(), After: *after}
	for attempt := 0; ; {
		res, err := service.Wait(context.Background(), req)
		if err != nil {
			if !retryable(err) {
				return fail(stderr, err)
			}
			time.Sleep(noticePause(attempt))
			attempt++
			continue
		}
		attempt = 0
		if res.GetWaitGeneration() > 0 {
			req.WaitGeneration = res.GetWaitGeneration()
		}
		switch res.GetAnswer() {
		case "none":
			continue
		case "replaced", "stopped":
			if *asJSON {
				_ = printProto(stdout, res)
			} else {
				fmt.Fprintf(stdout, "%s: %s\nthis wait ends; do not start another unless a reply tells you to\n", res.GetAnswer(), res.GetReason())
			}
			return 3
		}
		if *asJSON {
			if err := printProto(stdout, res); err != nil {
				return fail(stderr, err)
			}
			return 0
		}
		n := res.GetNotice()
		if res.GetAnswer() == "recovery" {
			c := res.GetClaim()
			fmt.Fprintf(stdout, "open claim: notice %s (%s), claimed %s\n", n.GetId(), n.GetKind(), time.UnixMilli(c.GetClaimedAt()).UTC().Format(time.RFC3339))
			if c.GetResumedSince() {
				fmt.Fprintln(stdout, "the route was paused and resumed since the claim")
			}
			fmt.Fprintf(stdout, "processing record: %s\n", c.GetRecordPath())
			fmt.Fprintln(stdout, "read your record before you act again; do not repeat work it records")
		} else {
			fmt.Fprintf(stdout, "notice %s (%s)\n", n.GetId(), n.GetKind())
		}
		fmt.Fprintf(stdout, "next: %s\n", res.GetNext())
		return 0
	}
}

func runNoticeRead(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("notice read", stderr)
	asJSON := fs.Bool("json", false, "print the answer as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) > 1 {
		return usageError(stderr, "notice read takes at most one notice id")
	}
	id := ""
	if len(positional) == 1 {
		id = positional[0]
	}
	service, closeService, err := dialNotice()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	res, err := service.Read(context.Background(), &daemonv1.NoticeServiceReadRequest{Id: id})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		if err := printProto(stdout, res); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	n := res.GetNotice()
	switch {
	case n == nil:
		fmt.Fprintln(stdout, "no notice waits for this execution")
	case res.GetRefused() != "":
		fmt.Fprintf(stdout, "notice %s: the claim is refused by the %s gate; the notice stays issued\n", n.GetId(), res.GetRefused())
	case n.GetState() == "handled":
		fmt.Fprintf(stdout, "notice %s is handled (%s); there is nothing to do\nrecord: %s\n", n.GetId(), res.GetOutcome(), res.GetRecord())
	default:
		fmt.Fprintf(stdout, "notice %s (%s)", n.GetId(), n.GetKind())
		if res.GetClaimedBefore() {
			fmt.Fprint(stdout, ", claimed before: this is not permission to do the work again")
		}
		fmt.Fprintf(stdout, "\ntoken: %s\n", res.GetToken())
		if c := res.GetClaim(); c != nil {
			fmt.Fprintf(stdout, "processing record: %s\n", c.GetRecordPath())
		}
		if !n.GetSafeToRepeat() {
			fmt.Fprintln(stdout, "doing this twice is not safe: if your record does not show how far it went, acknowledge interrupted")
		}
		fmt.Fprintf(stdout, "\n%s\n\n", res.GetContent())
	}
	fmt.Fprintf(stdout, "next: %s\n", res.GetNext())
	return 0
}

func runNoticeAck(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("notice ack", stderr)
	token := fs.String("token", "", "the claim token read printed")
	outcome := fs.String("outcome", "", "completed, interrupted or abandoned")
	record := fs.String("record", "", "what was done and how far it went, or @file")
	asJSON := fs.Bool("json", false, "print the answer as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(stderr, "notice ack takes one notice id")
	}
	if *token == "" || *outcome == "" || strings.TrimSpace(*record) == "" {
		return usageError(stderr, "notice ack needs --token, --outcome and --record")
	}
	text := *record
	if path, ok := strings.CutPrefix(text, "@"); ok {
		b, err := os.ReadFile(path)
		if err != nil {
			return fail(stderr, err)
		}
		text = string(b)
	}
	service, closeService, err := dialNotice()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	res, err := service.Ack(context.Background(), &daemonv1.NoticeServiceAckRequest{Id: positional[0], Token: *token, Outcome: *outcome, Record: text})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		if err := printProto(stdout, res); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	if res.GetRepeated() {
		fmt.Fprintf(stdout, "notice %s was acknowledged already, with this outcome\n", positional[0])
	} else {
		fmt.Fprintf(stdout, "notice %s: %s\n", positional[0], *outcome)
	}
	fmt.Fprintf(stdout, "next: %s\n", res.GetNext())
	return 0
}
