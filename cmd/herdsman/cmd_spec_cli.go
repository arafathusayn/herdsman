package main

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strings"

	cliv1 "github.com/arafathusayn/herdsman/sdk/cli/v1"
	sdkversion "github.com/arafathusayn/herdsman/sdk/version"
	"google.golang.org/protobuf/encoding/protojson"
)

// The commands declare their interface only as help text, so that text is the
// source this spec is read from: what a person is told is what a tool is told.
var (
	helpFlag       = regexp.MustCompile(`(^|[\s\[(|])(--[a-z0-9][a-z0-9-]*)(?:[ =]<([^>]+)>)?`)
	helpPositional = regexp.MustCompile(`<([^>]+)>`)
	helpExitRow    = regexp.MustCompile(`^  (\d+|N)\s+(.*)$`)
)

// writeSpecCLI prints every command as indented JSON. Indenting also removes
// the whitespace protojson varies between builds, so the bytes are stable.
func writeSpecCLI(w io.Writer) error {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	document := &cliv1.CliSpec{Contract: sdkversion.Version}
	for _, name := range names {
		document.Commands = append(document.Commands, commandSpec(commands[name]))
	}
	data, err := protojson.Marshal(document)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err = w.Write(out.Bytes())
	return err
}

func commandSpec(c command) *cliv1.CommandSpec {
	spec := specFromHelp(c.name, c.help)
	spec.Summary = c.summary
	names := make([]string, 0, len(c.subHelp))
	for name := range c.subHelp {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec.Subcommands = append(spec.Subcommands, specFromHelp(name, c.subHelp[name]))
	}
	return spec
}

func specFromHelp(name, help string) *cliv1.CommandSpec {
	spec := &cliv1.CommandSpec{Name: name, Help: help}
	if help == "" {
		return spec
	}
	spec.Usage = usageLines(help)
	spec.Flags = flagsIn(help)
	spec.ExitCodes = exitCodesIn(help)
	seen := map[string]bool{}
	for _, usage := range spec.Usage {
		for _, match := range helpPositional.FindAllStringSubmatch(helpFlag.ReplaceAllString(usage, "$1"), -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				spec.Positionals = append(spec.Positionals, &cliv1.PositionalSpec{Name: match[1]})
			}
		}
	}
	return spec
}

// usageLines are the "usage:" line and the forms listed directly under it.
func usageLines(help string) []string {
	var usage []string
	inUsage := false
	for _, line := range strings.Split(help, "\n") {
		rest, found := strings.CutPrefix(line, "usage: ")
		trimmed := strings.TrimSpace(line)
		switch {
		case found:
			usage = append(usage, strings.TrimSpace(rest))
			inUsage = true
		case inUsage && strings.HasPrefix(trimmed, "herdsman "):
			usage = append(usage, trimmed)
		default:
			inUsage = false
		}
	}
	return usage
}

func flagsIn(help string) []*cliv1.FlagSpec {
	values := map[string]string{}
	for _, match := range helpFlag.FindAllStringSubmatch(help, -1) {
		if match[3] != "" || values[match[2]] == "" {
			values[match[2]] = match[3]
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	flags := make([]*cliv1.FlagSpec, 0, len(names))
	for _, name := range names {
		flags = append(flags, &cliv1.FlagSpec{Name: name, ValueName: values[name]})
	}
	return flags
}

// exitCodesIn reads the table under "exit codes:"; a more deeply indented line
// continues the row above it, and a blank line ends the table.
func exitCodesIn(help string) []*cliv1.ExitCodeSpec {
	var rows []*cliv1.ExitCodeSpec
	inTable := false
	for _, line := range strings.Split(help, "\n") {
		switch {
		case line == "exit codes:":
			inTable = true
		case !inTable:
		case strings.TrimSpace(line) == "":
			inTable = false
		default:
			if match := helpExitRow.FindStringSubmatch(line); match != nil {
				rows = append(rows, &cliv1.ExitCodeSpec{Code: match[1], Meaning: match[2]})
			} else if len(rows) > 0 {
				last := rows[len(rows)-1]
				last.Meaning += " " + strings.TrimSpace(line)
			}
		}
	}
	return rows
}
