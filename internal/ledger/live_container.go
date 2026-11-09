package ledger

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// EngineName names the container tool, or "" when neither is installed.
func (r *Reader) EngineName(ctx context.Context) string {
	if r.Engine != "" {
		return r.Engine
	}
	for _, name := range []string{"docker", "podman"} {
		if _, err := r.Run(ctx, name, "--version"); !errors.Is(err, exec.ErrNotFound) {
			return name
		}
	}
	return ""
}

// engineDown reports a container tool whose daemon is not running: it can
// hold no running container, so a machine without one still tears down.
func engineDown(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	text := strings.ToLower(string(exit.Stderr))
	for _, phrase := range []string{"cannot connect to the docker daemon", "failed to connect to the docker api", "is the docker daemon running", "cannot connect to podman"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// Created reads when a named volume or network was made. It refuses what it
// cannot establish, because a record with no creation time matches nothing.
func (r *Reader) Created(ctx context.Context, kind, name string) (time.Time, error) {
	engine := r.EngineName(ctx)
	if engine == "" {
		return time.Time{}, fmt.Errorf("ledger: no container tool to read %s %q", kind, name)
	}
	raw, err := r.Run(ctx, engine, kind, "inspect", name)
	if err != nil {
		return time.Time{}, fmt.Errorf("ledger: %s %q: %w", kind, name, err)
	}
	resources, err := ParseResources(raw)
	if err != nil {
		return time.Time{}, err
	}
	for _, resource := range resources {
		if resource.Name == name && !resource.Created.IsZero() {
			return resource.Created, nil
		}
	}
	return time.Time{}, fmt.Errorf("ledger: %s %q has no readable creation time", kind, name)
}

func outputFields(raw []byte) []string {
	var fields []string
	for _, line := range outputLines(raw) {
		fields = append(fields, strings.TrimSpace(line))
	}
	return fields
}

// listThenInspect lists ids and inspects them, for exact labels.
func (r *Reader) listThenInspect(ctx context.Context, engine string, list, inspect []string) ([]byte, error) {
	raw, err := r.Run(ctx, engine, list...)
	if err != nil {
		return nil, err
	}
	ids := outputFields(raw)
	if len(ids) == 0 {
		return nil, nil
	}
	return r.Run(ctx, engine, append(append([]string(nil), inspect...), ids...)...)
}

func (r *Reader) readContainers(ctx context.Context, into *Snapshot) error {
	engine := r.EngineName(ctx)
	if engine == "" {
		return nil
	}
	raw, err := r.listThenInspect(ctx, engine, []string{"ps", "-a", "-q", "--no-trunc"}, []string{"inspect"})
	if engineDown(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ledger: %s containers: %w", engine, err)
	}
	if into.Containers, err = ParseContainerInspect(raw); err != nil {
		return err
	}
	for _, kind := range []struct {
		list, inspect []string
		into          *[]Resource
	}{
		{[]string{"volume", "ls", "-q"}, []string{"volume", "inspect"}, &into.Volumes},
		{[]string{"network", "ls", "--format", "{{.Name}}"}, []string{"network", "inspect"}, &into.Networks},
	} {
		raw, err := r.listThenInspect(ctx, engine, kind.list, kind.inspect)
		if err != nil {
			return fmt.Errorf("ledger: %s %s: %w", engine, kind.list[0], err)
		}
		if *kind.into, err = ParseResources(raw); err != nil {
			return err
		}
	}
	return nil
}
