package version

import (
	"regexp"
	"testing"
)

func TestVersionIsSemVer(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(Version) {
		t.Fatalf("Version %q is not SemVer", Version)
	}
}
