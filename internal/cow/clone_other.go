//go:build !darwin && !linux

package cow

// platformClone always falls back: no clone call is wired up on this platform.
func platformClone(string, string) error {
	return errNoClone
}
