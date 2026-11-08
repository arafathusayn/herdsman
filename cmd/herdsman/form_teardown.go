package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/tui/forms"
)

// planIDPrefix marks the line that a plan preview adds after the plan.
const planIDPrefix = "plan-id "

// teardownPlanID names what a plan would do. It covers each task and every
// field of its lines that the run acts on, in a fixed order, and leaves out a
// process's momentary state, which can differ between two reads of one plan.
func teardownPlanID(plans []json.RawMessage) string {
	var rows []string
	for _, raw := range plans {
		var plan teardownPlan
		if json.Unmarshal(raw, &plan) != nil {
			rows = append(rows, string(raw))
			continue
		}
		for _, line := range plan.Lines {
			encoded, _ := json.Marshal(line)
			rows = append(rows, plan.Task+" "+string(encoded))
		}
		rows = append(rows, plan.Task)
	}
	slices.Sort(rows)
	sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
	return hex.EncodeToString(sum[:])
}

// splitPlanID separates a preview's plan text from the ID on its last line.
func splitPlanID(output string) (text, id string) {
	text = strings.TrimRight(output, "\n")
	cut := strings.LastIndexByte(text, '\n') + 1
	if rest, ok := strings.CutPrefix(text[cut:], planIDPrefix); ok {
		return strings.TrimRight(text[:cut], "\n"), rest
	}
	return text, ""
}

// consentCommon shows a verb's plan and remembers its ID, so that confirming
// the screen approves that plan. A preview with no ID leaves the verb to ask.
func consentCommon() forms.Common {
	common := formCommon()
	preview := common.Plan
	id := ""
	common.Plan = func(argv []string) string {
		var text string
		text, id = splitPlanID(preview(append(slices.Clone(argv), "--show-plan-id")))
		return text
	}
	common.Consent = func() []string {
		if id == "" {
			return nil
		}
		return []string{"--approve-plan", id}
	}
	return common
}

func openTeardownForm(args *[]string, stdin io.Reader, stdout, stderr io.Writer) (int, bool) {
	return presentTargetForm(args, stdin, stdout, stderr, func(targets []string) forms.Spec {
		return forms.Teardown(forms.TargetDefaults{Common: consentCommon(), Targets: targets})
	})
}
