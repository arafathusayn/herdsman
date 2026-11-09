package project

import (
	"regexp"
	"strings"
)

const (
	maxGeneratedSlug = 48
	maxSlug          = 64
	emptySlug        = "task"
)

var slugShape = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidSlug reports whether s can name a task, and so a branch and a folder.
func ValidSlug(s string) bool {
	return len(s) <= maxSlug && slugShape.MatchString(s)
}

// Slugify turns a title into a slug: lower-case ASCII words joined by hyphens.
func Slugify(title string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if gap && b.Len() > 0 {
				b.WriteByte('-')
			}
			gap = false
			b.WriteRune(r)
			continue
		}
		gap = true
	}
	s := b.String()
	if len(s) > maxGeneratedSlug {
		s = strings.TrimRight(s[:maxGeneratedSlug], "-")
	}
	if s == "" {
		return emptySlug
	}
	return s
}
