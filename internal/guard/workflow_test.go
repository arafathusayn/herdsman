package guard

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSDKTSJobInstallsBunTypes reads the job itself: `bun check` finds its
// types in a global install, which a developer's machine has and a clean
// runner does not.
func TestSDKTSJobInstallsBunTypes(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/tests.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, job, found := strings.Cut(string(data), "\n  sdk-ts:\n")
	if !found {
		t.Fatal("no sdk-ts job")
	}
	if next := regexp.MustCompile(`\n  [a-z-]+:\n`).FindStringIndex(job); next != nil {
		job = job[:next[0]]
	}
	install, check := strings.Index(job, "make tools-bun"), strings.Index(job, "make check-sdk-ts")
	if install < 0 || check < 0 || install > check {
		t.Fatalf("the job must run make tools-bun before make check-sdk-ts:\n%s", job)
	}
}
