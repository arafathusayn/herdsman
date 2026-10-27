package inbox_test

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

func TestDigestReaderMatchesDigest(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 255, 256, 65536, 200001} {
		b := make([]byte, n)
		r.Read(b)
		got, err := inbox.DigestReader(bytes.NewReader(b))
		if err != nil || got != inbox.Digest(b) {
			t.Errorf("%d bytes: %s, want %s (%v)", n, got, inbox.Digest(b), err)
		}
	}
	if _, err := inbox.DigestFileFast(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("a missing file has no digest")
	}
}

// binaryProof rewrites a case's proof in the binary form, naming exe.
func binaryProof(c *inboxtest.Case, exe string) {
	sum, _ := inbox.DigestFileFast(exe)
	rule, _ := inbox.DigestFile(filepath.Join(c.Base, "ignore-rule"))
	c.WriteJSON(c.Proof, map[string]any{"inbox": c.Dir, "policy_verified": true,
		"rules":  []any{map[string]any{"path": filepath.Join(c.Base, "ignore-rule"), "checksum": rule}},
		"binary": map[string]any{"path": exe, "checksum": sum}})
}

func TestBinaryProof(t *testing.T) {
	c := inboxtest.New(t)
	exe := filepath.Join(c.Base, "herdsman")
	c.Write(exe, strings.Repeat("binary bytes\n", 1000))
	o := c.Options(false)
	binaryProof(c, exe)
	ch, err := inbox.OpenChannel(o, exe)
	if err != nil || ch.ProofStale || !ch.ProofValid() {
		t.Fatalf("a binary proof of the running executable: %v", err)
	}
	for name, edit := range map[string]func(){
		"another executable": func() {
			binaryProof(c, exe)
			o2 := filepath.Join(c.Base, "other")
			c.Write(o2, "x")
			binaryProof(c, o2)
		},
		"a changed binary": func() { binaryProof(c, exe); c.Write(exe, "changed\n") },
		"a changed rule":   func() { binaryProof(c, exe); c.Write(filepath.Join(c.Base, "ignore-rule"), "changed\n") },
		"no binary entry": func() {
			binaryProof(c, exe)
			c.Edit(c.Proof, func(m map[string]any) { delete(m, "binary") })
		},
		"a relative binary path": func() {
			binaryProof(c, exe)
			c.Edit(c.Proof, func(m map[string]any) { m["binary"].(map[string]any)["path"] = "herdsman" })
		},
		"an unverified policy": func() {
			binaryProof(c, exe)
			c.Edit(c.Proof, func(m map[string]any) { m["policy_verified"] = false })
		},
	} {
		c.Write(exe, strings.Repeat("binary bytes\n", 1000))
		c.Write(filepath.Join(c.Base, "ignore-rule"), "sandbox policy\n")
		edit()
		if _, err := inbox.OpenChannel(o, exe); err == nil {
			t.Errorf("%s: the proof verified", name)
		} else if msg, _ := inbox.IsFatal(err); !strings.Contains(msg, "proof stale") {
			t.Errorf("%s: %v", name, err)
		}
		ro := o
		ro.ReadOnly = true
		if ch, err := inbox.OpenChannel(ro, exe); err != nil || !ch.ProofStale {
			t.Errorf("%s: read-only open: %v", name, err)
		}
	}
	if _, err := inbox.OpenChannel(o, ""); err == nil {
		t.Error("no executable accepts no binary proof")
	}
	// The proof of the running binary, with no executable named.
	c.Write(filepath.Join(c.Base, "ignore-rule"), "sandbox policy\n")
	c.Prove()
	if _, err := inbox.OpenChannel(c.Options(false), ""); err != nil {
		t.Errorf("the running binary's proof: %v", err)
	}
	// A conflicting timing file is fatal for a writer.
	c.Write(filepath.Join(c.Dir, "state", "timing.json"), `{"health":"x"}`)
	if _, err := inbox.OpenChannel(c.Options(false), ""); err == nil {
		t.Error("invalid timing opened")
	} else if msg, _ := inbox.IsFatal(err); !strings.Contains(msg, "timing") {
		t.Errorf("invalid timing: %v", err)
	}
	os.Remove(filepath.Join(c.Dir, "state", "timing.json"))
	if _, err := inbox.OpenChannel(inbox.Options{Dir: "relative"}, exe); err == nil {
		t.Error("a relative inbox opened")
	}
}
