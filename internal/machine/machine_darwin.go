package machine

import (
	"encoding/binary"
	"errors"
	"syscall"
)

// ReadLoad reads the kernel's vm.loadavg: three fixed-point averages and
// their scale (struct loadavg).
func ReadLoad() (Load, error) {
	s, err := syscall.Sysctl("vm.loadavg")
	if err != nil {
		return Load{}, err
	}
	// syscall.Sysctl drops one trailing NUL byte, which here is the last
	// byte of the scale: put it back.
	b := []byte(s)
	for len(b) < 24 {
		b = append(b, 0)
	}
	return parseLoadavg(b)
}

func parseLoadavg(b []byte) (Load, error) {
	// struct loadavg { fixpt_t ldavg[3]; long fscale; }: three uint32, padding, an int64.
	if len(b) < 24 {
		return Load{}, errors.New("short vm.loadavg")
	}
	scale := float64(binary.LittleEndian.Uint64(b[16:24]))
	if scale == 0 {
		return Load{}, errors.New("vm.loadavg has no scale")
	}
	var l Load
	for i := range 3 {
		l[i] = float64(binary.LittleEndian.Uint32(b[i*4:])) / scale
	}
	return l, nil
}

// FreePercent is the kernel's kern.memorystatus_level, the free memory
// percentage that `memory_pressure` reports.
func FreePercent() (int, error) {
	v, err := syscall.SysctlUint32("kern.memorystatus_level")
	return int(v), err
}
