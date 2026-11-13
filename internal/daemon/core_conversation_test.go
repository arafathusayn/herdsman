package daemon

import (
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// Conversation evidence is read only from the harnesses' session folders of
// the login environment, the folders the checkpointer reads transcripts from,
// or from the configured ones; with no HOME there, from none.
func TestCoreSessionFoldersFollowTheLoginEnvironment(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h")
	c := &coreRuntime{cfg: CoreConfig{Environ: []string{"PATH=/usr/bin:/bin", "HOME=" + home}}}
	got := c.sessions()
	if got == nil || got.Claude != filepath.Join(home, ".claude", "projects") || got.Codex != filepath.Join(home, ".codex", "sessions") {
		t.Fatalf("from HOME: %+v", got)
	}
	codexHome := filepath.Join(t.TempDir(), "codex")
	c.cfg.Environ = append(c.cfg.Environ, "CODEX_HOME="+codexHome)
	if got := c.sessions(); got.Codex != filepath.Join(codexHome, "sessions") {
		t.Fatalf("from CODEX_HOME: %+v", got)
	}
	c.cfg.Environ = []string{"PATH=/usr/bin:/bin"}
	if got := c.sessions(); got != nil {
		t.Fatalf("with no HOME: %+v", got)
	}
	fixed := &core.SessionRoots{Claude: filepath.Join(t.TempDir(), "p")}
	c.cfg.Sessions = fixed
	if got := c.sessions(); got != fixed {
		t.Fatalf("the configured folders: %+v", got)
	}
}
