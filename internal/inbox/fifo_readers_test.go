package inbox

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Review inbox-fifo-1 P1: readers after a check (the setup proof's
// executable digest, the ack's message digest and claim, the boundary's
// state reads, CatText) opened the path with a blocking open, so a leaf
// replaced by a fifo after the check hung them until a writer came. Each
// reader gets a fifo with no writer and must refuse it. The readers run
// at once; one still open at the test's deadline is reported and left
// blocked, as a writer would only move it on to a blocking read.
func TestReadersRefuseAFIFO(t *testing.T) {
	dir := t.TempDir()
	readers := map[string]func(p string) bool{
		"DigestFileFast": func(p string) bool {
			_, err := DigestFileFast(p)
			return err != nil
		},
		"DigestFileCached": func(p string) bool {
			_, err := DigestFileCached(p)
			return err != nil
		},
		"ReadFile": func(p string) bool {
			_, err := ReadFile(p)
			return err != nil
		},
		"DigestFile": func(p string) bool {
			_, err := DigestFile(p)
			return err != nil
		},
		"CatText":   func(p string) bool { return CatText(p) == "" },
		"readState": func(p string) bool { return readState(p) == nil },
	}
	type result struct {
		name    string
		refused bool
	}
	results := make(chan result, len(readers))
	for name, read := range readers {
		fifo := filepath.Join(dir, name)
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		go func() { results <- result{name, read(fifo)} }()
	}
	var hang <-chan time.Time
	if d, ok := t.Deadline(); ok {
		hang = time.After(time.Until(d) * 9 / 10)
	}
	open := map[string]bool{}
	for name := range readers {
		open[name] = true
	}
	for len(open) > 0 {
		select {
		case r := <-results:
			delete(open, r.name)
			if !r.refused {
				t.Errorf("%s read a fifo", r.name)
			}
		case <-hang:
			for name := range open {
				t.Errorf("%s blocked on a fifo", name)
			}
			return
		}
	}
}
