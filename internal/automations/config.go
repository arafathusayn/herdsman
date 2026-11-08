// Package automations runs an action when the daemon sees an event. The action
// comes from configuration, which a repository can supply, so nothing runs
// until a person has approved that action's exact checksum.
package automations

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
)

// Version is carried by everything this experimental surface writes.
const Version = "0.1.0"

// Kind says what an action is.
type Kind string

// The kinds of action.
const (
	KindShell   Kind = "shell"
	KindBuiltin Kind = "builtin"
	KindPlugin  Kind = "plugin"
)

// The built-in actions.
const (
	BuiltinRoutePause = "route pause"
	BuiltinTaskStart  = "task start"
)

var builtins = []string{BuiltinRoutePause, BuiltinTaskStart}

// A filter would receive the shell reference that stands in for a value, not
// the value, and quietly produce nonsense.
var templateFilter = regexp.MustCompile(`\{\{[^}]*\|`)

// Rule is one event kind and its action. Timeout zero means the engine's.
type Rule struct {
	Event   string
	Kind    Kind
	Body    string
	Timeout time.Duration
}

// Config is the parsed [automations] section.
type Config struct {
	Enabled bool
	Rules   []Rule
}

// FromProto validates the section and returns its rules in event order. A
// missing section is off.
func FromProto(section *configv1.Automations) (Config, error) {
	config := Config{Enabled: section.GetExperimental()}
	on := section.GetOn()
	for _, event := range slices.Sorted(maps.Keys(on)) {
		if strings.TrimSpace(event) == "" {
			return Config{}, errors.New("automations: an event kind is empty")
		}
		rule, err := parseAction(event, on[event])
		if err != nil {
			return Config{}, fmt.Errorf("automations: %s: %w", event, err)
		}
		config.Rules = append(config.Rules, rule)
	}
	return config, nil
}

func parseAction(event string, action *configv1.AutomationAction) (Rule, error) {
	rule := Rule{Event: event}
	set := 0
	if action != nil && action.Run != nil {
		set++
		rule.Kind, rule.Body = KindShell, action.GetRun()
	}
	if action != nil && action.Builtin != nil {
		set++
		rule.Kind, rule.Body = KindBuiltin, action.GetBuiltin()
	}
	if action != nil && action.Plugin != nil {
		set++
		rule.Kind, rule.Body = KindPlugin, action.GetPlugin()
	}
	if set != 1 {
		return Rule{}, errors.New("set exactly one of run, builtin and plugin")
	}
	if err := checkBody(rule); err != nil {
		return Rule{}, err
	}
	if action.Timeout != nil {
		timeout, err := time.ParseDuration(action.GetTimeout())
		if err != nil {
			return Rule{}, fmt.Errorf("timeout: %w", err)
		}
		if timeout <= 0 {
			return Rule{}, errors.New("timeout must be positive")
		}
		rule.Timeout = timeout
	}
	return rule, nil
}

func checkBody(rule Rule) error {
	switch rule.Kind {
	case KindShell:
		if strings.TrimSpace(rule.Body) == "" {
			return errors.New("run is empty")
		}
		if templateFilter.MatchString(rule.Body) {
			return errors.New("run: template filters are not available here, because values reach the shell as variables")
		}
	case KindBuiltin:
		if !slices.Contains(builtins, rule.Body) {
			return fmt.Errorf("unknown builtin %q", rule.Body)
		}
	case KindPlugin:
		if len(strings.Fields(rule.Body)) != 2 {
			return errors.New(`plugin must be written "<plugin> <action>"`)
		}
	}
	return nil
}
