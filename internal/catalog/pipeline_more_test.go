package catalog

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// A29: Muse advertises model-profile; it is queried like a models listing.
// A hung --version is cut at the probe deadline and reads "unknown".
func TestMuseModelProfileAndHungVersion(t *testing.T) {
	bin := t.TempDir()
	fakeBin(t, bin, "muse", `case "$1" in --help) cat '`+fixturePath(t, "catalogdata/muse-help.txt")+`';; model-profile) printf 'Models:\nm-one\nm-two  (default)\n';; --version) exec sleep 20;; esac`)
	fakeBin(t, bin, "gemini", `case "$1" in --help) printf 'Commands:\n  model-profile   show\n';; model-profile) exit 4;; esac`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
	old := probeTimeout
	probeTimeout = 2 * time.Second
	t.Cleanup(func() { probeTimeout = old })
	start := time.Now()
	ks := Pipeline(context.Background(), ExecRunner, "muse")
	if time.Since(start) > 12*time.Second {
		t.Fatal("the hung --version was waited for")
	}
	m := ks[0]
	if m.Unavailable != "" || len(m.Models) != 2 || m.Models[1].ID != "m-two" || m.Version != "unknown" || m.EffortFlag == "" {
		t.Fatalf("muse %+v", m)
	}
	g := Pipeline(context.Background(), ExecRunner, "gemini")[0]
	if !strings.HasPrefix(g.Unavailable, "gemini model-profile: ") {
		t.Fatalf("gemini %+v", g)
	}
}
