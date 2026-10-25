package catalog

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// probeTimeout bounds each command of the pipeline; tests shorten it.
var probeTimeout = 10 * time.Second

// modelProfile is a harness's model interface named model-profile (Muse).
var modelProfile = regexp.MustCompile(`(?m)^\s+model-profile\b`)

// Pipeline is the one probe `herdsman models` and herdsman web share
// (A29): the agent kinds `herdr agent start --help` lists (only one when
// only names it; claude and codex alone when herdr cannot be asked), kept
// when installed (claude and codex always), each asked for its own model
// list: Claude from its --help, Codex from `codex debug models`, any other
// from `<program> models` when its --help lists that subcommand, with the
// efforts its --help lists. A list that cannot be read leaves the kind
// offline with no models and the reason in Unavailable: no captured list
// and no guessed free-text model stand in.
func Pipeline(ctx context.Context, run Runner, only string) []Kind {
	kinds := []string{only}
	if only == "" {
		kinds = herdrKinds(ctx, run)
	}
	out := []Kind{}
	for _, k := range kinds {
		kd, installed := probeKind(ctx, run, k)
		if installed || only != "" || k == "claude" || k == "codex" {
			out = append(out, kd)
		}
	}
	return out
}

// herdrKinds are herdr's kinds with claude and codex first, or those two
// when herdr cannot be asked.
func herdrKinds(ctx context.Context, run Runner) []string {
	kinds := slicesClone(Probed)
	if listed, err := askHerdr(ctx, run); err == nil {
		for _, k := range listed {
			if k != "claude" && k != "codex" {
				kinds = append(kinds, k)
			}
		}
	}
	return kinds
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

func askHerdr(ctx context.Context, run Runner) ([]string, error) {
	out, err := ask(ctx, run, "herdr", "agent", "start", "--help")
	if err == nil {
		return ParseKinds(out)
	}
	return nil, err
}

// HerdrError says why herdr's kinds could not be read, or "".
func HerdrError(ctx context.Context, run Runner) string {
	if _, err := askHerdr(ctx, run); err != nil {
		return "herdr agent start --help: " + err.Error()
	}
	return ""
}

func ask(ctx context.Context, run Runner, name string, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return run(c, name, args...)
}

// unavailable marks k offline with no models and the reason.
func unavailable(k Kind, why string) Kind {
	k.Models, k.Offline, k.Unavailable, k.DefaultModel = []Model{}, true, why, ""
	return k
}

// probeKind asks one kind for its list; installed is false when its
// program is not on PATH.
func probeKind(ctx context.Context, run Runner, kind string) (Kind, bool) {
	base := Kind{Kind: kind, Label: label(kind), Models: []Model{}, SkillPrefix: "/"}
	switch kind {
	case "claude", "codex":
		base.Checkpointer = true
		if kind == "codex" {
			base.SkillPrefix = "$"
		}
		p := probes[kind]
		cmd := kind + " " + strings.Join(p.args, " ")
		out, err := ask(ctx, run, kind, p.args...)
		if errors.Is(err, exec.ErrNotFound) {
			return unavailable(base, "not found on PATH"), false
		}
		if err != nil {
			return unavailable(base, cmd+": "+err.Error()), true
		}
		k, err := p.parse(out)
		if err != nil {
			base.Version = version(ctx, run, kind)
			return unavailable(base, cmd+": "+err.Error()), true
		}
		k.Version = version(ctx, run, kind)
		return k, true
	}
	program := kind
	if p, ok := Others[kind]; ok {
		program = p
	}
	help, err := ask(ctx, run, program, "--help")
	if errors.Is(err, exec.ErrNotFound) {
		return unavailable(base, "not found on PATH"), false
	}
	if err != nil {
		return unavailable(base, program+" --help: "+err.Error()), true
	}
	h := ParseHelp(kind, program, help)
	base.Version = version(ctx, run, program)
	base.EffortFlag = effortFlag(help)
	// The model interface the --help advertises: a models subcommand, or
	// Muse's model-profile.
	sub := ""
	switch {
	case subcommand.Match(help):
		sub = "models"
	case modelProfile.Match(help):
		sub = "model-profile"
	default:
		return unavailable(base, program+" lists no models"), true
	}
	out, err := ask(ctx, run, program, sub)
	if err != nil {
		return unavailable(base, program+" "+sub+": "+err.Error()), true
	}
	ms, err := ParseModelLines(out, h.Efforts, h.DefaultEffort)
	if err != nil {
		return unavailable(base, program+" "+sub+": "+err.Error()), true
	}
	base.Models, base.DefaultModel = ms, ms[0].ID
	return base, true
}

// effortFlag is the option a harness's --help names for the effort, or "".
func effortFlag(help []byte) string {
	for _, l := range strings.Split(string(help), "\n") {
		if m := optionStart.FindStringSubmatch(l); m != nil && strings.Contains(m[1], "effort") {
			return m[1]
		}
	}
	return ""
}

// ParseModelLines reads a `<program> models` listing: one model id first on
// each line, optionally followed by a parenthesized note; headers and other
// lines are skipped. Each model takes the efforts the --help lists.
func ParseModelLines(b []byte, efforts []string, def string) ([]Model, error) {
	var ms []Model
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 0 || !ModelID.MatchString(f[0]) || strings.HasSuffix(strings.TrimSpace(l), ":") {
			continue
		}
		if rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), f[0])); rest != "" && !(strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")")) {
			continue
		}
		e := append([]string{}, efforts...)
		ms = append(ms, Model{ID: f[0], Label: f[0], Efforts: e, DefaultEffort: DefaultEffort(e, def)})
		if len(ms) == 500 {
			break
		}
	}
	if len(ms) == 0 {
		return nil, errors.New("lists no models")
	}
	return ms, nil
}

func label(k string) string {
	r := []rune(k) // a kind is never empty: herdr lists names, --harness checks them
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
