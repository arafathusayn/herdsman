package routedb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a private home with daemon/routes and an inbox whose
// contract 3 owner record names route's database there.
func fixture(t *testing.T, route string) (ph, dir, store string) {
	t.Helper()
	base, err := os.MkdirTemp("", "routedb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = filepath.EvalSymlinks(base)
	ph, dir = filepath.Join(base, "ph"), filepath.Join(base, "inbox")
	store = Path(ph, route)
	for _, d := range []string{filepath.Dir(store), filepath.Join(dir, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	owner := `{"owner":"daemon","contract":3,"store":"` + store + `"}`
	if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	return ph, dir, store
}

// TestClassifyDaemonRouteNames: a contract 3 record of any route name the
// daemon accepts names its store; a dotted, an upper-case, a leading
// digit and a reserved inbox name included.
func TestClassifyDaemonRouteNames(t *testing.T) {
	for _, route := range []string{"r", "v1.2", "Web", "1x", "state", "alerts"} {
		t.Run(route, func(t *testing.T) {
			ph, dir, store := fixture(t, route)
			for _, want := range []string{route, ""} {
				mode, p, err := Classify(dir, ph, want)
				if mode != Store || p != store || err != nil {
					t.Errorf("Classify(route %q) = %v %q %v, want Store %q", want, mode, p, err, store)
				}
			}
		})
	}
}

// TestClassifyRefusesBadRouteName: a store name the daemon refuses is
// refused, as is a store of another route.
func TestClassifyRefusesBadRouteName(t *testing.T) {
	for _, name := range []string{"a..b", ".r", "-r"} {
		t.Run(name, func(t *testing.T) {
			ph, dir, _ := fixture(t, "r")
			owner := `{"owner":"daemon","contract":3,"store":"` + filepath.Join(ph, "daemon", "routes", name+".db") + `"}`
			if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(owner), 0o600); err != nil {
				t.Fatal(err)
			}
			if mode, _, err := Classify(dir, ph, ""); mode != Refused || err == nil || !strings.Contains(err.Error(), "names store") {
				t.Errorf("store name %q: %v %v, want Refused", name, mode, err)
			}
		})
	}
	ph, dir, _ := fixture(t, "r")
	if mode, _, err := Classify(dir, ph, "other"); mode != Refused || !strings.Contains(err.Error(), Path(ph, "other")) {
		t.Errorf("store of another route: %v %v, want Refused naming %s", mode, err, Path(ph, "other"))
	}
}
