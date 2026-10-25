package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/catalog"
	"github.com/arafathusayn/herdsman/internal/choices"
)

func init() {
	register(command{name: "models", summary: "Probe each installed harness: its version, models, efforts and default effort as it reports them.", run: runModels})
}

const modelsUsage = `usage: herdsman models [--harness <kind>] [--json]
       herdsman models last [--json]
       herdsman models save --role <role> [--runner herdr|subagent] --harness <kind> --model <model> [--effort <effort>] [--route <name>]

Asks each harness (claude, codex), or the one --harness names, for its
version and its models as the harness itself reports them, in its order
(the newest first where it gives one), with each model's efforts and the
default effort the harness reports ("none" when it reports none), and the
harness's model page. A harness that is missing or fails gets one line
saying so; the others still print. --json prints one object
{"harnesses": [{"kind","version","docs_url","models","error"}]}.

last prints the last pick of each role (implementer, integrator, reviewer,
adjutant, orchestrator), one "<role> runner=<herdr|subagent> harness=<kind>
model=<model> effort=<effort|none> route=<name|none> at=<time>" line each,
from <user config folder>/herdsman/choices.json; --json prints the file's
object {"picks": [{"role","runner","harness","model","effort","route","at"}]}.
save records one role's pick there (mode 600, written atomically); its
runner says whether the role's agents run in Herdr panes (the default) or
as background subagents of the orchestrator's harness.

Exit codes:
  0  at least one harness answered; last: a pick printed; save: saved
  1  no harness answered; last: no pick saved, or the file is invalid;
     save: the pick is invalid or the file cannot be written
  2  usage error
`

// modelsRunner runs the harness programs; tests replace it.
var modelsRunner catalog.Runner = catalog.ExecRunner

// modelsReport is models' --json output.
//
// JSON fields: harnesses (each a catalog.Harness).
type modelsReport struct {
	Harnesses []catalog.Harness `json:"harnesses"`
}

// choicesStore is the store of picks; tests point HOME at a temp folder.
var choicesStore = choices.Default

func runModels(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "last" {
		return modelsLast(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "save" {
		return modelsSave(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	one := fs.String("harness", "", "")
	asJSON := fs.Bool("json", false, "")
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, modelsUsage)
		return 0
	}
	kinds := catalog.Probed
	if *one != "" {
		kinds = []string{*one}
	}
	if err != nil || fs.NArg() > 0 || (*one != "" && !slices.Contains(catalog.Probed, *one)) {
		fmt.Fprint(stderr, modelsUsage)
		return 2
	}
	var rep modelsReport
	answered := false
	for _, k := range kinds {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		h := catalog.Probe(ctx, modelsRunner, k)
		cancel()
		answered = answered || h.Error == ""
		rep.Harnesses = append(rep.Harnesses, h)
	}
	if *asJSON {
		b, _ := json.Marshal(rep)
		fmt.Fprintln(stdout, string(b))
	} else {
		for _, h := range rep.Harnesses {
			writeHarness(stdout, h)
		}
	}
	if !answered {
		return 1
	}
	return 0
}

func writeHarness(w io.Writer, h catalog.Harness) {
	if h.Error != "" {
		fmt.Fprintf(w, "%s: %s\n", h.Kind, h.Error)
		return
	}
	fmt.Fprintf(w, "%s %s (models: %s)\n", h.Kind, h.Version, h.DocsURL)
	for _, m := range h.Models {
		def := m.DefaultEffort
		if def == "" {
			def = "none"
		}
		label := ""
		if m.Label != m.ID {
			label = " (" + m.Label + ")"
		}
		fmt.Fprintf(w, "  %s%s  efforts: %s; default: %s\n", m.ID, label, strings.Join(m.Efforts, ", "), def)
	}
}

func modelsLast(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models last", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, modelsUsage)
		return 2
	}
	st, err := choicesStore()
	var f choices.File
	if err == nil {
		f, err = st.Load()
	}
	if err != nil {
		fmt.Fprintf(stderr, "herdsman models last: %v\n", err)
		return 1
	}
	if *asJSON {
		b, _ := json.Marshal(f)
		fmt.Fprintln(stdout, string(b))
	} else {
		for _, p := range f.Picks {
			fmt.Fprintf(stdout, "%s runner=%s harness=%s model=%s effort=%s route=%s at=%s\n", p.Role, p.Runner, p.Harness, orNone(p.Model), orNone(p.Effort), orNone(p.Route), p.At.Format(time.RFC3339))
		}
	}
	if len(f.Picks) == 0 {
		if !*asJSON {
			fmt.Fprintln(stdout, "no previous choice")
		}
		return 1
	}
	return 0
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func modelsSave(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models save", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var p choices.Pick
	fs.StringVar(&p.Role, "role", "", "")
	fs.StringVar(&p.Runner, "runner", "herdr", "")
	fs.StringVar(&p.Harness, "harness", "", "")
	fs.StringVar(&p.Model, "model", "", "")
	fs.StringVar(&p.Effort, "effort", "", "")
	fs.StringVar(&p.Route, "route", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || p.Role == "" || p.Harness == "" || p.Model == "" {
		fmt.Fprint(stderr, modelsUsage)
		return 2
	}
	p.At = now().UTC().Truncate(time.Second)
	st, err := choicesStore()
	if err == nil {
		err = st.Save(p)
	}
	if err != nil {
		fmt.Fprintf(stderr, "herdsman models save: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "saved %s runner=%s harness=%s model=%s effort=%s\n", p.Role, p.Runner, p.Harness, p.Model, orNone(p.Effort))
	return 0
}

// webChoices is the store herdsman web saves a started route's picks to,
// or nil when the user has no config folder.
func webChoices() *choices.Store {
	st, err := choicesStore()
	if err != nil {
		return nil
	}
	return st
}
