package checkpoint

import (
	"testing"
	"time"
)

// The checkpointer reads its settings with the shared parser
// (internal/settings), so it accepts exactly the forms every other command
// accepts: a sign, blanks around a number, blanks around a duration.
func TestSharedSettingForms(t *testing.T) {
	for _, tc := range []struct {
		env      map[string]string
		lockWait int
		interval time.Duration
	}{
		{map[string]string{"LOCK_WAIT": "+6"}, 6, 900 * time.Second},
		{map[string]string{"LOCK_WAIT": " 06 "}, 6, 900 * time.Second},
		{map[string]string{"INTERVAL": " 2m "}, 6, 2 * time.Minute},
	} {
		env := map[string]string{"TARGET": "w1:p1"}
		for k, v := range tc.env {
			env[k] = v
		}
		c, err := FromEnv(func(k string) string { return env[k] }, "/")
		if err != nil || c.LockWait != tc.lockWait || c.Inbox.LockWait != "6" || c.Interval != tc.interval {
			t.Errorf("%v: %+v %v", tc.env, c, err)
		}
	}
}
