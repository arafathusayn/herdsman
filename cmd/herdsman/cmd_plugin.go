package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/plugin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// main.go sets these three. pluginApprove is separate from the service because
// no service method may accept a plugin; pluginIsTerminal must ask the kernel.
var (
	pluginService    daemonv1.PluginService
	pluginApprove    func(ctx context.Context, name string, in io.Reader, out io.Writer, isTerminal bool) error
	pluginIsTerminal func(stdin any) bool
)

const pluginHelp = `usage: herdsman plugin --experimental-plugins <verb> [args] [--json]

  list                   the plugins found, usable or not
  describe <name>        a plugin's manifest
  install <path|name>    copy an executable into the plugin folder and ask for approval
  remove <name>          delete an installed plugin and its approval
  verify <name>          run the conformance checks for its extension points
  run <name>             start it in the foreground, for development

Plugins are experimental, so every use needs --experimental-plugins. A plugin
runs only after a person approves its exact checksum, at a terminal.

exit codes:
  0  done
  1  it failed, or a plugin did not pass verify
  2  bad usage
`

func init() {
	register(command{name: "plugin", summary: "Manage plugins (experimental).", help: pluginHelp, run: runPlugin})
}

func runPlugin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	enabled := false
	var rest []string
	for _, arg := range args {
		if arg == "--experimental-plugins" {
			enabled = true
			continue
		}
		rest = append(rest, arg)
	}
	if !enabled {
		return usageError(stderr, "plugins are experimental: pass --experimental-plugins")
	}
	if len(rest) == 0 {
		return usageError(stderr, "usage: herdsman plugin --experimental-plugins list|describe|install|remove|verify|run")
	}
	if pluginService == nil {
		return fail(stderr, errors.New("this build has no plugin host"))
	}
	fmt.Fprintf(stderr, "herdsman plugin: experimental, protocol %s\n", plugin.Protocol())
	verb := rest[0]
	fs := verbFlags("plugin "+verb, stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	positional, _, err := parseVerb(fs, rest[1:])
	if err != nil {
		return 2
	}
	ctx := context.Background()
	wantArgs := 1
	if verb == "list" {
		wantArgs = 0
	}
	switch verb {
	case "list", "describe", "install", "remove", "verify", "run":
	default:
		return usageError(stderr, "unknown plugin verb: "+verb)
	}
	if len(positional) != wantArgs {
		return usageError(stderr, "wrong number of arguments for plugin "+verb)
	}
	switch verb {
	case "list":
		return pluginList(ctx, *asJSON, stdout, stderr)
	case "describe":
		return pluginDescribe(ctx, positional[0], *asJSON, stdout, stderr)
	case "install":
		return pluginInstall(ctx, positional[0], *asJSON, stdin, stdout, stderr)
	case "remove":
		return pluginRemove(ctx, positional[0], *asJSON, stdout, stderr)
	case "verify":
		return pluginVerify(ctx, positional[0], *asJSON, stdout, stderr)
	}
	return pluginRun(ctx, positional[0], stdin, stdout, stderr)
}

func printMessage(stdout, stderr io.Writer, message proto.Message) int {
	data, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(message)
	if err != nil {
		return fail(stderr, err)
	}
	return printJSON(stdout, stderr, json.RawMessage(data))
}

func pluginLine(status *daemonv1.PluginStatus) string {
	state := "approved"
	if !status.GetApproved() {
		state = "unapproved"
	}
	if status.GetPath() == "" {
		state = "built-in"
	}
	version := status.GetVersion()
	if version == "" {
		version = "-"
	}
	line := fmt.Sprintf("%-20s %-10s %-12s", status.GetName(), version, state)
	var points []string
	for _, point := range status.GetExtensionPoints() {
		points = append(points, strings.ToLower(strings.TrimPrefix(point.String(), "EXTENSION_POINT_")))
	}
	line += " " + strings.Join(points, ",")
	if status.GetSkipped() != "" {
		line += "  skipped: " + status.GetSkipped()
	}
	return strings.TrimRight(line, " ")
}

func pluginList(ctx context.Context, asJSON bool, stdout, stderr io.Writer) int {
	response, err := pluginService.List(ctx, &daemonv1.PluginServiceListRequest{})
	if err != nil {
		return fail(stderr, err)
	}
	if asJSON {
		return printMessage(stdout, stderr, response)
	}
	for _, status := range response.GetPlugins() {
		fmt.Fprintln(stdout, pluginLine(status))
	}
	return 0
}

