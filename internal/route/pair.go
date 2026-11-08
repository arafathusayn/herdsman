package route

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// PairState returns the pending registry's checksum and whether it is the
// approved one.
func PairState(dir string) (sum string, approved bool, err error) {
	pending, err := os.ReadFile(filepath.Join(dir, "peers"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sum, err = inbox.DigestFile(filepath.Join(dir, "peers"))
	if err != nil {
		return "", false, err
	}
	current, err := os.ReadFile(filepath.Join(dir, "peers.approved"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	return sum, bytes.Equal(pending, current) && len(bytes.TrimSpace(pending)) > 2, nil
}

func approveLine(d stepData, sum string) string {
	return fmt.Sprintf("HERDSMAN_INBOX=%s HERDSMAN_PREFLIGHT=%s %s message register approve %s",
		shellQuote(d.Inbox), shellQuote(filepath.Join(d.Inbox, "setup.json")), shellQuote(d.Executable), sum)
}

func pendingError(d stepData, sum, why string) error {
	return fmt.Errorf("the pair is left pending: %s; run: %s; then: herdsman up %s --continue", why, approveLine(d, sum), d.Route)
}

// approvalRecord names who approved a pair and from where.
type approvalRecord struct {
	By        string `json:"by"`
	Operation string `json:"operation"`
	Key       string `json:"key"`
	PID       int    `json:"pid"`
	Started   string `json:"started,omitempty"`
	Pane      string `json:"pane,omitempty"`
	TTY       string `json:"tty,omitempty"`
	Manual    bool   `json:"manual"`
	Checksum  string `json:"checksum"`
	Time      string `json:"time"`
}

func applyApprove(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	sum, approved, err := PairState(d.Inbox)
	if err != nil {
		return err
	}
	if approved {
		return nil
	}
	if sum == "" {
		return errors.New("no pending pair to approve")
	}
	caller, ok := deps.Callers.Get(d.Key)
	if !ok {
		return pendingError(d, sum, "no caller is known to attest")
	}
	if d.Manual {
		if caller.Confirmed != sum {
			return pendingError(d, sum, "confirm the identities on the terminal")
		}
		if err := verifyCaller(caller); err != nil {
			return pendingError(d, sum, err.Error())
		}
	} else if err := attest(ctx, c, caller); err != nil {
		return pendingError(d, sum, err.Error())
	}
	if err := runChannel(ctx, deps, d, caller.Pane, ChannelRegister, "approve", sum); err != nil {
		return fmt.Errorf("approve the pair: %w", err)
	}
	record := approvalRecord{By: "herdsman up", Operation: d.Op, Key: d.Key, PID: caller.PID, Pane: caller.Pane, TTY: caller.TTY, Manual: d.Manual, Checksum: sum, Time: time.Now().UTC().Format(time.RFC3339)}
	if !caller.Started.IsZero() {
		record.Started = caller.Started.UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.Inbox, "pair-approval.json"), b, 0o600)
}

func probeApprove(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	_, approved, err := PairState(d.Inbox)
	if err != nil {
		return ops.Unknown, "", err
	}
	if approved {
		return ops.Matches, "", nil
	}
	return ops.Unknown, "the pair is not approved", nil
}
