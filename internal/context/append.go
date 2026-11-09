package context

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// tempFile is the part of a file that publishing an event uses.
type tempFile interface {
	Name() string
	Write([]byte) (int, error)
	Chmod(os.FileMode) error
	Sync() error
	Close() error
}

// The system calls Append depends on, replaced by the tests that prove what
// happens when one fails.
var (
	flock      = syscall.Flock
	createTemp = func(dir, pattern string) (tempFile, error) { return os.CreateTemp(dir, pattern) }
	linkFile   = os.Link
	openDir    = os.Open
)

// Input is what a caller says about a new event.
type Input struct {
	Kind    string
	Actor   string
	Task    string
	Repos   []string
	Parents []string // empty means every current head, which joins open branches
	Commits map[string]string
	Extra   map[string]string // written as given, one line each
	Body    string
	Now     time.Time // zero means the clock
}

// Append adds one event. Its id sorts after every event of the same actor,
// even one from a clock ahead of this one. Its time is the id's time, or the
// actor's latest recorded time when that is later, so an actor's ids and times
// cannot disagree.
func Append(folder string, in Input) (Event, error) {
	if err := validate(in); err != nil {
		return Event{}, err
	}
	unlock, err := lockEvents(folder)
	if err != nil {
		return Event{}, err
	}
	defer unlock()
	events, problems, err := load(folder)
	if err != nil {
		return Event{}, err
	}
	if len(problems) > 0 {
		return Event{}, fmt.Errorf("%w: %s: %s", ErrMalformed, problems[0].File, problems[0].Detail)
	}
	parents := heads(events)
	if len(in.Parents) > 0 {
		parents = in.Parents
	}
	floor := ""
	var floorAt time.Time
	for _, e := range events {
		if e.Actor == in.Actor && e.ID > floor {
			floor = e.ID
		}
		if e.Actor == in.Actor && e.At.After(floorAt) {
			floorAt = e.At
		}
		// Found parents are crossed off, so what is left at the end is absent.
		if i := slices.Index(in.Parents, e.ID); i >= 0 {
			in.Parents = slices.Delete(slices.Clone(in.Parents), i, i+1)
		}
	}
	if len(in.Parents) > 0 {
		return Event{}, fmt.Errorf("%w: parent %s does not exist", ErrInvalid, in.Parents[0])
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	id := nextULID(floor, now)
	at := ulidTime(id).Truncate(time.Second)
	if at.Before(floorAt) {
		at = floorAt
	}
	e := Event{
		ID: id, Kind: in.Kind, At: at, Actor: in.Actor, Task: in.Task,
		Repos: in.Repos, Parents: parents, Commits: in.Commits, Extra: in.Extra, Body: in.Body,
	}
	if e.Body != "" && !strings.HasSuffix(e.Body, "\n") {
		e.Body += "\n"
	}
	if err := writeEvent(filepath.Join(folder, EventsDir), id+eventExt, formatEvent(e)); err != nil {
		return Event{}, err
	}
	return e, nil
}

func validate(in Input) error {
	var problem string
	switch {
	case strings.TrimSpace(in.Actor) == "":
		problem = "no actor"
	case !kindPattern.MatchString(in.Kind):
		problem = "bad kind " + in.Kind
	case slices.Contains(in.Repos, ""):
		problem = "empty repository name"
	}
	if err := checkParents(in.Parents); err != nil {
		problem = err.Error()
	}
	for key, value := range in.Commits {
		if key == "" || value == "" {
			problem = "empty commit entry"
		}
	}
	for key, value := range in.Extra {
		if !keyPattern.MatchString(key) || slices.Contains(knownKeys, key) ||
			strings.ContainsAny(value, "\r\n") || cleanValue(value) != value {
			problem = "bad extra key " + key
		}
	}
	if problem != "" {
		return fmt.Errorf("%w: %s", ErrInvalid, problem)
	}
	return nil
}

// lockEvents takes the folder's lock, held on the events folder itself so that
// no lock file can reach git. The lock ends when the process does.
func lockEvents(folder string) (func(), error) {
	dir, err := os.Open(filepath.Join(folder, EventsDir))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotContextRepo, folder)
	}
	for {
		err = flock(int(dir.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		dir.Close()
		return nil, err
	}
	return func() { dir.Close() }, nil
}

// writeEvent publishes a complete file under its final name or not at all. A
// hard link, unlike a rename, cannot replace an existing event. A temporary
// left by a crash starts with a dot, which readers skip.
func writeEvent(dir, name string, data []byte) error {
	tmp, err := createTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = linkFile(tmp.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		return err
	}
	parent, err := openDir(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