func pluginDescribe(ctx context.Context, name string, asJSON bool, stdout, stderr io.Writer) int {
	response, err := pluginService.Describe(ctx, &daemonv1.PluginServiceDescribeRequest{Name: name})
	if err != nil {
		return fail(stderr, err)
	}
	if asJSON {
		return printMessage(stdout, stderr, response)
	}
	fmt.Fprintln(stdout, pluginLine(response.GetStatus()))
	if description := response.GetManifest().GetDescription(); description != "" {
		fmt.Fprintln(stdout, description)
	}
	return 0
}

func pluginInstall(ctx context.Context, source string, asJSON bool, stdin io.Reader, stdout, stderr io.Writer) int {
	if strings.ContainsRune(source, filepath.Separator) && !strings.Contains(source, "://") {
		absolute, err := filepath.Abs(source)
		if err != nil {
			return fail(stderr, err)
		}
		source = absolute
	}
	response, err := pluginService.Install(ctx, &daemonv1.PluginServiceInstallRequest{Source: source})
	if err != nil {
		return fail(stderr, err)
	}
	status := response.GetStatus()
	if !status.GetApproved() {
		if err := pluginApprove(ctx, status.GetName(), stdin, stderr, pluginIsTerminal(stdin)); err != nil {
			fmt.Fprintf(stderr, "herdsman plugin: %s stays unapproved: %v\n", status.GetName(), err)
		} else {
			// The handshake ran before the approval, so read the plugin again.
			if refreshed, err := pluginService.Install(ctx, &daemonv1.PluginServiceInstallRequest{Source: status.GetPath(), Name: status.GetName()}); err == nil {
				response = refreshed
			}
		}
	}
	if asJSON {
		return printMessage(stdout, stderr, response)
	}
	fmt.Fprintln(stdout, pluginLine(response.GetStatus()))
	return 0
}

func pluginRemove(ctx context.Context, name string, asJSON bool, stdout, stderr io.Writer) int {
	response, err := pluginService.Remove(ctx, &daemonv1.PluginServiceRemoveRequest{Name: name})
	if err != nil {
		return fail(stderr, err)
	}
	if asJSON {
		return printMessage(stdout, stderr, response)
	}
	fmt.Fprintf(stdout, "removed %s\n", name)
	return 0
}

func pluginVerify(ctx context.Context, name string, asJSON bool, stdout, stderr io.Writer) int {
	response, err := pluginService.Verify(ctx, &daemonv1.PluginServiceVerifyRequest{Name: name})
	if err != nil {
		return fail(stderr, err)
	}
	code := 0
	if !response.GetPassed() {
		code = 1
	}
	if asJSON {
		if printed := printMessage(stdout, stderr, response); printed != 0 {
			return printed
		}
		return code
	}
	for _, check := range response.GetChecks() {
		mark := "pass"
		if !check.GetPassed() {
			mark = "FAIL"
		}
		fmt.Fprintf(stdout, "%s  %s", mark, check.GetName())
		if check.GetDetail() != "" {
			fmt.Fprintf(stdout, ": %s", check.GetDetail())
		}
		fmt.Fprintln(stdout)
	}
	return code
}

// pluginRun starts an approved plugin with the terminal attached, so its
// handshake line lands on stdout for whoever is developing it.
func pluginRun(ctx context.Context, name string, stdin io.Reader, stdout, stderr io.Writer) int {
	response, err := pluginService.List(ctx, &daemonv1.PluginServiceListRequest{})
	if err != nil {
		return fail(stderr, err)
	}
	for _, status := range response.GetPlugins() {
		if status.GetName() != name {
			continue
		}
		if status.GetPath() == "" {
			return fail(stderr, fmt.Errorf("%s is built in and has no process", name))
		}
		if !status.GetApproved() {
			return fail(stderr, fmt.Errorf("%s is not approved: run herdsman plugin --experimental-plugins install %s on a terminal", name, name))
		}
		cmd := exec.CommandContext(ctx, status.GetPath())
		cmd.Env = plugin.Host{}.Environment()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
		if err := cmd.Run(); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	return fail(stderr, fmt.Errorf("no plugin named %q", name))
}
