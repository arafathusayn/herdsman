package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/update"
)

func TestUpdateStopsBeforeRunningWhenItCannotFindItself(t *testing.T) {
	saved := updateExecutable
	t.Cleanup(func() { updateExecutable = saved })
	updateExecutable = func() (string, error) { return "", errors.New("planted executable") }
	var out, errOut bytes.Buffer
	if code := runUpdate(nil, nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "planted executable") {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
}

func TestUpdateStopsWhenTheTrustedListCannotBeRead(t *testing.T) {
	saved := updateKeys
	t.Cleanup(func() { updateKeys = saved })
	updateKeys = func() ([]update.Key, error) { return nil, errors.New("planted keys") }
	var out, errOut bytes.Buffer
	if code := runUpdate(nil, nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "planted keys") {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
}
