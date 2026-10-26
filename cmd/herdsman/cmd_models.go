package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
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
       herdsman models save --role <role> [--runner herdr|subagent] --harness <kind> --model <model> [--effort <effort> [--effort-flag <option>]] [--route <name>] [--json]

Asks each agent kind herdr can start (herdr agent start --help; claude and
codex alone when herdr cannot be asked) that is installed, or the one
--harness names, for its version and its models as the harness itself
reports them, in its order: Claude from its --help, Codex from codex debug
models, any other from "<program> models" when its --help lists that
subcommand, with the efforts its --help lists and the default effort it
reports ("none" when it reports none). A harness whose list cannot be read
prints "<kind> <version>: unavailable: <reason>" and offers no models; the
others still print. herdsman web's New route form uses the same probe.
--json prints {"harnesses": [{"kind","label","models","version",
"unavailable","offline","docs_url","effort_flag",...}], "herdr_error"}.

last prints the last pick of each role (implementer, integrator, reviewer,
adjutant, orchestrator), one "<role> runner=<herdr|subagent> harness=<kind>
model=<model> effort=<effort|none> route=<name|none> at=<time>" line each,
from <user config folder>/herdsman/choices.json; --json prints the file's
object {"picks": [{"role","runner","harness","model","effort","route","at"}]}.
save records one role's pick there (mode 600, written atomically; --json
prints the saved pick as one object); its
runner says whether the role's agents run in Herdr panes (the default) or
as background subagents of the orchestrator's harness. An effort of a
harness other than Claude and Codex is saved with the option that passes
it: --effort-flag, else the one the harness's --help names; with neither,
the pick is refused.

Exit codes:
  0  at least one harness listed its models; last: a pick printed; save: saved
  1  no harness listed its models; last: no pick saved, or the file is invalid;
     save: the pick is invalid or the file cannot be written
  2  usage error
`

// modelsRunner runs the harness programs; tests replace it.
var modelsRunner catalog.Runner = catalog.ExecRunner

// modelsReport is models' --json output.
//
// JSON fields: harnesses (each a catalog.Kind, the same entries the web
// catalog serves) and herdr_error (why herdr's kinds could not be read).
type modelsReport struct {
	Harnesses  []catalog.Kind `json:"harnesses"`
	HerdrError string         `json:"herdr_error,omitempty"`
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
	if err != nil || fs.NArg() > 0 || (*one != "" && !kindName.MatchString(*one)) {
		fmt.Fprint(stderr, modelsUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rep := modelsReport{Harnesses: catalog.Pipeline(ctx, modelsRunner, *one)}
	if *one == "" {
		rep.HerdrError = catalog.HerdrError(ctx, modelsRunner)
	}
	answered := false
	for _, k := range rep.Harnesses {
		answered = answered || k.Unavailable == ""
	}
	if *asJSON {
		b, _ := json.Marshal(rep)
		fmt.Fprintln(stdout, string(b))
	} else {
		if rep.HerdrError != "" {
			fmt.Fprintf(stdout, "herdr: %s (only claude and codex were asked)\n", rep.HerdrError)
		}
		for _, k := range rep.Harnesses {
			writeKind(stdout, k)
		}
	}
	if !answered {
		return 1
	}
	return 0
}

// kindName is a harness kind as herdr names it.
var kindName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// writeKind prints one harness: a header, then one line per model, or one
// "unavailable: <reason>" line.
func writeKind(w io.Writer, k catalog.Kind) {
	v := k.Version
	if v == "" {
		v = "unknown"
	}
	if k.Unavailable != "" {
		fmt.Fprintf(w, "%s %s: unavailable: %s\n", k.Kind, v, k.Unavailable)
		return
	}
	docs := k.DocsURL
	if docs == "" {
		docs = "its own list"
	}
	fmt.Fprintf(w, "%s %s (models: %s)\n", k.Kind, v, docs)
	for _, m := range k.Models {
		label := ""
		if m.Label != m.ID {
			label = " (" + m.Label + ")"
		}
		efforts := "none"
		if len(m.Efforts) > 0 {
			efforts = strings.Join(m.Efforts, ", ")
		}
		fmt.Fprintf(w, "  %s%s  efforts: %s; default: %s\n", m.ID, label, efforts, orNone(m.DefaultEffort))
	}
}

func modelsLast(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models last", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, modelsUsage)
			return 0
		}
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
	fs.StringVar(&p.EffortFlag, "effort-flag", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || p.Role == "" || p.Harness == "" || p.Model == "" {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, modelsUsage)
			return 0
		}
		fmt.Fprint(stderr, modelsUsage)
		return 2
	}
	if p.Effort != "" && p.EffortFlag == "" && p.Harness != "claude" && p.Harness != "codex" {
		flagName, err := catalog.EffortFlagOf(context.Background(), modelsRunner, p.Harness)
		if err == nil && flagName == "" {
			err = fmt.Errorf("%s names no effort option: pass --effort-flag", p.Harness)
		}
		if err != nil {
			fmt.Fprintf(stderr, "herdsman models save: %v\n", err)
			return 1
		}
		p.EffortFlag = flagName
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
	if *asJSON {
		b, _ := json.Marshal(p)
		fmt.Fprintln(stdout, string(b))
		return 0
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
