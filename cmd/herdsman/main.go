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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/termqr"
	"github.com/arafathusayn/herdsman/internal/web"
)

// Set by the release build.
var (
	version = "dev"
	commit  = "none"
)

const usage = `herdsman: run a team of coding agents inside herdr.

Usage:
  herdsman web [--port 7878] [--host <ip>] [--allow-host <name>]... [--inbox <folder>]
               [--client <url>] [--session <name>] [--route <folder>]... [--open] [--no-qr]
  herdsman version
  herdsman help

Commands:
  web      Serve the web app and print its URL, with a one-run token, and a QR code.
           It listens on 127.0.0.1 unless --host names one other address,
           such as a LAN or Tailscale IP, to open it from a phone. The app may
           also be hosted elsewhere (--client) and reach this server across
           origins; every browser still pairs with a code printed here.
  version  Print the version.
  help     Print this help.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "herdsman:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "version", "--version":
		fmt.Fprintf(stdout, "herdsman %s (%s) %s/%s\n", version, commit, runtime.GOOS, runtime.GOARCH)
		return nil
	case "web":
		return runWeb(args[1:], stdout, stderr)
	}
	return fmt.Errorf("unknown command %q; run `herdsman help`", args[0])
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
	clientFlag := fs.String("client", os.Getenv("HERDSMAN_CLIENT"), "URL of a hosted herdsman web app to print a link for (default: HERDSMAN_CLIENT)")
	var routes, allow multi
	fs.Var(&routes, "route", "route folder to show first (repeatable)")
	fs.Var(&allow, "allow-host", "another host name to answer to, such as the domain of a proxy or tunnel in front of this server (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if ip := net.ParseIP(*host); ip != nil && ip.IsUnspecified() {
		return fmt.Errorf("--host %s listens on every interface; name the one address to use, such as a LAN or Tailscale IP", *host)
	}
	if *inbox != "" && !filepath.IsAbs(*inbox) {
		return fmt.Errorf("--inbox %s must be an absolute path", *inbox)
	}
	var clientURL *neturl.URL
	if *clientFlag != "" {
		u, err := neturl.Parse(*clientFlag)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("--client %s must be an http or https URL", *clientFlag)
		}
		clientURL = u
	}
	hosts := extraHosts(*host)
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
	srv := &http.Server{
		Handler: web.NewServer(client, hub, queue, web.Config{
			Token: token, Port: actual, Hosts: hosts, Inbox: *inbox,
			OnPair: func(n web.PairNotice) { printPair(stdout, n) },
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(stderr, "", log.LstdFlags),
	}
	go hub.Run(ctx)
	go queue.Run(ctx)

	url := fmt.Sprintf("http://%s/#t=%s", net.JoinHostPort(*host, strconv.Itoa(actual)), token)
	// The URL lets a browser ask to pair; each browser must then type the
	// code this terminal prints for it.
	fmt.Fprintf(stdout, "herdsman web %s on herdr %s (%s)\n\n  %s\n\n", version, pong.Version, sock, url)
	if clientURL != nil {
		fmt.Fprintf(stdout, "  Hosted app: %s\n\n", clientLink(clientURL, net.JoinHostPort(*host, strconv.Itoa(actual)), token))
	}
	if !*noQR && isTerminal(stdout) {
		if err := termqr.Write(stdout, url, "  "); err != nil {
			fmt.Fprintf(stderr, "herdsman: no QR code: %v\n", err)
		}
		fmt.Fprintln(stdout)
	}
	if len(extraHosts(*host)) > 0 {
		fmt.Fprintf(stdout, "Listening on %s. Each browser that opens the URL must type the pairing code printed here.\n", *host)
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
func clientLink(u *neturl.URL, hostport, token string) string {
	c := *u
	q := neturl.Values{"host": {"http://" + hostport}, "t": {token}}
	c.Fragment, c.RawFragment = "", ""
	return c.String() + "#" + q.Encode()
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
