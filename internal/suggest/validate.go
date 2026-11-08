package suggest

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

var (
	idPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	tagPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	semverSimple = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	scpPattern   = regexp.MustCompile(`^git@[A-Za-z0-9][A-Za-z0-9.-]*:[A-Za-z0-9._/~-]+$`)
)

var kinds = []Kind{KindTip, KindMCP, KindSkill, KindPlugin, KindMod, KindRepo}

// Validate reports every rule an entry breaks. An id is also a file name, so
// its pattern is what keeps `<id>.md` inside the catalog folder.
func Validate(e Entry) error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !idPattern.MatchString(e.ID) {
		bad("id %q must be lowercase letters, digits and dashes", e.ID)
	}
	if !slices.Contains(kinds, e.Kind) {
		bad("kind %q is not one of tip, mcp, skill, plugin, mod, repo", e.Kind)
	}
	if strings.TrimSpace(e.Title) == "" {
		bad("title is required")
	}
	if len(e.Tags) == 0 {
		bad("tags needs at least one tag")
	}
	for _, t := range e.Tags {
		if !tagPattern.MatchString(t) {
			bad("tag %q must be lowercase letters, digits, dots, dashes and underscores", t)
		}
	}
	if !semverSimple.MatchString(e.Since) {
		bad("since %q must be a version like 1.2.3", e.Since)
	}
	if strings.TrimSpace(e.Body) == "" {
		bad("body is required")
	}
	if e.URL != "" && !validWebURL(e.URL) {
		bad("url %q must be an http or https address", e.URL)
	}
	if e.Repo != "" && !validRepoURL(e.Repo, false) {
		bad("repo %q must be an https, ssh or git@ address", e.Repo)
	}
	for _, list := range [][]string{e.AppliesTo.Harnesses, e.AppliesTo.Ecosystems, e.AppliesTo.Platforms} {
		for _, v := range list {
			if !tagPattern.MatchString(v) {
				bad("applies_to value %q must be lowercase", v)
			}
		}
	}
	if e.Install != nil {
		errs = append(errs, validateInstall(*e.Install)...)
	}
	return errors.Join(errs...)
}

func validateInstall(in Install) []error {
	var errs []error
	recipes := 0
	for _, r := range []string{in.Command, in.MCP, in.SkillRepo} {
		if strings.TrimSpace(r) != "" {
			recipes++
		}
	}
	if recipes != 1 {
		errs = append(errs, errors.New("install needs exactly one of command, mcp, skill_repo"))
	}
	if in.SkillRepo != "" && !validRepoURL(in.SkillRepo, false) {
		errs = append(errs, fmt.Errorf("install skill_repo %q must be an https, ssh or git@ address", in.SkillRepo))
	}
	if len(in.Touches) == 0 {
		errs = append(errs, errors.New("install touches must list the files the recipe changes"))
	}
	return errs
}

func validWebURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// validRepoURL accepts the forms git may fetch from. Refusing everything else
// keeps out option-like arguments and transports such as ext::, which run
// commands. file:// is for local catalogs and is allowed only when asked for.
func validRepoURL(s string, allowFile bool) bool {
	if strings.ContainsAny(s, " \t\n") {
		return false
	}
	if scpPattern.MatchString(s) {
		return true
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https", "ssh":
		return u.Host != ""
	case "file":
		return allowFile && u.Path != ""
	}
	return false
}
