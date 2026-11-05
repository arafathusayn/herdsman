package daemon

import (
	"strings"
	"testing"
)

// TestAdoptKeepsDeliveryHold (S35): route-paused gives
// meta.delivery_hold=paused, route-frozen gives frozen, and with both
// frozen wins with one disposition naming both; no file gives no hold.
func TestAdoptKeepsDeliveryHold(t *testing.T) {
	for _, c := range []struct {
		files []string
		want  string
	}{
		{nil, ""},
		{[]string{"route-paused"}, "paused"},
		{[]string{"route-frozen"}, "frozen"},
		{[]string{"route-paused", "route-frozen"}, "frozen"},
	} {
		x := newImportInbox(t)
		for _, f := range c.files {
			x.write(t, "state/"+f, []byte("by the human\n"))
		}
		db, e := x.run(t)
		if e != nil {
			t.Fatalf("%v: %v", c.files, e)
		}
		if got := strings.Join(rowsOf(t, db, `SELECT v FROM meta WHERE k = 'delivery_hold'`), ","); got != c.want {
			t.Fatalf("%v: delivery_hold %q, want %q", c.files, got, c.want)
		}
		for _, f := range c.files {
			d := rowsOf(t, db, `SELECT disposition FROM import_log WHERE path = ?`, "state/"+f)
			if len(d) != 1 || !strings.HasPrefix(d[0], "meta delivery_hold "+c.want) || (len(c.files) == 2 && !strings.Contains(d[0], "both present")) {
				t.Fatalf("%v: %s logged %v", c.files, f, d)
			}
		}
	}
}
