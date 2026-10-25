package machine

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

var (
	loadavgPath = "/proc/loadavg"
	meminfoPath = "/proc/meminfo"
)

// ReadLoad reads /proc/loadavg.
func ReadLoad() (Load, error) {
	b, err := os.ReadFile(loadavgPath)
	if err != nil {
		return Load{}, err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return Load{}, errors.New("short /proc/loadavg")
	}
	var l Load
	for i := range 3 {
		if l[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return Load{}, err
		}
	}
	return l, nil
}

// FreePercent is MemAvailable over MemTotal, in whole percent.
func FreePercent() (int, error) {
	f, err := os.Open(meminfoPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var total, avail int64
	s := bufio.NewScanner(f)
	for s.Scan() {
		k, v, ok := strings.Cut(s.Text(), ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.Fields(v + " 0")[0], 10, 64)
		switch k {
		case "MemTotal":
			total = n
		case "MemAvailable":
			avail = n
		}
	}
	if total == 0 {
		return 0, errors.New("no MemTotal in /proc/meminfo")
	}
	return int(avail * 100 / total), nil
}
