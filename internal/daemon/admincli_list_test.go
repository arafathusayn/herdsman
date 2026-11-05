package daemon

import "testing"

// TestRouteListStrictArgs: route list takes no argument and no flag; an
// extra one is a usage error (exit 2) before any request.
func TestRouteListStrictArgs(t *testing.T) {
	h, _, sup := newSup(t, "darwin")
	for _, extra := range [][]string{{"--bogus"}, {"r"}, {"r", "--json"}} {
		args := append([]string{"route", "list"}, extra...)
		if out, errOut, code := adminRun(h, sup, nil, nil, false, args...); code != 2 || out != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want a usage error", args, code, out, errOut)
		}
	}
	if _, errOut, code := adminRun(h, sup, nil, nil, false, "route", "list"); code != 0 {
		t.Errorf("route list: exit %d, stderr %q", code, errOut)
	}
}
