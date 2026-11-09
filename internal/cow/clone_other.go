//go:build !darwin && !linux

package cow

import "time"

// platformClone always falls back: no clone call is wired up on this platform.
func platformClone(string, string) error {
	return errNoClone
}

// setLinkTime does nothing: no such call is wired up on this platform.
func setLinkTime(string, time.Time) error {
	return nil
}
