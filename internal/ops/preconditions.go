package ops

import (
	"context"
	"fmt"
	"strings"
)

// Check reads the current value of a fact about subject.
type Check func(ctx context.Context, subject string) (string, error)

// check resolves a precondition's check and subject.
func (e *Engine) check(p Precondition) (Check, string, error) {
	name, subject, _ := strings.Cut(p.Name, ":")
	c, ok := e.opt.Checks[name]
	if !ok {
		return nil, "", fmt.Errorf("ops: unknown precondition check %q", name)
	}
	return c, subject, nil
}

// checkAll compares every precondition with the world.
func (e *Engine) checkAll(ctx context.Context, ps []Precondition) error {
	for _, p := range ps {
		c, subject, err := e.check(p)
		if err != nil {
			return err
		}
		got, err := c(ctx, subject)
		if err != nil {
			return fmt.Errorf("ops: precondition %s: %w", p.Name, err)
		}
		if got != p.Want {
			return fmt.Errorf("%w: %s is %q, the plan assumed %q", ErrPrecondition, p.Name, got, p.Want)
		}
	}
	return nil
}
