package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Small units the command-level cases of inbox_test.go do not reach.
func TestUnits(t *testing.T) {
	for c, want := range map[string]bool{"12-34": true, "-5": false, "a-5": false, "5": false, "5-": false} {
		if checksumShape(c) != want {
			t.Errorf("checksumShape(%q) = %v", c, !want)
		}
	}
	if KeysExactly([]byte(`{"a":1,"c":2}`), "a", "b") {
		t.Error("KeysExactly: a missing key with the same count")
	}
	if IdentityOK([]byte(`{"pane":"p1","session":{"agent":"claude","kind":"id","source":"hook"}}`)) {
		t.Error("IdentityOK: a session without a value")
	}
	if (&Fatal{Msg: "m"}).Error() != "m" {
		t.Error("Fatal text")
	}
	if _, ok := IsFatal(errors.New("x")); ok {
		t.Error("IsFatal of a plain error")
	}
	p := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(p, []byte("abc"), 0o600)
	if b, err := ReadBounded(p, 3); err != nil || string(b) != "abc" {
		t.Errorf("ReadBounded: %q %v", b, err)
	}
	if _, err := ReadBounded(p, 2); err == nil {
		t.Error("ReadBounded: over the bound")
	}
	if _, err := ReadBounded(p+".none", 2); err == nil {
		t.Error("ReadBounded: a missing file")
	}
}
