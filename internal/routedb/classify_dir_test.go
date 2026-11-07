package routedb

import (
	"strings"
	"testing"
)

// TestClassifyRefusesNoInbox: no inbox ("" when the route is in no
// routes.json) is refused, never read as the working folder.
func TestClassifyRefusesNoInbox(t *testing.T) {
	ph, dir, _ := fixture(t, "r")
	t.Chdir(dir)
	if mode, _, err := Classify("", ph, "r"); mode != Refused || err == nil || !strings.Contains(err.Error(), "no inbox") {
		t.Errorf("Classify(\"\"): %v %v, want Refused (no inbox)", mode, err)
	}
}
