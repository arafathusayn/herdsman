package hooks

import (
	"context"
	"os"
	"testing"
)

func TestAnEmptyPlanStartsNothingAndLeavesNoLogs(t *testing.T) {
	r, _ := runner(t)
	result, err := r.Run(context.Background(), "pre-switch", nil)
	if err != nil || !result.OK || result.File != "" {
		t.Fatalf("%+v %v", result, err)
	}
	logs, err := r.Start("post-switch", nil)
	if err != nil || logs != nil {
		t.Fatalf("%v %v", logs, err)
	}
	if _, err := os.Stat(r.LogDir); err == nil {
		t.Fatal("wrote a log folder for nothing")
	}
}
