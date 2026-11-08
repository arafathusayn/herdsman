package policy

import (
	"fmt"
	"path"
	"strings"
)

// IsProtected reports whether a branch matches a protected pattern. A star does
// not cross a slash, which is how branch rules on code hosts read too.
func (p Policy) IsProtected(branch string) bool {
	name := strings.TrimPrefix(branch, "refs/heads/")
	if name == "" {
		return false
	}
	for _, pattern := range p.Protected {
		// Parse rejected malformed patterns, so a match error cannot occur here.
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
	}
	return false
}

// CheckRefMove refuses a verb that would move or delete a protected branch.
func (p Policy) CheckRefMove(verb, branch string) error {
	if !p.IsProtected(branch) {
		return nil
	}
	return &Refusal{
		Reason:  ReasonProtected,
		Message: fmt.Sprintf("%s refused: branch %s is protected by [policy]", verb, branch),
	}
}
