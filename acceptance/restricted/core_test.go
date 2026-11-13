//go:build acceptance

package restricted

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/profile"
)

// This child runs under the real Codex sandbox; it uses no model or login.
func TestCoreSandboxChild(t *testing.T) {
	if os.Getenv("HERDSMAN_SANDBOX_CHILD") != "1" {
		return
	}
	var paths map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("HERDSMAN_SANDBOX_PATHS")), &paths); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker", "control", "core", "tcp"} {
		network := "unix"
		if name == "tcp" {
			network = "tcp"
		}
		conn, err := net.DialTimeout(network, paths[name], time.Second)
		if conn != nil {
			conn.Close()
		}
		if (err == nil) != (name == "worker") {
			t.Errorf("%s connection allowed=%v", name, err == nil)
		}
	}
	for _, name := range []string{"primary", "extra", "protected", "git"} {
		err := os.WriteFile(filepath.Join(paths[name], "probe"), []byte("fixture"), 0600)
		if (err == nil) != (name != "protected") {
			t.Errorf("%s write allowed=%v", name, err == nil)
		}
	}
}

func TestCoreSandboxExactSocketAndInheritedGrants(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not installed")
	}
	// Keep the filesystem control outside Codex's writable temporary roots.
	root, err := os.MkdirTemp(".", ".core-sandbox-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, name := range []string{"primary", "extra", "protected"} {
		paths[name] = filepath.Join(root, name)
		if err := os.Mkdir(paths[name], 0700); err != nil {
			t.Fatal(err)
		}
	}
	paths["git"] = filepath.Join(paths["primary"], ".git")
	if err := os.Mkdir(paths["git"], 0700); err != nil {
		t.Fatal(err)
	}
	sockets, err := os.MkdirTemp("/tmp", "hs-sandbox-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	for _, name := range []string{"worker", "control", "core"} {
		paths[name] = filepath.Join(sockets, name+".sock")
		l, err := net.Listen("unix", paths[name])
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	paths["tcp"] = tcp.Addr().String()
	home := filepath.Join(root, "codex")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	// Codex merges maps: an old fixed profile is intentionally too broad.
	config := "[permissions.herdsman.filesystem]\n" + strconv.Quote(paths["protected"]) + "=\"write\"\n[permissions.herdsman.network]\nenabled=true\ndangerously_allow_all_unix_sockets=true\n[permissions.herdsman.network.domains]\n\"*\"=\"allow\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := profile.CoreSandboxConfig("codex", profile.Roots{Primary: paths["primary"], Extra: []string{paths["extra"]}}, paths["worker"])
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(paths)
	env := []string{"HOME=" + root, "PATH=" + os.Getenv("PATH"), "CODEX_HOME=" + home, "HERDSMAN_SANDBOX_CHILD=1", "HERDSMAN_SANDBOX_PATHS=" + string(raw)}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := profile.CheckCoreSandbox(ctx, codex, paths["primary"], env, args); err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"sandbox"}, args...)
	argv = append(argv, "--", self, "-test.run=^TestCoreSandboxChild$", "-test.v")
	cmd := exec.CommandContext(ctx, codex, argv...)
	cmd.Dir = paths["primary"]
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandbox: %v\n%s", err, out)
	}
	// Some versions prioritize legacy settings differently. A selected
	// generated profile is acceptable only while the same real isolation
	// checks still hold; user-supplied legacy argv is refused separately.
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("sandbox_mode=\"danger-full-access\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := profile.CheckCoreSandbox(ctx, codex, paths["primary"], env, args); err == nil {
		cmd := exec.CommandContext(ctx, codex, argv...)
		cmd.Dir = paths["primary"]
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("legacy setting defeated generated profile: %v\n%s", err, out)
		}
	}
}
