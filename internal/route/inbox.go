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
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/ops"
)

type proofEntry struct {
	Path     string `json:"path"`
	Checksum string `json:"checksum"`
}

type setupProof struct {
	Inbox          string       `json:"inbox"`
	PolicyVerified bool         `json:"policy_verified"`
	Rules          []proofEntry `json:"rules"`
	Binary         proofEntry   `json:"binary"`
}

func applyInbox(_ context.Context, _ Deps, _ *herdr.Client, d stepData) error {
	if err := checkInboxPlace(d); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.Inbox), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(d.Inbox, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := os.Chmod(d.Inbox, 0o700); err != nil {
		return err
	}
	proof := setupProof{Inbox: d.Inbox}
	for _, path := range d.PolicyFiles {
		entry, err := proofOf(path)
		if err != nil {
			return fmt.Errorf("policy file: %w", err)
		}
		proof.Rules = append(proof.Rules, entry)
	}
	if len(proof.Rules) == 0 {
		return errors.New("no sandbox policy file found to prove; the setup proof needs at least one")
	}
	binary, err := proofOf(d.Executable)
	if err != nil {
		return fmt.Errorf("herdsman binary: %w", err)
	}
	proof.Binary = binary
	proof.PolicyVerified = true
	b, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d.Inbox, "setup.json"), b, 0o600); err != nil {
		return err
	}
	for name, content := range map[string]string{"watch-targets.json": "[]", "resume-targets.json": "{}"} {
		if err := writeOnce(filepath.Join(d.Inbox, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// checkInboxPlace is the probe behind policy_verified: the inbox must lie
// outside the repository, the only tree an implementer can always write.
func checkInboxPlace(d stepData) error {
	rel, err := filepath.Rel(d.Repo, d.Inbox)
	if err != nil {
		return err
	}
	if rel == "." || !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("the inbox %s lies inside the repository", d.Inbox)
	}
	return nil
}

func proofOf(path string) (proofEntry, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return proofEntry{}, err
	}
	sum, err := inbox.DigestFile(real)
	if err != nil {
		return proofEntry{}, err
	}
	return proofEntry{Path: real, Checksum: sum}, nil
}

func probeInbox(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(filepath.Join(d.Inbox, "setup.json")); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

type peerIdentity struct {
	Pane    string `json:"pane"`
	Session any    `json:"session"`
}

// applyTargets records every enrolled pane as a card-watch target.
func applyTargets(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	targets := make([]peerIdentity, 0, len(d.Peers))
	for _, name := range d.Peers {
		record, err := c.AgentGet(ctx, name)
		if err != nil {
			return fmt.Errorf("agent %s: %w", name, err)
		}
		id, code, note := deps.verifier(c).Live(ctx, inbox.Ref{Backend: deps.provider(c).Backend(), Pane: record.PaneID})
		if code != inbox.LiveOK {
			return fmt.Errorf("agent %s has no live identity: %s", name, note)
		}
		targets = append(targets, peerIdentity{Pane: id.Pane, Session: map[string]any{"agent": id.Session.Agent, "kind": id.Session.Kind, "source": id.Session.Source, "value": id.Session.Value}})
	}
	b, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.Inbox, "watch-targets.json"), b, 0o600)
}

func probeTargets(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	b, err := os.ReadFile(filepath.Join(d.Inbox, "watch-targets.json"))
	if err != nil || bytes.Equal(b, []byte("[]")) {
		return ops.Absent, "", nil
	}
	return ops.Matches, "", nil
}

func runChannel(ctx context.Context, deps Deps, d stepData, pane, action string, args ...string) error {
	if deps.Channel == nil {
		return errors.New("this build has no channel runner")
	}
	return deps.Channel(ctx, ChannelCall{Inbox: d.Inbox, Socket: d.Socket, Executable: d.Executable, Pane: pane, Action: action, Args: args})
}

func applyPropose(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	record, err := c.AgentGet(ctx, d.Name)
	if err != nil {
		return fmt.Errorf("agent %s: %w", d.Name, err)
	}
	if err := runChannel(ctx, deps, d, record.PaneID, ChannelRegister, "propose", d.Name, d.Role); err != nil {
		return fmt.Errorf("enroll %s: %w", d.Name, err)
	}
	return nil
}

func probePropose(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	b, err := os.ReadFile(filepath.Join(d.Inbox, "peers"))
	if err == nil && bytes.Contains(b, []byte(`"`+d.Name+`"`)) {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

func handshakeBody(d stepData) string { return filepath.Join(d.Folder, ".steps", "handshake-body") }

// applyHandshake seeds the pair's first message. The nonce derives from the
// route, so a retried handshake is recognizably the same one.
func applyHandshake(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	record, err := c.AgentGet(ctx, d.Adjutant)
	if err != nil {
		return fmt.Errorf("agent %s: %w", d.Adjutant, err)
	}
	body := fmt.Sprintf("handshake %s: adjutant %s, orchestrator %s\n", d.Route, d.Adjutant, d.Orchestrator)
	if err := writeFileNew(handshakeBody(d), []byte(body)); err != nil {
		return err
	}
	if err := runChannel(ctx, deps, d, record.PaneID, ChannelSend, d.Adjutant, d.Orchestrator, handshakeBody(d), "observation"); err != nil {
		return fmt.Errorf("seed the handshake: %w", err)
	}
	return writeFileNew(handshakeBody(d)+".sent", nil)
}

func probeHandshake(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(handshakeBody(d) + ".sent"); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}
