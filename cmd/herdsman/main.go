// Command herdsman runs herdsman's tools: today the web app that operates
// every agent of a herdr session from the browser.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/termqr"
	"github.com/arafathusayn/herdsman/internal/web"
)

// defaultClient is the hosted copy of the app, built from main.
const defaultClient = "https://herdsman.arafat.ai"

// Set by the release build.
var (
	version = "dev"
	commit  = "none"
)

const usage = `herdsman: run a team of coding agents inside herdr.

Usage:
  herdsman web [--port 7878] [--host <ip>] [--allow-host <name>]... [--inbox <folder>] [--skill-dir <folder>]
               [--client <url>|none] [--public-url <https url>] [--session <name>]
               [--route <folder>]... [--open] [--no-qr]
  herdsman <command> [args]   (the commands below)
  herdsman version
  herdsman help

Commands:
  web      Serve the web app and print its URL, with a one-run token, and a QR code.
           It listens on 127.0.0.1 unless --host names one other address,
           such as a LAN or Tailscale IP, to open it from a phone. The app is
           also hosted at ` + defaultClient + ` (--client) and reaches this
           server across origins; every browser still pairs with a code
           printed here. A phone can use the hosted app only through HTTPS:
           with --public-url (for example the address of tailscale serve),
           the QR code opens the hosted app.
`

const usageTail = `  version  Print the version.
  help     Print this help.
`

// command is one subcommand of the binary. Each lives in its own
// cmd_<name>.go file and registers itself in init.
type command struct {
	name    string
	summary string // one line for help
	// run returns the exit code; the command prints its own output.
	run func(args []string, stdin io.Reader, stdout, stderr io.Writer) int
}

var commands = map[string]command{}

func register(c command) {
	if _, dup := commands[c.name]; dup {
		panic("herdsman: command registered twice: " + c.name)
	}
	commands[c.name] = c
}

// printUsage prints the usage with every registered command.
func printUsage(w io.Writer) {
	fmt.Fprint(w, usage)
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-8s %s\n", n, commands[n].summary)
	}
	fmt.Fprint(w, usageTail)
}

// exitError ends the program with a code; its message, when there is one,
// was not printed yet.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	if e, ok := errors.AsType[*exitError](err); ok {
		if e.msg != "" {
			fmt.Fprintln(os.Stderr, "herdsman:", e.msg)
		}
		os.Exit(e.code)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdsman:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "version", "--version":
		fmt.Fprintf(stdout, "herdsman %s (%s) %s/%s\n", version, commit, runtime.GOOS, runtime.GOARCH)
		return nil
	case "web":
		return runWeb(args[1:], stdout, stderr)
	}
	if c, ok := commands[args[0]]; ok {
		if code := c.run(args[1:], os.Stdin, stdout, stderr); code != 0 {
			return &exitError{code: code}
		}
		return nil
	}
	printUsage(stderr)
	return &exitError{code: 2, msg: fmt.Sprintf("unknown command %q; run `herdsman help`", args[0])}
}

type multi []string

