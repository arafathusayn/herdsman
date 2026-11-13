package launch

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/profile"
)

func TestCoreWrapperRefusesSandboxBeforeRegistration(t *testing.T) {
	t.Run("invalid resolved permissions", func(t *testing.T) { coreSandboxRefusal(t, profile.CheckCoreLaunch) })
	t.Run("missing verifier", func(t *testing.T) { coreSandboxRefusal(t, nil) })
}

func coreSandboxRefusal(t *testing.T, verify func(context.Context, string, string, []string, []string, []string) error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'codex-cli 0.164.0'; else echo '{}'; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	args, err := profile.CoreSandboxConfig("codex", profile.Roots{Primary: dir}, filepath.Join(dir, "worker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	d := CoreDescriptor{Intent: "intent", Execution: "execution", Kind: "codex", Harness: []string{bin, "--no-daemon"}, Sandbox: args, Cwd: dir}
	a, b := net.Pipe()
	defer a.Close()
	requests := make(chan []string, 1)
	go func() {
		defer b.Close()
		var ops []string
		dec, enc := json.NewDecoder(b), json.NewEncoder(b)
		for {
			var req WrapRequest
			if dec.Decode(&req) != nil {
				requests <- ops
				return
			}
			ops = append(ops, req.Op)
			reply := WrapReply{OK: true}
			if req.Op == WrapAuthenticate {
				reply.Descriptor = &d
			}
			if enc.Encode(reply) != nil {
				requests <- ops
				return
			}
		}
	}()
	w := Wrapper{Socket: "test", Intent: d.Intent, Stderr: io.Discard, VerifySandbox: verify,
		Dial: func(context.Context, string) (net.Conn, error) { return a, nil },
		Exec: func(string, []string, []string) error { t.Error("unverified sandbox executed"); return nil }}
	if code := w.Main(context.Background()); code != ExitRefused {
		t.Fatalf("exit %d", code)
	}
	for _, op := range <-requests {
		if op == WrapRegister {
			t.Fatal("registered before sandbox verification")
		}
	}
}
