package settings

import (
	"strings"
	"testing"
	"time"
)

func TestInt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"", 30, true}, {"5", 5, true}, {" 08 ", 8, true}, {"-3", -3, true}, {"+4", 4, true}, {"007", 7, true},
		{"x", 0, false}, {"1/0", 0, false}, {"1.5", 0, false}, {"0x10", 0, false}, {"99999999999999999999", 0, false}, {"200", 0, false},
	} {
		got, err := Int("LIMIT_MIN", tc.in, 30, -100, 100)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("%q: %d %v", tc.in, got, err)
		}
	}
	_, err := Int("LIMIT_MIN", "x", 30, 0, 100)
	if err == nil || err.Error() != `LIMIT_MIN: not a whole number (got "x")` {
		t.Errorf("%v", err)
	}
}

func TestDuration(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 15 * time.Second, true}, {"0", 0, true}, {"60", time.Minute, true}, {" 090 ", 90 * time.Second, true}, {"30s", 30 * time.Second, true},
		{"10m", 10 * time.Minute, true}, {"2h", 2 * time.Hour, true}, {"1d", day, true}, {"3d", day, true},
		{"9223372036854775807", day, true}, {"99999999999999999999999", day, true}, {"-1", 0, false}, {"-5m", 0, false},
		{"x", 0, false}, {"1/0", 0, false}, {"5 m", 0, false}, {"m", 0, false}, {"1.5", 0, false}, {"5w", 0, false},
	} {
		got, err := Duration("HERDSMAN_GATE_POLL", tc.in, 15*time.Second, day)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("%q: %v %v", tc.in, got, err)
		}
	}
	if _, err := Duration("POLL", "-1", time.Minute, day); err == nil || !strings.HasPrefix(err.Error(), "POLL: ") || !strings.HasSuffix(err.Error(), `(got "-1")`) {
		t.Errorf("%v", err)
	}
}

func TestDecimal(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{{"", true}, {"8", true}, {"2.5", true}, {" 4 ", true}, {"x", false}, {"-1", false}, {"0", false}, {"NaN", false}, {"Inf", false}, {"1e400", false}} {
		if _, err := Decimal("HERDSMAN_LOAD_LIMIT", tc.in); (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.in, err)
		}
	}
}

func TestSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{{"", 7, true}, {"10", 10, true}, {"2k", 2048, true}, {"3m", 3 << 20, true}, {"-1", 0, false}, {"1g", 0, false}, {"99999999999999m", 0, false}} {
		got, err := Size("SIZE", tc.in, 7, 1<<40)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("%q: %d %v", tc.in, got, err)
		}
	}
}

func TestReaderKeepsTheFirstError(t *testing.T) {
	env := map[string]string{"A": "x", "B": "y"}
	r := Reader{Getenv: func(k string) string { return env[k] }}
	r.Int("A", 1, 0, 9)
	r.Duration("B", time.Second, time.Hour)
	if r.Err == nil || !strings.HasPrefix(r.Err.Error(), "A: ") {
		t.Errorf("%v", r.Err)
	}
}

func FuzzParsers(f *testing.F) {
	for _, s := range []string{"", "0", "08", "-1", "5m", "1d", "2.5", "3k", "x", "99999999999999999999", " 1 "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if d, err := Duration("D", s, time.Second, 24*time.Hour); err == nil && (d < 0 || d > 24*time.Hour) {
			t.Errorf("%q: %v out of range", s, d)
		}
		if n, err := Int("I", s, 0, -5, 5); err == nil && (n < -5 || n > 5) {
			t.Errorf("%q: %d out of range", s, n)
		}
		if n, err := Size("S", s, 0, 1<<30); err == nil && (n < 0 || n > 1<<30) {
			t.Errorf("%q: %d out of range", s, n)
		}
		_, _ = Decimal("L", s)
	})
}
