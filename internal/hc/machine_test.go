package hc_test

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/hc"
)

// A remote record is known by its address alone: hc 0.1.20 sends no
// machine field.
func TestMachineOf(t *testing.T) {
	for addr, want := range map[string]string{"t1": "", "remote/t1": "remote", "vps/t12": "vps", "": ""} {
		if got := hc.MachineOf(addr); got != want {
			t.Errorf("MachineOf(%q) = %q, want %q", addr, got, want)
		}
		if local := (hc.Terminal{Address: addr}).Local(); local != (addr != "" && want == "") {
			t.Errorf("Local(%q) = %v", addr, local)
		}
	}
}
