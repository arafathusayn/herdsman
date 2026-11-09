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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/afk"
	"github.com/arafathusayn/herdsman/internal/doctor"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/secrets"
	"github.com/arafathusayn/herdsman/internal/termqr"
	"github.com/arafathusayn/herdsman/internal/web"
	"github.com/arafathusayn/herdsman/sdk/client"
)

const webHelp = `usage: herdsman web [--host <address>] [--port <n>] [--no-qr] [--open] [--session <name>] [--inbox <folder>] [--client <url>] [--public-url <url>] [--route <folder>]... [--allow-host <host>]...

Serves the web app and prints its URL, with a one-run token, and a QR code.

  --port <n>          port to listen on, 0 picks a free one (default 7878)
  --host <address>    address to listen on: loopback, or one LAN or Tailscale address (default 127.0.0.1)
  --no-qr             do not print the QR code
  --open              open the app in the default browser
  --session <name>    herdr session name (default: HERDR_SESSION or the default session)
  --inbox <folder>    private message folder of the adjutant channel (default: HERDSMAN_INBOX)
  --client <url>      URL of a hosted web app to print a link for, or none (default: HERDSMAN_CLIENT, else the hosted app)
  --public-url <url>  HTTPS address in front of this server; the QR code then opens the hosted app
  --route <folder>    route folder to show first (repeatable)
  --allow-host <host> another host name to answer to, such as a proxy or tunnel's (repeatable)

exit codes:
  0  stopped, or printed this help
  1  a bad argument, or the server could not start
`

func runWeb(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(fs.Output(), webHelp) }
	port := fs.Int("port", 7878, "port to listen on (0 picks a free one)")
	host := fs.String("host", "127.0.0.1", "address to listen on: loopback, or one LAN or Tailscale address")
	noQR := fs.Bool("no-qr", false, "do not print the QR code")
	session := fs.String("session", "", "herdr session name (default: HERDR_SESSION or the default session)")
	open := fs.Bool("open", false, "open the app in the default browser")
	inbox := fs.String("inbox", os.Getenv("HERDSMAN_INBOX"), "private herdsman message folder of the adjutant channel (default: HERDSMAN_INBOX)")
	clientDefault := defaultClient
	if v, ok := os.LookupEnv("HERDSMAN_CLIENT"); ok {
		clientDefault = v
	}
	clientFlag := fs.String("client", clientDefault, "URL of a hosted herdsman web app to print a link for, or none (default: HERDSMAN_CLIENT, else "+defaultClient+")")
	publicFlag := fs.String("public-url", "", "HTTPS address in front of this server, such as tailscale serve or a tunnel; the QR code then opens the hosted app")
	var routes, allow multi
	fs.Var(&routes, "route", "route folder to show first (repeatable)")
	fs.Var(&allow, "allow-host", "another host name to answer to, such as the domain of a proxy or tunnel in front of this server (repeatable)")
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			fs.SetOutput(stdout)
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if ip := net.ParseIP(*host); ip != nil && ip.IsUnspecified() {
		return fmt.Errorf("--host %s listens on every interface; name the one address to use, such as a LAN or Tailscale IP", *host)
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
	ctx, stop := webContext()
	defer stop()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pong, err := client.Check(cctx)
	cancel()
	if err != nil {
		return err
	}

	gateway, err := gatewaySocket(*host, os.Getenv)
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
	redactor, errorLog, err := webSecrets(os.Getenv, secrets.System(), stderr)
	if err != nil {
		return err
	}
	profile, err := doctor.ConfiguredProfile(repoRoot())()
	if err != nil {
		return err
	}
	hostport := net.JoinHostPort(*host, strconv.Itoa(actual))
	qr := !*noQR && isTerminal(stdout)
	srv := &http.Server{
		Handler: web.NewServer(client, hub, queue, web.Config{
			Token: token, Redactor: redactor, RefuseAgentStart: webAgentRefusal(profile), Port: actual, Hosts: hosts, Inbox: *inbox, Executable: runningBinary(),
			DaemonSocket: gateway,
			Origins:      gatewayOrigins(clientURL),
			Choices:      webChoices(),
			GitHub:       webGitHub,
			ActivityFile: afk.DefaultWebFile(),
			OnPair:       func(n web.PairNotice) { printPair(stdout, n) },
			OnRotate: func(t string) {
				fmt.Fprintln(stdout, "\n  Too many wrong pairing codes: the links above no longer work. Paired browsers keep working. New links:")
				printLinks(stdout, stderr, makeLinks(hostport, t, publicURL, clientURL), qr)
			},
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(errorLog, "", log.LstdFlags),
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
	if gateway == "" {
		fmt.Fprintln(stdout, "The RPC gateway is off: it answers only when this server listens on localhost.")
	}
	fmt.Fprintln(stdout, "Ctrl+C stops the server.")
	if *open {
		openBrowser(url)
	}

	return serve(ctx, srv, ln)
}

// webContext is the server's lifetime: until Ctrl+C or SIGTERM. Tests
// replace it with a context they cancel.
var webContext = signalContext

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

// extraHosts returns host when it is not a loopback name, for the server's
// Host check.
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

// runningBinary is this herdsman binary, links resolved: a new route's
// checkpointer pane runs it as `<binary> checkpointer`.
func runningBinary() string { return inbox.RunningExecutable() }

// gatewaySocket is the daemon socket to forward RPC calls to. It is empty when
// the server listens beyond this machine, which turns the gateway off.
func gatewaySocket(host string, getenv func(string) string) (string, error) {
	if len(extraHosts(host)) > 0 {
		return "", nil
	}
	return client.SocketPath(getenv)
}

// gatewayOrigins are the origins besides this server's own that may call the
// gateway: the hosted app, when one is configured.
func gatewayOrigins(hosted *neturl.URL) []string {
	if hosted == nil {
		return nil
	}
	return []string{hosted.Scheme + "://" + hosted.Host}
}
