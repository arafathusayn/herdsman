package launch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

const (
	startSuffix = ".start"
	rcSuffix    = ".rc"
	// maxRCSize is the length of the largest exit status, 255.
	maxRCSize = 3
)

// MarkerState is what the marker files prove about a step.
type MarkerState string

const (
	// MarkerNotStarted means no .start file: the shell writes that file before
	// the command, so the command never ran.
	MarkerNotStarted MarkerState = "not-started"
	// MarkerUncertain means a .start file with no status: the effect may have
	// happened. An empty .rc is this state too, since the shell creates the
	// file before it writes the status.
	MarkerUncertain MarkerState = "uncertain"
	// MarkerFinished means the command ended and .rc holds its status.
	MarkerFinished MarkerState = "finished"
)

// Marker is the state of one step's markers.
type Marker struct {
	State MarkerState
	// RC is the exit status when State is MarkerFinished.
	RC int
}

// ReadMarker reads the markers of step n. Files that the wrapper cannot have
// produced are an error, never a guess.
func ReadMarker(stepsDir string, n int) (Marker, error) {
	if err := checkStepNumber(n); err != nil {
		return Marker{}, err
	}
	return readMarker(stepsDir, strconv.Itoa(n))
}

// ReadProbeMarker reads the markers of step n's probe.
func ReadProbeMarker(stepsDir string, n int) (Marker, error) {
	if err := checkStepNumber(n); err != nil {
		return Marker{}, err
	}
	return readMarker(stepsDir, probeID(n))
}

func readMarker(stepsDir, id string) (Marker, error) {
	startPath := filepath.Join(stepsDir, id+startSuffix)
	rcPath := filepath.Join(stepsDir, id+rcSuffix)
	startInfo, err := statMarker(startPath)
	if err != nil {
		return Marker{}, err
	}
	rcInfo, err := statMarker(rcPath)
	if err != nil {
		return Marker{}, err
	}
	switch {
	case rcInfo != nil && startInfo == nil:
		return Marker{}, fmt.Errorf("marker %s exists without its .start file", rcPath)
	case startInfo == nil:
		return Marker{State: MarkerNotStarted}, nil
	case rcInfo == nil || rcInfo.Size() == 0:
		return Marker{State: MarkerUncertain}, nil
	case rcInfo.Size() > maxRCSize:
		return Marker{}, fmt.Errorf("marker %s is too long for an exit status", rcPath)
	}
	data, err := os.ReadFile(rcPath)
	if err != nil {
		return Marker{}, fmt.Errorf("read marker: %w", err)
	}
	rc, err := parseRC(string(data))
	if err != nil {
		return Marker{}, fmt.Errorf("marker %s: %w", rcPath, err)
	}
	return Marker{State: MarkerFinished, RC: rc}, nil
}

// statMarker returns nil for a missing file. It never follows a link, and it
// refuses anything but a regular file, so opening a planted FIFO cannot block.
func statMarker(path string) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check marker: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("marker %s is not a regular file", path)
	}
	return info, nil
}

func parseRC(text string) (int, error) {
	rc, err := strconv.Atoi(text)
	if err != nil || rc < 0 || rc > 255 || strconv.Itoa(rc) != text {
		return 0, fmt.Errorf("%q is not an exit status", text)
	}
	return rc, nil
}

// ClearMarkers removes step n's markers and its probe's. Call it only on
// ActionRun, when the step is about to run again; a stale .rc would otherwise
// read as this run's status. On ActionProbe use ClearProbeMarkers, since this
// function removes the .start that is the only evidence the step may have run.
// Each .rc goes before its .start, so a crash here leaves the uncertain state
// and never a status without a start.
func ClearMarkers(stepsDir string, n int) error {
	if err := checkStepNumber(n); err != nil {
		return err
	}
	return clearMarkers(stepsDir, strconv.Itoa(n), probeID(n))
}

// ClearProbeMarkers removes only the old result of step n's probe, before the
// probe runs again. The step's own markers stay, so a crash after this call
// still reads as uncertain and can never run a never or once step twice.
func ClearProbeMarkers(stepsDir string, n int) error {
	if err := checkStepNumber(n); err != nil {
		return err
	}
	return clearMarkers(stepsDir, probeID(n))
}

func clearMarkers(stepsDir string, ids ...string) error {
	for _, id := range ids {
		for _, suffix := range []string{rcSuffix, startSuffix} {
			err := os.Remove(filepath.Join(stepsDir, id+suffix))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("clear marker: %w", err)
			}
		}
	}
	return nil
}
