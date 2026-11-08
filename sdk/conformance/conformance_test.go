// Package conformance will run the contract scenarios against any
// implementation. Until the first contract exists it proves the module resolves
// the SDK from inside the workspace.
package conformance

import (
	"testing"

	"github.com/arafathusayn/herdsman/sdk/version"
)

func TestSDKResolves(t *testing.T) {
	if version.Version == "" {
		t.Fatal("sdk/version.Version is empty")
	}
}
