package hc

import (
	"fmt"
	"slices"
	"strings"
)

// Generation is the core protocol generation this client speaks. Core refuses
// a hello at any other generation with unsupported_generation, and the client
// refuses an answer that names another one.
const Generation uint32 = 1

// GenerationError is a core that speaks another protocol generation.
type GenerationError struct {
	Want uint32
	Got  string // what core said: its generation, or its refusal
}

func (e *GenerationError) Error() string {
	return fmt.Sprintf("hc: this Herdsman speaks core protocol generation %d; core %s: update Herdsman or herdr-core", e.Want, e.Got)
}

// MissingMethodError is a core that does not serve a method or feature
// Herdsman uses. Methods names each missing one.
type MissingMethodError struct {
	Version  string
	Methods  []string
	Features []string
}

func (e *MissingMethodError) Error() string {
	var parts []string
	if len(e.Methods) > 0 {
		parts = append(parts, "method "+strings.Join(e.Methods, ", "))
	}
	if len(e.Features) > 0 {
		parts = append(parts, "feature "+strings.Join(e.Features, ", "))
	}
	return fmt.Sprintf("hc: herdr-core %s lacks %s, which Herdsman uses", e.Version, strings.Join(parts, " and "))
}

// Missing compares the listed methods and features with what core advertised
// at hello and returns those core lacks, in list order.
func Missing(h HelloResult) (methods, features []string) {
	for _, m := range Methods {
		if !slices.Contains(h.Methods, m) {
			methods = append(methods, m)
		}
	}
	for _, f := range Features {
		if !slices.Contains(h.Features, f) {
			features = append(features, f)
		}
	}
	return methods, features
}

// checkHello refuses a hello answer at another generation, in framed mode, or
// without every listed method and feature.
func checkHello(h HelloResult) error {
	if h.Generation != Generation {
		return &GenerationError{Want: Generation, Got: fmt.Sprintf("%s speaks generation %d", h.Version, h.Generation)}
	}
	if h.Framed {
		return fmt.Errorf("hc: core switched to framed mode, which this client never asks for")
	}
	if methods, features := Missing(h); len(methods) > 0 || len(features) > 0 {
		return &MissingMethodError{Version: h.Version, Methods: methods, Features: features}
	}
	return nil
}