func (m *multi) String() string     { return fmt.Sprint(*m) }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func runWeb(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 7878, "port to listen on (0 picks a free one)")
	host := fs.String("host", "127.0.0.1", "address to listen on: loopback, or one LAN or Tailscale address")
	noQR := fs.Bool("no-qr", false, "do not print the QR code")
	session := fs.String("session", "", "herdr session name (default: HERDR_SESSION or the default session)")
	open := fs.Bool("open", false, "open the app in the default browser")
	inbox := fs.String("inbox", os.Getenv("HERDSMAN_INBOX"), "private herdsman message folder of the adjutant channel (default: HERDSMAN_INBOX)")
	skillDir := fs.String("skill-dir", "", "herdsman skill folder whose scripts/checkpointer.sh a new route runs (default: the installed skill)")
	clientDefault := defaultClient
	if v, ok := os.LookupEnv("HERDSMAN_CLIENT"); ok {
		clientDefault = v
	}
	clientFlag := fs.String("client", clientDefault, "URL of a hosted herdsman web app to print a link for, or none (default: HERDSMAN_CLIENT, else "+defaultClient+")")
	publicFlag := fs.String("public-url", "", "HTTPS address in front of this server, such as tailscale serve or a tunnel; the QR code then opens the hosted app")
	var routes, allow multi
	fs.Var(&routes, "route", "route folder to show first (repeatable)")
	fs.Var(&allow, "allow-host", "another host name to answer to, such as the domain of a proxy or tunnel in front of this server (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if ip := net.ParseIP(*host); ip != nil && ip.IsUnspecified() {
		return fmt.Errorf("--host %s listens on every interface; name the one address to use, such as a LAN or Tailscale IP", *host)
	}
	if *skillDir != "" && !filepath.IsAbs(*skillDir) {
		return fmt.Errorf("--skill-dir %s must be an absolute path", *skillDir)
	}
	if *inbox != "" && !filepath.IsAbs(*inbox) {
		return fmt.Errorf("--inbox %s must be an absolute path", *inbox)
	}
	var clientURL *neturl.URL
	if *clientFlag != "" && *clientFlag != "none" {
		u, err := neturl.Parse(*clientFlag)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("--client %s must be an http or https URL, or none", *clientFlag)
		}
		clientURL = u
	}
	var publicURL *neturl.URL
	if *publicFlag != "" {
		u, err := neturl.Parse(*publicFlag)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("--public-url %s must be an https origin, such as https://box.tailnet.ts.net", *publicFlag)
		}
		publicURL = &neturl.URL{Scheme: "https", Host: u.Host}
	}
	hosts := extraHosts(*host)
	if publicURL != nil {
		hosts = append(hosts, publicURL.Hostname())
	}
	for _, a := range allow {
		for n := range strings.SplitSeq(a, ",") {
			if n = strings.TrimSpace(n); n != "" {
				hosts = append(hosts, n)
			}
		}
	}
	for i, r := range routes {
		abs, err := filepath.Abs(r)
		if err != nil {
			return err
		}
		routes[i] = abs
	}

	sock, err := herdr.SocketPath(*session)
	if err != nil {
		return err
	}
	client := herdr.New(sock)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pong, err := client.Check(cctx)
	cancel()
	if err != nil {
		return err
	}

	ln, actual, err := web.Listen(*host, *port)
	if err != nil {
		return err
	}
	hub := web.NewHub(client, pong, routes)
	queue := web.NewQueue(client, hub)
	token := web.NewToken()
	hostport := net.JoinHostPort(*host, strconv.Itoa(actual))
	qr := !*noQR && isTerminal(stdout)
	srv := &http.Server{
		Handler: web.NewServer(client, hub, queue, web.Config{
			Token: token, Port: actual, Hosts: hosts, Inbox: *inbox, SkillDir: *skillDir,
			OnPair: func(n web.PairNotice) { printPair(stdout, n) },
			OnRotate: func(t string) {
				fmt.Fprintln(stdout, "\n  Too many wrong pairing codes: the links above no longer work. Paired browsers keep working. New links:")
				printLinks(stdout, stderr, makeLinks(hostport, t, publicURL, clientURL), qr)
			},
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(stderr, "", log.LstdFlags),
	}
	go hub.Run(ctx)
	go queue.Run(ctx)

	// Each link lets a browser ask to pair; each browser must then type the
	// code this terminal prints for it.
	l := makeLinks(hostport, token, publicURL, clientURL)
	url := l.direct
	fmt.Fprintf(stdout, "herdsman web %s on herdr %s (%s)\n", version, pong.Version, sock)
	printLinks(stdout, stderr, l, qr)
	if w := plainHTTPWarning(*host); w != "" {
		fmt.Fprintf(stdout, "Warning: %s\n\n", w)
	}
	if len(extraHosts(*host)) > 0 {
		fmt.Fprintf(stdout, "Listening on %s. Each browser that opens the URL must type the pairing code printed here.\n", *host)
	} else if publicURL != nil {
		fmt.Fprintln(stdout, "Each browser that opens a link must type the pairing code printed here.")
	} else {
		fmt.Fprintln(stdout, "Each browser that opens the URL must type the pairing code printed here. To open it from a phone, add --host <LAN or Tailscale IP>.")
	}
	fmt.Fprintln(stdout, "Ctrl+C stops the server.")
	if *open {
		openBrowser(url)
	}

	return serve(ctx, srv, ln)
}

// serve runs srv until ctx ends, then shuts it down. Requests get ctx as
// their base, so the event streams end with it instead of holding Shutdown
// open until its deadline.
func serve(ctx context.Context, srv *http.Server, ln net.Listener) error {
	srv.BaseContext = func(net.Listener) context.Context { return ctx }
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer scancel()
	return srv.Shutdown(sctx)
}

// printPair shows a pairing code to the person at the terminal; the browser
// that asked must type it in.
func printPair(w io.Writer, n web.PairNotice) {
	code := n.Code[:3] + " " + n.Code[3:]
	fmt.Fprintf(w, "\n  Pairing request from %s (%s)\n  Code: \x1b[1m%s\x1b[0m  (expires %s)\n  Type it in that browser. Ignore it if you did not open the URL just now.\n\n",
		n.Device, n.Remote, code, n.Expires.Local().Format("15:04:05"))
}

// clientLink is the hosted app's URL that adds this server and pairs with it:
// the server's address and the token ride in the fragment, which never
// leaves the browser.
func clientLink(u *neturl.URL, server, token string) string {
	c := *u
	q := neturl.Values{"host": {server}, "t": {token}}
	c.Fragment, c.RawFragment = "", ""
	if c.Path == "" {
		c.Path = "/"
	}
	return c.String() + "#" + q.Encode()
}

// links are the URLs the server prints, and the one its QR code carries.
type links struct {
	direct string // this server's own copy of the app
	public string // the same, through the HTTPS address in front of it
	hosted string // the hosted app, with this server filled in
	qr     string
	qrNote string
}

// makeLinks picks what the QR code opens. A phone can use the hosted app
// only when it reaches this server over HTTPS: an HTTPS page may not call
// plain HTTP on another machine, and a phone's 127.0.0.1 is the phone.
func makeLinks(hostport, token string, public, client *neturl.URL) links {
	l := links{direct: fmt.Sprintf("http://%s/#t=%s", hostport, token)}
	server := "http://" + hostport
	if public != nil {
		server = public.String()
		l.public = server + "/#t=" + token
	}
	if client != nil {
		l.hosted = clientLink(client, server, token)
	}
	switch {
	case l.hosted != "" && l.public != "":
		l.qr, l.qrNote = l.hosted, "The QR code opens the hosted app with this server filled in."
	case l.public != "":
		l.qr, l.qrNote = l.public, "The QR code opens this server's app through "+server+"."
	case l.hosted != "":
		l.qr, l.qrNote = l.direct, "The QR code opens this server's own app. To open the hosted app from a phone, put HTTPS in front of this server (for example `tailscale serve 7878`) and pass its address with --public-url."
	default:
		l.qr, l.qrNote = l.direct, "The QR code opens this server's own app."
	}
	return l
}

// extraHosts returns host when it is not a loopback name, for the server's
// Host check.
// printLinks prints the links that let a browser ask to pair, and the QR code.
func printLinks(stdout, stderr io.Writer, l links, qr bool) {
	fmt.Fprintf(stdout, "\n  %s\n\n", l.direct)
	if l.public != "" {
		fmt.Fprintf(stdout, "  Public: %s\n\n", l.public)
	}
	if l.hosted != "" {
		fmt.Fprintf(stdout, "  Hosted app: %s\n\n", l.hosted)
	}
	if qr {
		if err := termqr.Write(stdout, l.qr, "  "); err != nil {
			fmt.Fprintf(stderr, "herdsman: no QR code: %v\n", err)
		}
		fmt.Fprintf(stdout, "\n  %s\n\n", l.qrNote)
	}
}

// tailnet is the address range Tailscale gives its machines; traffic to them
// is encrypted by Tailscale itself.
var tailnet = []*net.IPNet{mustCIDR("100.64.0.0/10"), mustCIDR("fd7a:115c:a1e0::/48")}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// plainHTTPWarning says why --host on a LAN address is risky: the pairing
// code, the agents' screens and what you type cross the network unencrypted.
func plainHTTPWarning(host string) string {
	if len(extraHosts(host)) == 0 {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, n := range tailnet {
			if n.Contains(ip) {
				return ""
			}
		}
	}
	return fmt.Sprintf("%s serves plain HTTP. Anyone on this network can read the traffic, "+
		"including the agents' screens and what you type to them. Prefer a Tailscale address, "+
		"or put HTTPS in front of this server (for example tailscale serve) and pass --public-url.", host)
}

func extraHosts(host string) []string {
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return []string{host}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
