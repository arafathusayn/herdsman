package machine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcFiles(t *testing.T) {
	dir := t.TempDir()
	oldL, oldM := loadavgPath, meminfoPath
	defer func() { loadavgPath, meminfoPath = oldL, oldM }()
	loadavgPath, meminfoPath = filepath.Join(dir, "loadavg"), filepath.Join(dir, "meminfo")
	_ = os.WriteFile(loadavgPath, []byte("1.50 2.25 3.00 1/100 42\n"), 0o644)
	_ = os.WriteFile(meminfoPath, []byte("MemTotal:  1000 kB\nMemFree: 1 kB\nMemAvailable:  250 kB\nbad line\n"), 0o644)
	if l, err := ReadLoad(); err != nil || l != (Load{1.5, 2.25, 3}) {
		t.Fatalf("load %v %v", l, err)
	}
	if p, err := FreePercent(); err != nil || p != 25 {
		t.Fatalf("free %v %v", p, err)
	}
	if Reading() != "1.50 25" {
		t.Fatalf("reading %q", Reading())
	}
	_ = os.WriteFile(loadavgPath, []byte("1.5\n"), 0o644)
	if _, err := ReadLoad(); err == nil {
		t.Fatal("short")
	}
	_ = os.WriteFile(loadavgPath, []byte("x y z\n"), 0o644)
	if _, err := ReadLoad(); err == nil || Reading() != "" {
		t.Fatal("bad number")
	}
	_ = os.WriteFile(meminfoPath, []byte("MemFree: 1 kB\n"), 0o644)
	if _, err := FreePercent(); err == nil {
		t.Fatal("no total")
	}
	_ = os.Remove(loadavgPath)
	_ = os.Remove(meminfoPath)
	if _, err := ReadLoad(); err == nil {
		t.Fatal("missing loadavg")
	}
	if _, err := FreePercent(); err == nil {
		t.Fatal("missing meminfo")
	}
}
