package core_test

import (
	"encoding/json"
	"testing"

	"github.com/arafathusayn/herdsman/internal/hc/hclive"
	"github.com/arafathusayn/herdsman/internal/herdrapp/apptest"
)

// gRemote starts the terminal through the selected core (`hc run --on
// remote`) on a second private core, added as machine "remote" through
// `hc machine add --via` with a local command in place of ssh. Both cores
// run on this computer, so the record's pid is a live local process: the
// case where a pid alone would pass for identity.
func (w *liveWorld) gRemote(labels map[string]string) string {
	w.t.Helper()
	if w.remote == nil {
		w.remote = hclive.Start(w.t, hclive.Options{})
		w.core.AddMachine(w.remote, "remote")
		w.appRemote = apptest.NewCore("remote-core")
		w.app.AddMachine("remote", w.appRemote)
	}
	args := []string{"run", "--on", "remote", "--bg", "--json"}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	out, err := w.core.HC(append(args, "/bin/sleep", w.mark)...)
	var r struct {
		Address string `json:"address"`
	}
	if err != nil || json.Unmarshal(out, &r) != nil || r.Address == "" {
		w.t.Fatalf("hc run --on remote: %v\n%s", err, out)
	}
	raw, err := w.core.GetRaw(r.Address)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Logf("core's record of %s: machine field %v, pid %v, labels %v", r.Address, raw["machine"], raw["pid"], raw["labels"])
	return r.Address
}

var _ remoteGate = (*liveWorld)(nil)

// Remote records, against a real core with a second real core added as a
// machine.
func TestLiveRemoteRecordNeverLocal(t *testing.T) {
	gateRemoteRecordNeverLocal(newLive(t, hclive.Options{}))
}
