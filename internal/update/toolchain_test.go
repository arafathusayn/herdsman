package update

import (
	"runtime"
	"testing"
)

func TestToolchainComponentNamesTheRunningGo(t *testing.T) {
	c := ToolchainComponent()
	if c.Name != "go-stdlib" || c.Version != runtime.Version() || c.License == "" {
		t.Fatalf("%+v", c)
	}
}
